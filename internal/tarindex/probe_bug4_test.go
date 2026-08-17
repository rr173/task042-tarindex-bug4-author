package tarindex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"sync"
	"testing"
)

func TestProbeConcurrentCreateAndSearch(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "item.txt", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()

	svc := NewService()
	summary, err := svc.Create(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}

	const readers = 6
	const searchesPerReader = 1000
	const creates = 500
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < creates; i++ {
			if _, err := svc.Create(bytes.NewReader(archive)); err != nil {
				t.Errorf("Create: %v", err)
				return
			}
		}
	}()
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < searchesPerReader; j++ {
				if _, err := svc.Search(summary.ID, Filters{}); err != nil {
					t.Errorf("Search: %v", err)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
}
