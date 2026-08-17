// Package selfcheck runs an end-to-end verification of the tarindex service
// against an in-process HTTP server. It is invoked by the --smoke-test flag
// and exits the process on completion.
//
// Each scenario builds its own fresh service+server so global state never
// leaks between scenarios. Test archives are constructed in-memory with the
// archive/tar and compress/gzip packages, so no external fixtures are needed.
package selfcheck

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"task042-tarindex/internal/httpapi"
	"task042-tarindex/internal/tarindex"
)

// fixedModTime is the deterministic timestamp stamped on every test entry.
const fixedModTime = 1700000000

// tentry describes a tar entry to build into a test archive.
type tentry struct {
	name     string
	typeflag byte
	mode     int64
	size     int64
	link     string
}

// client wraps a fresh httptest server bound to a fresh service.
type client struct {
	base string
	c    *http.Client
	srv  *httptest.Server
}

func newClient() *client {
	svc := tarindex.NewService()
	srv := httptest.NewServer(httpapi.New(svc).Handler())
	return &client{base: srv.URL, c: srv.Client(), srv: srv}
}

func (cl *client) close() { cl.srv.Close() }

// postArchive uploads raw gzip bytes and returns the status code and decoded
// JSON body.
func (cl *client) postArchive(path string, body []byte) (int, map[string]any) {
	req, _ := http.NewRequest(http.MethodPost, cl.base+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/gzip")
	resp, err := cl.c.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	return decodeBody(resp)
}

func (cl *client) get(path string) (int, map[string]any) {
	resp, err := cl.c.Get(cl.base + path)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	return decodeBody(resp)
}

func (cl *client) del(path string) int {
	req, _ := http.NewRequest(http.MethodDelete, cl.base+path, nil)
	resp, err := cl.c.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func decodeBody(resp *http.Response) (int, map[string]any) {
	data, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out)
	}
	return resp.StatusCode, out
}

// eqInt compares a JSON-decoded number to an expected integer.
func eqInt(v any, want int) bool {
	f, ok := v.(float64)
	return ok && int64(f) == int64(want)
}

// buildTar encodes the given entries into a gzip-compressed tar byte slice.
// Regular files are filled with 'x' bytes to match their declared size.
func buildTar(entries []tentry) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	mt := time.Unix(fixedModTime, 0)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     e.mode,
			Size:     e.size,
			ModTime:  mt,
			Typeflag: e.typeflag,
			Linkname: e.link,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			panic(err)
		}
		if e.typeflag == tar.TypeReg && e.size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(e.size))); err != nil {
				panic(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := gz.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// basicEntries is the canonical four-entry archive used across scenarios.
func basicEntries() []tentry {
	return []tentry{
		{name: "a.txt", typeflag: tar.TypeReg, mode: 0644, size: 3},
		{name: "b.go", typeflag: tar.TypeReg, mode: 0644, size: 5},
		{name: "dir/", typeflag: tar.TypeDir, mode: 0755, size: 0},
		{name: "dir/c.txt", typeflag: tar.TypeReg, mode: 0644, size: 2},
	}
}

// uploadBasic uploads the canonical archive and returns its id.
func uploadBasic(cl *client) string {
	code, body := cl.postArchive("/archives", buildTar(basicEntries()))
	if code != http.StatusOK {
		panic(fmt.Sprintf("uploadBasic: code=%d body=%v", code, body))
	}
	return body["id"].(string)
}

// entriesByName returns the items array of a search keyed by name for stable
// lookup; the slice is in response order (already sorted by the service).
func searchItems(cl *client, id, query string) []any {
	code, body := cl.get("/archives/" + id + "/entries" + query)
	if code != http.StatusOK {
		panic(fmt.Sprintf("search %s: code=%d body=%v", query, code, body))
	}
	items, _ := body["items"].([]any)
	return items
}

func entryName(e any) string {
	if m, ok := e.(map[string]any); ok {
		if s, ok := m["name"].(string); ok {
			return s
		}
	}
	return ""
}

func entrySize(e any) int64 {
	if m, ok := e.(map[string]any); ok {
		if s, ok := m["size"].(float64); ok {
			return int64(s)
		}
	}
	return -1
}

func entryField(e any, key string) string {
	if m, ok := e.(map[string]any); ok {
		if s, ok := m[key].(string); ok {
			return s
		}
	}
	return ""
}

// names extracts the name sequence from an items array.
func names(items []any) []string {
	out := make([]string, 0, len(items))
	for _, e := range items {
		out = append(out, entryName(e))
	}
	return out
}

// byTypeField returns the count for a type key from the by_type map.
func byTypeField(body map[string]any, key string) int {
	bt, _ := body["by_type"].(map[string]any)
	if bt == nil {
		return -1
	}
	return int(bt[key].(float64))
}

// Run exercises the full HTTP API across isolated scenarios, returning nil if
// every behavior matches the specification.
func Run() error {
	scenarios := []struct {
		name string
		fn   func() error
	}{
		{"健康检查", scenarioHealth},
		{"上传与摘要", scenarioUploadSummary},
		{"查询摘要", scenarioGetSummary},
		{"按类型过滤", scenarioFilterType},
		{"名称通配不跨分隔符", scenarioNameGlob},
		{"大小下界过滤", scenarioMinSize},
		{"大小上界过滤", scenarioMaxSize},
		{"按大小排序", scenarioSortSize},
		{"分页", scenarioPagination},
		{"同名条目不去重", scenarioDuplicateNames},
		{"符号链接索引", scenarioSymlink},
		{"空压缩包", scenarioEmptyArchive},
		{"非法 gzip 400", scenarioInvalidGzip},
		{"合法 gzip 非 tar 400", scenarioGzipNotTar},
		{"非法通配模式 400", scenarioBadPattern},
		{"不存在压缩包 404", scenarioNotFound},
		{"删除压缩包", scenarioDelete},
	}
	for _, sc := range scenarios {
		if err := sc.fn(); err != nil {
			return fmt.Errorf("%s: %w", sc.name, err)
		}
	}
	return nil
}

func scenarioHealth() error {
	cl := newClient()
	defer cl.close()
	code, body := cl.get("/healthz")
	if code != http.StatusOK || body["status"] != "ok" {
		return fmt.Errorf("healthz: code=%d body=%v", code, body)
	}
	return nil
}

func scenarioUploadSummary() error {
	cl := newClient()
	defer cl.close()
	code, body := cl.postArchive("/archives", buildTar(basicEntries()))
	if code != http.StatusOK {
		return fmt.Errorf("upload: code=%d body=%v", code, body)
	}
	if body["id"] == nil || body["id"] == "" {
		return fmt.Errorf("upload: missing id: %v", body)
	}
	if !eqInt(body["entries"], 4) {
		return fmt.Errorf("upload: entries=%v want 4", body["entries"])
	}
	if !eqInt(body["total_size"], 10) {
		return fmt.Errorf("upload: total_size=%v want 10", body["total_size"])
	}
	for _, tc := range []struct {
		typ string
		n   int
	}{
		{"file", 3}, {"dir", 1}, {"symlink", 0}, {"other", 0},
	} {
		if byTypeField(body, tc.typ) != tc.n {
			return fmt.Errorf("upload: by_type[%s]=%d want %d", tc.typ, byTypeField(body, tc.typ), tc.n)
		}
	}
	return nil
}

func scenarioGetSummary() error {
	cl := newClient()
	defer cl.close()
	id := uploadBasic(cl)
	code, body := cl.get("/archives/" + id)
	if code != http.StatusOK {
		return fmt.Errorf("get: code=%d body=%v", code, body)
	}
	if !eqInt(body["entries"], 4) || !eqInt(body["total_size"], 10) {
		return fmt.Errorf("get: entries=%v total_size=%v want 4/10", body["entries"], body["total_size"])
	}
	// Every entry carries the fixed mod_time and a non-negative mode.
	items := searchItems(cl, id, "")
	for _, e := range items {
		m := e.(map[string]any)
		if !eqInt(m["mod_time"], fixedModTime) {
			return fmt.Errorf("get: entry %v mod_time=%v want %d", m["name"], m["mod_time"], fixedModTime)
		}
		if eq, _ := m["mode"].(float64); eq < 0 {
			return fmt.Errorf("get: entry %v mode=%v", m["name"], m["mode"])
		}
	}
	return nil
}

func scenarioFilterType() error {
	cl := newClient()
	defer cl.close()
	id := uploadBasic(cl)
	items := searchItems(cl, id, "?type=file")
	if len(items) != 3 {
		return fmt.Errorf("type=file: len=%d want 3", len(items))
	}
	got := names(items)
	want := []string{"a.txt", "b.go", "dir/c.txt"}
	for i := range got {
		if got[i] != want[i] {
			return fmt.Errorf("type=file order=%v want %v", got, want)
		}
	}
	return nil
}

func scenarioNameGlob() error {
	cl := newClient()
	defer cl.close()
	id := uploadBasic(cl)
	// *.go matches b.go only.
	items := searchItems(cl, id, "?name=*.go")
	if got := names(items); len(got) != 1 || got[0] != "b.go" {
		return fmt.Errorf("name=*.go: %v want [b.go]", got)
	}
	// *.txt matches a.txt but NOT dir/c.txt (* does not cross /).
	items = searchItems(cl, id, "?name=*.txt")
	if got := names(items); len(got) != 1 || got[0] != "a.txt" {
		return fmt.Errorf("name=*.txt: %v want [a.txt]", got)
	}
	return nil
}

func scenarioMinSize() error {
	cl := newClient()
	defer cl.close()
	id := uploadBasic(cl)
	// min_size=3 -> a.txt(3), b.go(5); dir/c.txt(2) and dir(0) excluded.
	items := searchItems(cl, id, "?min_size=3")
	got := names(items)
	want := []string{"a.txt", "b.go"}
	if len(got) != 2 || got[0] != "a.txt" || got[1] != "b.go" {
		return fmt.Errorf("min_size=3: %v want %v", got, want)
	}
	return nil
}

func scenarioMaxSize() error {
	cl := newClient()
	defer cl.close()
	id := uploadBasic(cl)
	// max_size=2 -> dir(0), dir/c.txt(2); a.txt(3), b.go(5) excluded.
	items := searchItems(cl, id, "?max_size=2")
	got := names(items)
	want := []string{"dir/", "dir/c.txt"}
	if len(got) != 2 || got[0] != "dir/" || got[1] != "dir/c.txt" {
		return fmt.Errorf("max_size=2: %v want %v", got, want)
	}
	return nil
}

func scenarioSortSize() error {
	cl := newClient()
	defer cl.close()
	id := uploadBasic(cl)
	// sort=size ascending: dir(0), dir/c.txt(2), a.txt(3), b.go(5).
	items := searchItems(cl, id, "?sort=size")
	got := names(items)
	want := []string{"dir/", "dir/c.txt", "a.txt", "b.go"}
	if len(got) != 4 {
		return fmt.Errorf("sort=size: len=%d want 4", len(got))
	}
	for i := range got {
		if got[i] != want[i] {
			return fmt.Errorf("sort=size: %v want %v", got, want)
		}
	}
	// Verify the sizes line up.
	sizes := []int64{0, 2, 3, 5}
	for i, e := range items {
		if entrySize(e) != sizes[i] {
			return fmt.Errorf("sort=size: entry %d size=%d want %d", i, entrySize(e), sizes[i])
		}
	}
	return nil
}

func scenarioPagination() error {
	cl := newClient()
	defer cl.close()
	id := uploadBasic(cl)
	// limit=2 offset=0 -> a.txt, b.go; total=4.
	code, body := cl.get("/archives/" + id + "/entries?limit=2&offset=0")
	if code != http.StatusOK {
		return fmt.Errorf("pagination p1: code=%d body=%v", code, body)
	}
	if !eqInt(body["total"], 4) || !eqInt(body["offset"], 0) || !eqInt(body["limit"], 2) {
		return fmt.Errorf("pagination p1: total=%v offset=%v limit=%v want 4/0/2", body["total"], body["offset"], body["limit"])
	}
	items, _ := body["items"].([]any)
	if got := names(items); len(got) != 2 || got[0] != "a.txt" || got[1] != "b.go" {
		return fmt.Errorf("pagination p1: %v want [a.txt b.go]", got)
	}
	// offset=2 -> dir/, dir/c.txt.
	code, body = cl.get("/archives/" + id + "/entries?limit=2&offset=2")
	if !eqInt(body["total"], 4) {
		return fmt.Errorf("pagination p2: total=%v want 4", body["total"])
	}
	items, _ = body["items"].([]any)
	if got := names(items); len(got) != 2 || got[0] != "dir/" || got[1] != "dir/c.txt" {
		return fmt.Errorf("pagination p2: %v want [dir/ dir/c.txt]", got)
	}
	// offset beyond end -> empty items, total still 4.
	code, body = cl.get("/archives/" + id + "/entries?limit=2&offset=10")
	if code != http.StatusOK || !eqInt(body["total"], 4) {
		return fmt.Errorf("pagination p3: code=%d total=%v", code, body["total"])
	}
	items, _ = body["items"].([]any)
	if len(items) != 0 {
		return fmt.Errorf("pagination p3: items=%v want empty", items)
	}
	return nil
}

func scenarioDuplicateNames() error {
	cl := newClient()
	defer cl.close()
	entries := []tentry{
		{name: "foo", typeflag: tar.TypeReg, mode: 0644, size: 1},
		{name: "foo", typeflag: tar.TypeReg, mode: 0644, size: 1},
		{name: "bar", typeflag: tar.TypeReg, mode: 0644, size: 1},
	}
	code, body := cl.postArchive("/archives", buildTar(entries))
	if code != http.StatusOK || !eqInt(body["entries"], 3) {
		return fmt.Errorf("dup upload: code=%d body=%v want entries=3", code, body)
	}
	id := body["id"].(string)
	// name=foo returns both foo occurrences (no dedup), in stable order.
	items := searchItems(cl, id, "?name=foo")
	if len(items) != 2 {
		return fmt.Errorf("dup search: len=%d want 2", len(items))
	}
	if entryName(items[0]) != "foo" || entryName(items[1]) != "foo" {
		return fmt.Errorf("dup search: names=%v want [foo foo]", names(items))
	}
	return nil
}

func scenarioSymlink() error {
	cl := newClient()
	defer cl.close()
	entries := []tentry{
		{name: "lnk", typeflag: tar.TypeSymlink, mode: 0777, size: 0, link: "target.txt"},
	}
	code, body := cl.postArchive("/archives", buildTar(entries))
	if code != http.StatusOK {
		return fmt.Errorf("symlink upload: code=%d body=%v", code, body)
	}
	if byTypeField(body, "symlink") != 1 {
		return fmt.Errorf("symlink upload: by_type[symlink]=%d want 1", byTypeField(body, "symlink"))
	}
	id := body["id"].(string)
	items := searchItems(cl, id, "?type=symlink")
	if len(items) != 1 {
		return fmt.Errorf("symlink search: len=%d want 1", len(items))
	}
	if entryField(items[0], "link") != "target.txt" || entryField(items[0], "type") != "symlink" {
		return fmt.Errorf("symlink search: entry=%v want link=target.txt type=symlink", items[0])
	}
	return nil
}

func scenarioEmptyArchive() error {
	cl := newClient()
	defer cl.close()
	// An empty tar stream has no entries.
	code, body := cl.postArchive("/archives", buildTar(nil))
	if code != http.StatusOK {
		return fmt.Errorf("empty upload: code=%d body=%v", code, body)
	}
	if !eqInt(body["entries"], 0) {
		return fmt.Errorf("empty upload: entries=%v want 0", body["entries"])
	}
	id := body["id"].(string)
	items := searchItems(cl, id, "")
	if len(items) != 0 {
		return fmt.Errorf("empty search: items=%v want empty", items)
	}
	return nil
}

func scenarioInvalidGzip() error {
	cl := newClient()
	defer cl.close()
	// Random bytes are not valid gzip.
	code, _ := cl.postArchive("/archives", []byte("not gzip at all, just plain text bytes"))
	if code != http.StatusBadRequest {
		return fmt.Errorf("invalid gzip: code=%d want 400", code)
	}
	return nil
}

func scenarioGzipNotTar() error {
	cl := newClient()
	defer cl.close()
	// gzip-compressed plain text is valid gzip but not a valid tar stream.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write([]byte("hello, this is not a tar stream"))
	gz.Close()
	code, _ := cl.postArchive("/archives", buf.Bytes())
	if code != http.StatusBadRequest {
		return fmt.Errorf("gzip not tar: code=%d want 400", code)
	}
	return nil
}

func scenarioBadPattern() error {
	cl := newClient()
	defer cl.close()
	id := uploadBasic(cl)
	// "[" is an invalid path.Match pattern.
	code, _ := cl.get("/archives/" + id + "/entries?name=[")
	if code != http.StatusBadRequest {
		return fmt.Errorf("bad pattern: code=%d want 400", code)
	}
	return nil
}

func scenarioNotFound() error {
	cl := newClient()
	defer cl.close()
	if code, _ := cl.get("/archives/ar-9999"); code != http.StatusNotFound {
		return fmt.Errorf("get not found: code=%d want 404", code)
	}
	if code, _ := cl.get("/archives/ar-9999/entries"); code != http.StatusNotFound {
		return fmt.Errorf("search not found: code=%d want 404", code)
	}
	if code := cl.del("/archives/ar-9999"); code != http.StatusNotFound {
		return fmt.Errorf("delete not found: code=%d want 404", code)
	}
	return nil
}

func scenarioDelete() error {
	cl := newClient()
	defer cl.close()
	id := uploadBasic(cl)
	if code := cl.del("/archives/" + id); code != http.StatusNoContent {
		return fmt.Errorf("delete: code=%d want 204", code)
	}
	if code, _ := cl.get("/archives/" + id); code != http.StatusNotFound {
		return fmt.Errorf("get after delete: code=%d want 404", code)
	}
	return nil
}
