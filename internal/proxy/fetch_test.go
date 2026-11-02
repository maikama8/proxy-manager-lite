package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxies.txt")
	body := "# demo\nhttp://192.0.2.10:8080\nhttp://192.0.2.10:8080\nbogus\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewPool()
	res, err := ImportFile(p, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 || res.Updated != 1 || len(res.Errors) != 1 {
		t.Fatalf("%+v", res)
	}
	if _, err := ImportFile(p, filepath.Join(dir, "missing.txt")); err == nil {
		t.Fatal("expected missing file error")
	}
}

func TestImportURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmtJSON := `{"proxies":["socks5://198.51.100.20:1080","not valid"]}`
		_, _ = w.Write([]byte(fmtJSON))
	}))
	defer srv.Close()
	p := NewPool()
	res, err := ImportURL(context.Background(), p, srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 || len(res.Errors) != 1 {
		t.Fatalf("%+v", res)
	}
	_, err = ImportURL(context.Background(), p, srv.Client(), "file:///etc/passwd")
	if err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("got %v", err)
	}
}

func TestFetchRejectsLargeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", maxImportBytes+2)))
	}))
	defer srv.Close()
	_, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("got %v", err)
	}
}
