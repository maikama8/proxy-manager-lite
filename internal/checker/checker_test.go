package checker

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/fakeproxy"
	"github.com/maikama8/proxy-manager-lite/internal/geo"
	"github.com/maikama8/proxy-manager-lite/internal/health"
	"github.com/maikama8/proxy-manager-lite/internal/proxy"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	fresh := proxy.Proxy{}
	if !Due(fresh, now, time.Second, time.Minute) {
		t.Fatal("never checked should be due")
	}
	fresh.Demo = true
	if Due(fresh, now, time.Second, time.Minute) {
		t.Fatal("demo should not be due")
	}
	healthy := proxy.Proxy{State: health.State{Status: health.StatusHealthy, LastChecked: now.Add(-2 * time.Second)}}
	if !Due(healthy, now, time.Second, time.Minute) {
		t.Fatal("healthy past interval")
	}
	if Due(healthy, now, 5*time.Second, time.Minute) {
		t.Fatal("healthy inside interval")
	}
	dead := proxy.Proxy{State: health.State{Status: health.StatusDead, LastChecked: now.Add(-2 * time.Second)}}
	if Due(dead, now, time.Second, 30*time.Second) {
		t.Fatal("dead should wait for the recheck interval")
	}
	dead.State.LastChecked = now.Add(-time.Minute)
	if !Due(dead, now, time.Second, 30*time.Second) {
		t.Fatal("dead past recheck")
	}
}

func TestPassLimitsConcurrency(t *testing.T) {
	pool := proxy.NewPool()
	for i := 0; i < 3; i++ {
		px, err := proxy.ParseLine("http://192.0.2." + strconv.Itoa(10+i) + ":8080")
		if err != nil {
			t.Fatal(err)
		}
		pool.Upsert(px)
	}
	var inflight atomic.Int32
	var max atomic.Int32
	c := New(Options{
		Pool:        pool,
		Concurrency: 2,
		Interval:    time.Second,
		Recheck:     time.Minute,
		Timeout:     time.Second,
		Rules:       health.Rules{History: 5, EvictAfter: 3, ReviveAfter: 1, Budget: time.Second},
		Log:         discard(),
		Probe: func(ctx context.Context, px proxy.Proxy) (time.Duration, error) {
			n := inflight.Add(1)
			for {
				old := max.Load()
				if n <= old || max.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(60 * time.Millisecond)
			inflight.Add(-1)
			return 15 * time.Millisecond, nil
		},
	}, nil)
	c.Pass(context.Background())
	if max.Load() < 2 || max.Load() > 2 {
		t.Fatalf("max inflight %d", max.Load())
	}
	for _, px := range pool.List() {
		if px.State.Status != health.StatusHealthy || !px.State.Measured {
			t.Fatalf("%s status %s", px.Host, px.State.Status)
		}
	}
}

func TestCheckEvictsAndRevives(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	up := fakeproxy.NewHTTP(t, "ops", "secret")
	px := proxyFromURL(t, up.URL, "ops", "secret")
	pool := proxy.NewPool()
	pool.Upsert(px)

	target, err := url.Parse(origin.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	rules := health.Rules{History: 10, EvictAfter: 2, ReviveAfter: 1, Budget: time.Second}
	c := New(Options{
		Pool:        pool,
		Concurrency: 1,
		Interval:    time.Second,
		Recheck:     time.Second,
		Timeout:     2 * time.Second,
		Rules:       rules,
		Log:         discard(),
		Geo:         geoMap{"127.0.0.1": {Country: "DE", City: "Local"}},
	}, target)

	got, err := c.Check(context.Background(), px.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Status != health.StatusHealthy || got.Place.Country != "DE" {
		t.Fatalf("after success %+v place %+v", got.State, got.Place)
	}
	if up.User() != "ops" {
		t.Fatalf("upstream saw user %q", up.User())
	}

	origin.Close()
	if _, err := c.Check(context.Background(), px.ID); err == nil {
		t.Fatal("expected failure")
	}
	got, _ = pool.Get(px.ID)
	if got.State.Status == health.StatusDead {
		t.Fatal("evicted after one failure")
	}
	if _, err := c.Check(context.Background(), px.ID); err == nil {
		t.Fatal("expected second failure")
	}
	got, _ = pool.Get(px.ID)
	if got.State.Status != health.StatusDead || got.InRotation() {
		t.Fatalf("status %s rotation %v err %q", got.State.Status, got.InRotation(), got.State.LastError)
	}
	if got.State.LastError == "" || strings.Contains(got.State.LastError, "secret") {
		t.Fatalf("last error %q", got.State.LastError)
	}
}

func TestCheckRejectsDemo(t *testing.T) {
	pool := proxy.NewPool()
	px, err := proxy.ParseLine("http://192.0.2.10:8080")
	if err != nil {
		t.Fatal(err)
	}
	px.Demo = true
	pool.Upsert(px)
	c := New(Options{Pool: pool, Log: discard(), Rules: health.Rules{History: 5, EvictAfter: 1, ReviveAfter: 1, Budget: time.Second}}, nil)
	if _, err := c.Check(context.Background(), px.ID); err == nil {
		t.Fatal("expected demo error")
	}
}

type geoMap map[string]geo.Place

func (m geoMap) Lookup(_ context.Context, host string) (geo.Place, error) {
	return m[host], nil
}

func proxyFromURL(t *testing.T, raw, user, pass string) proxy.Proxy {
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
	return proxy.Proxy{
		ID:       proxy.ID(proxy.SchemeHTTP, user, host, port),
		Scheme:   proxy.SchemeHTTP,
		Host:     host,
		Port:     port,
		Username: user,
		Password: pass,
	}
}
