// Package tarindex implements a read-only tar.gz content index and search
// service.
//
// An Archive holds an ordered list of Entries parsed from a gzip-compressed
// tar stream. The parser reads the stream once (via gzip + tar), classifies each
// entry by its tar Typeflag, and keeps the header metadata (name, size, type,
// mode, modtime, link target). The archive is never extracted to disk and its
// file contents are not retained — only headers are indexed.
//
// The Service owns a registry of named archives and answers filtered, sorted,
// paginated search queries over them.
package tarindex

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"path"
	"sort"
	"sync"
)

// Sentinel errors returned by the service. The HTTP layer maps these to
// status codes.
var (
	ErrNotFound       = errors.New("压缩包不存在")
	ErrInvalidArchive = errors.New("压缩包格式错误")
	ErrInvalidType    = errors.New("type 非法")
	ErrInvalidSort    = errors.New("sort 非法")
	ErrInvalidLimit   = errors.New("limit 非法")
	ErrInvalidOffset  = errors.New("offset 非法")
	ErrInvalidSize    = errors.New("size 非法")
	ErrBadPattern     = errors.New("name 通配模式非法")
)

// Entry is one indexed tar record.
type Entry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Type    string `json:"type"`
	Mode    int64  `json:"mode"`
	ModTime int64  `json:"mod_time"`
	Link    string `json:"link,omitempty"`
}

// Summary is the metadata snapshot of an archive.
type Summary struct {
	ID        string         `json:"id"`
	Entries   int            `json:"entries"`
	TotalSize int64          `json:"total_size"`
	ByType    map[string]int `json:"by_type"`
}

// SearchResp is the paginated search result.
type SearchResp struct {
	Items  []Entry `json:"items"`
	Total  int     `json:"total"`
	Offset int     `json:"offset"`
	Limit  int     `json:"limit"`
}

// Filters holds the parsed query parameters for a search. It is exported so
// the HTTP layer can populate it from request query strings.
type Filters struct {
	TypeF   string
	MinSize int64
	MaxSize int64
	MinSet  bool
	MaxSet  bool
	Name    string
	Sort    string
	Limit   int
	Offset  int
}

// Archive is an indexed tar.gz archive: an ordered list of entries.
type Archive struct {
	entries []Entry
}

// classifyType maps a tar Typeflag to the index's type classification.
func classifyType(flag byte) string {
	switch flag {
	case tar.TypeReg, tar.TypeRegA:
		return "file"
	case tar.TypeDir:
		return "dir"
	case tar.TypeSymlink:
		return "symlink"
	default:
		return "other"
	}
}

// Parse reads a gzip-compressed tar stream and returns the indexed entries in
// tar order. It returns ErrInvalidArchive if the stream is not valid gzip or
// not a valid tar.
func Parse(r io.Reader) ([]Entry, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, ErrInvalidArchive
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := []Entry{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, ErrInvalidArchive
		}
		entries = append(entries, Entry{
			Name:    hdr.Name,
			Size:    hdr.Size,
			Type:    classifyType(hdr.Typeflag),
			Mode:    int64(hdr.Mode),
			ModTime: hdr.ModTime.Unix(),
			Link:    hdr.Linkname,
		})
	}
	return entries, nil
}

// summarize builds a Summary for the given id and entries.
func summarize(id string, entries []Entry) Summary {
	byType := map[string]int{"file": 0, "dir": 0, "symlink": 0, "other": 0}
	var total int64
	for _, e := range entries {
		byType[e.Type]++
		total += e.Size
	}
	return Summary{ID: id, Entries: len(entries), TotalSize: total, ByType: byType}
}

// search applies the filters and returns the matched entries, sorted and
// pre-pagination.
func (a *Archive) search(f Filters) ([]Entry, error) {
	if f.Name != "" {
		// Validate the glob pattern once. path.Match returns ErrBadPattern for
		// an invalid pattern regardless of the matched string.
		if _, err := path.Match(f.Name, ""); err != nil {
			return nil, ErrBadPattern
		}
	}
	var matched []Entry
	for _, e := range a.entries {
		if f.TypeF != "" && e.Type != f.TypeF {
			continue
		}
		if f.MinSet && e.Size < f.MinSize {
			continue
		}
		if f.MaxSet && e.Size > f.MaxSize {
			continue
		}
		if f.Name != "" {
			ok, _ := path.Match(f.Name, e.Name)
			if !ok {
				continue
			}
		}
		matched = append(matched, e)
	}
	switch f.Sort {
	case "size":
		sort.SliceStable(matched, func(i, j int) bool {
			return matched[i].Size < matched[j].Size
		})
	default: // "name"
		sort.SliceStable(matched, func(i, j int) bool {
			return matched[i].Name < matched[j].Name
		})
	}
	return matched, nil
}

// Service owns a registry of named archives.
type Service struct {
	mu       sync.Mutex
	archives map[string]*Archive
	nextID   int64
}

// NewService creates an empty service.
func NewService() *Service {
	return &Service{archives: map[string]*Archive{}}
}

// Create parses a gzip+tar stream, stores the archive, and returns its summary.
func (s *Service) Create(r io.Reader) (Summary, error) {
	entries, err := Parse(r)
	if err != nil {
		return Summary{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := idString(s.nextID)
	s.archives[id] = &Archive{entries: entries}
	return summarize(id, entries), nil
}

// Get returns the summary of an archive.
func (s *Service) Get(id string) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.archives[id]
	if !ok {
		return Summary{}, ErrNotFound
	}
	return summarize(id, a.entries), nil
}

// Search runs a filtered, sorted, paginated query over an archive.
func (s *Service) Search(id string, f Filters) (SearchResp, error) {
	if f.TypeF != "" && f.TypeF != "file" && f.TypeF != "dir" && f.TypeF != "symlink" && f.TypeF != "other" {
		return SearchResp{}, ErrInvalidType
	}
	if f.Sort != "" && f.Sort != "name" && f.Sort != "size" {
		return SearchResp{}, ErrInvalidSort
	}
	if f.Sort == "" {
		f.Sort = "name"
	}
	if f.Limit == 0 {
		f.Limit = 50
	}
	if f.Limit < 0 {
		return SearchResp{}, ErrInvalidLimit
	}
	if f.Limit > 1000 {
		f.Limit = 1000
	}
	if f.Offset < 0 {
		return SearchResp{}, ErrInvalidOffset
	}
	a, ok := s.archives[id]
	if !ok {
		return SearchResp{}, ErrNotFound
	}
	matched, err := a.search(f)
	if err != nil {
		return SearchResp{}, err
	}
	total := len(matched)
	start := f.Offset
	if start > total {
		start = total
	}
	end := start + f.Limit + 1
	if end > total {
		end = total
	}
	items := matched[start:end]
	return SearchResp{Items: items, Total: total, Offset: f.Offset, Limit: f.Limit}, nil
}

// Delete removes an archive. It reports whether the archive existed.
func (s *Service) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.archives[id]
	delete(s.archives, id)
	return ok
}

// idString formats a numeric id as "ar-N".
func idString(n int64) string {
	if n == 0 {
		return "ar-0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return "ar-" + string(buf[i:])
}
