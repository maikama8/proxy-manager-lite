package geo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStaticLookup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "geo.json")
	body := []byte(`{"192.0.2.10":{"country":"DE","region":"Hesse","city":"Frankfurt"}}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadStatic(path)
	if err != nil {
		t.Fatal(err)
	}
	place, err := s.Lookup(context.Background(), "192.0.2.10")
	if err != nil || place.City != "Frankfurt" || place.Country != "DE" {
		t.Fatalf("%+v %v", place, err)
	}
	place, err = s.Lookup(context.Background(), "198.51.100.9")
	if err != nil || place.Country != "" {
		t.Fatalf("unknown host %+v %v", place, err)
	}
}

func TestHTTPLookup(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		if r.URL.Path != "/v1/192.0.2.10" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"country":"US","region":"Virginia","city":"Ashburn"}`))
	}))
	defer srv.Close()
	h := HTTP{RawURL: srv.URL + "/v1/{host}", Client: srv.Client()}
	place, err := h.Lookup(context.Background(), "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if place.City != "Ashburn" || place.Country != "US" {
		t.Fatalf("%+v", place)
	}
	if gotPath != "/v1/192.0.2.10" {
		t.Fatalf("path %s", gotPath)
	}
	h.RawURL = srv.URL + "/missing/{host}"
	if _, err := h.Lookup(context.Background(), "192.0.2.10"); err == nil {
		t.Fatal("expected status error")
	}
}

func TestNop(t *testing.T) {
	place, err := Nop{}.Lookup(context.Background(), "192.0.2.10")
	if err != nil || place != (Place{}) {
		t.Fatalf("%+v %v", place, err)
	}
}
