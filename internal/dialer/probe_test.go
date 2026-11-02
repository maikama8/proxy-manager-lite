package dialer

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/fakeproxy"
	"github.com/maikama8/proxy-manager-lite/internal/proxy"
)

func TestProbeHTTPAndSOCKS(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	target, err := url.Parse(origin.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}

	httpProxy := fakeproxy.NewHTTP(t, "ops", "secret")
	ep := endpointFrom(t, httpProxy.URL, proxy.SchemeHTTP, "ops", "secret")
	lat, err := Probe(context.Background(), ep, target)
	if err != nil {
		t.Fatal(err)
	}
	if lat <= 0 || lat > 5*time.Second {
		t.Fatalf("latency %s", lat)
	}

	ep.Password = "nope"
	if _, err := Probe(context.Background(), ep, target); err == nil || !strings.Contains(err.Error(), "407") {
		t.Fatalf("auth error %v", err)
	}

	socks := fakeproxy.NewSOCKS(t, "svc", "secret")
	host, portStr, err := net.SplitHostPort(socks.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	sep := Endpoint{Scheme: proxy.SchemeSOCKS5, Host: host, Port: port, Username: "svc", Password: "secret"}
	if _, err := Probe(context.Background(), sep, target); err != nil {
		t.Fatal(err)
	}
	if socks.Hits.Load() != 1 {
		t.Fatalf("socks hits %d", socks.Hits.Load())
	}
}

func TestDialRejectsBadTarget(t *testing.T) {
	if _, err := Dial(context.Background(), Endpoint{Scheme: proxy.SchemeHTTP, Host: "127.0.0.1", Port: 1}, "not-a-target"); err == nil {
		t.Fatal("expected error")
	}
}

func endpointFrom(t *testing.T, raw, scheme, user, pass string) Endpoint {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return Endpoint{Scheme: scheme, Host: host, Port: port, Username: user, Password: pass}
}
