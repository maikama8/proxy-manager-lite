package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/config"
	"github.com/maikama8/proxy-manager-lite/internal/health"
)

func TestLoadDemoAndFile(t *testing.T) {
	cfg := config.Defaults()
	cfg.Demo = true
	dir := t.TempDir()
	path := filepath.Join(dir, "proxies.txt")
	if err := os.WriteFile(path, []byte("http://192.0.2.10:8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Pool.File = path
	a, err := New(cfg, Logger("error"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The file replaces the matching demo row, so the pool stays at 6 and
	// 192.0.2.10 is no longer a demo.
	if a.Len() != 6 {
		t.Fatalf("len %d", a.Len())
	}
	var found bool
	for _, px := range a.pool.List() {
		if px.Host == "192.0.2.10" && px.Port == 8080 {
			found = !px.Demo && px.State.Status == health.StatusUnknown
		}
	}
	if !found {
		t.Fatal("file import did not replace the demo row")
	}
}

func TestLoadURLFailure(t *testing.T) {
	cfg := config.Defaults()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg.Pool.URL = srv.URL
	a, err := New(cfg, Logger("error"), "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.Load(ctx); err == nil {
		t.Fatal("expected import error")
	}
}

func TestMissingFile(t *testing.T) {
	cfg := config.Defaults()
	cfg.Pool.File = filepath.Join(t.TempDir(), "missing.txt")
	a, err := New(cfg, Logger("error"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Load(context.Background()); err == nil {
		t.Fatal("expected missing file")
	}
}
