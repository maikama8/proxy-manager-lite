package gateway

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/fakeproxy"
	"github.com/maikama8/proxy-manager-lite/internal/health"
	"github.com/maikama8/proxy-manager-lite/internal/proxy"
	"github.com/maikama8/proxy-manager-lite/internal/rotate"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHTTPUpstreamForwards(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			if string(body) != "ping" {
				http.Error(w, "body", http.StatusBadRequest)
				return
			}
		}
		if r.Header.Get("Proxy-Authorization") != "" {
			http.Error(w, "proxy header leaked", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("pong"))
	}))
	defer origin.Close()

	up := fakeproxy.NewHTTP(t, "ops", "secret")
	pool := proxy.NewPool()
	px := mustProxy(t, up.URL, "ops", "secret", 40*time.Millisecond)
	pool.Upsert(px)
	gw := serve(t, pool, rotate.RoundRobin)

	res, err := gw.Client().Post(origin.URL+"/v1", "text/plain", strings.NewReader("ping"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || string(body) != "pong" {
		t.Fatalf("status %d body %s", res.StatusCode, body)
	}
	if res.Header.Get("X-Harbor-Proxy") != px.ID {
		t.Fatalf("proxy header %q", res.Header.Get("X-Harbor-Proxy"))
	}
	if up.Hits.Load() != 1 || up.User() != "ops" {
		t.Fatalf("hits %d user %s", up.Hits.Load(), up.User())
	}
	got, _ := pool.Get(px.ID)
	if got.Served != 1 || got.Errors != 0 {
		t.Fatalf("served %d errors %d", got.Served, got.Errors)
	}
}

func TestUpstreamAuthFailure(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("nope"))
	}))
	defer origin.Close()
	up := fakeproxy.NewHTTP(t, "ops", "secret")
	pool := proxy.NewPool()
	pool.Upsert(mustProxy(t, up.URL, "ops", "wrong", time.Millisecond))
	gw := serve(t, pool, rotate.RoundRobin)
	res, err := gw.Client().Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "upstream proxy failed") {
		t.Fatalf("status %d body %s", res.StatusCode, body)
	}
	if up.Hits.Load() != 0 {
		t.Fatal("auth failure was forwarded")
	}
}

func TestSOCKS5Upstream(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Path)
	}))
	defer origin.Close()
	up := fakeproxy.NewSOCKS(t, "svc", "secret")
	pool := proxy.NewPool()
	px := proxy.Proxy{
		Scheme:   proxy.SchemeSOCKS5,
		Host:     "127.0.0.1",
		Username: "svc",
		Password: "secret",
		State:    measured(30 * time.Millisecond),
	}
	host, portStr, err := net.SplitHostPort(up.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	px.Host = host
	px.Port = port
	px.ID = proxy.ID(px.Scheme, px.Username, px.Host, px.Port)
	pool.Upsert(px)
	gw := serve(t, pool, rotate.RoundRobin)
	res, err := gw.Client().Get(origin.URL + "/socks")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != "/socks" {
		t.Fatalf("status %d body %s", res.StatusCode, body)
	}
	if up.Hits.Load() != 1 {
		t.Fatalf("hits %d", up.Hits.Load())
	}
}

func TestCONNECT(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "tunneled")
	}))
	defer origin.Close()
	up := fakeproxy.NewHTTP(t, "", "")
	pool := proxy.NewPool()
	pool.Upsert(mustProxy(t, up.URL, "", "", time.Millisecond))
	gw := serve(t, pool, rotate.RoundRobin)
	client := gw.Client()
	client.Transport.(*http.Transport).TLSClientConfig = origin.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != "tunneled" {
		t.Fatalf("status %d body %s", res.StatusCode, body)
	}
}

func TestRoundRobin(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	a := fakeproxy.NewHTTP(t, "", "")
	b := fakeproxy.NewHTTP(t, "", "")
	pool := proxy.NewPool()
	pool.Upsert(mustProxy(t, a.URL, "", "", time.Millisecond))
	pool.Upsert(mustProxy(t, b.URL, "", "", time.Millisecond))
	gw := serve(t, pool, rotate.RoundRobin)
	client := gw.Client()
	for i := 0; i < 4; i++ {
		res, err := client.Get(origin.URL)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("status %d", res.StatusCode)
		}
	}
	if a.Hits.Load() != 2 || b.Hits.Load() != 2 {
		t.Fatalf("hits a=%d b=%d", a.Hits.Load(), b.Hits.Load())
	}
}

func TestLeastLatency(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	fast := fakeproxy.NewHTTP(t, "", "")
	slow := fakeproxy.NewHTTP(t, "", "")
	pool := proxy.NewPool()
	pool.Upsert(mustProxy(t, fast.URL, "", "", 20*time.Millisecond))
	pool.Upsert(mustProxy(t, slow.URL, "", "", 800*time.Millisecond))
	gw := serve(t, pool, rotate.LeastLatency)
	client := gw.Client()
	for i := 0; i < 3; i++ {
		res, err := client.Get(origin.URL)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	if fast.Hits.Load() != 3 || slow.Hits.Load() != 0 {
		t.Fatalf("fast %d slow %d", fast.Hits.Load(), slow.Hits.Load())
	}
}

func TestSticky(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	a := fakeproxy.NewHTTP(t, "", "")
	b := fakeproxy.NewHTTP(t, "", "")
	pool := proxy.NewPool()
	pa := mustProxy(t, a.URL, "", "", 100*time.Millisecond)
	pb := mustProxy(t, b.URL, "", "", 100*time.Millisecond)
	pool.Upsert(pa)
	pool.Upsert(pb)
	gw := serve(t, pool, rotate.Sticky)
	seen := map[string]string{}
	var keys []string
	for i := 0; i < 30; i++ {
		key := "session-" + strconv.Itoa(i)
		id := gw.get(t, origin.URL, key)
		if prev, ok := seen[key]; ok && prev != id {
			t.Fatalf("key %s moved", key)
		}
		seen[key] = id
		if len(keys) == 0 || seen[keys[0]] != id {
			keys = append(keys, key)
		}
		if len(distinct(seen)) == 2 {
			break
		}
	}
	if len(distinct(seen)) != 2 {
		t.Fatal("sticky keys did not cover both proxies")
	}
	for _, key := range keys[:2] {
		for n := 0; n < 3; n++ {
			if id := gw.get(t, origin.URL, key); id != seen[key] {
				t.Fatalf("key %s moved from %s to %s", key, seen[key], id)
			}
		}
	}
}

func TestDeadProxyIsSkipped(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	dead := fakeproxy.NewHTTP(t, "", "")
	live := fakeproxy.NewHTTP(t, "", "")
	pool := proxy.NewPool()
	d := mustProxy(t, dead.URL, "", "", time.Millisecond)
	d.State.Status = health.StatusDead
	pool.Upsert(d)
	pool.Upsert(mustProxy(t, live.URL, "", "", 50*time.Millisecond))
	gw := serve(t, pool, rotate.RoundRobin)
	for i := 0; i < 3; i++ {
		res, err := gw.Client().Get(origin.URL)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	if dead.Hits.Load() != 0 || live.Hits.Load() != 3 {
		t.Fatalf("dead %d live %d", dead.Hits.Load(), live.Hits.Load())
	}
}

func TestNoHealthyProxy(t *testing.T) {
	pool := proxy.NewPool()
	gw := serve(t, pool, rotate.RoundRobin)
	res, err := gw.Client().Get("http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "no healthy proxy") {
		t.Fatalf("status %d body %s", res.StatusCode, body)
	}
}

func TestDemoProxyIsNotUsed(t *testing.T) {
	pool := proxy.NewPool()
	px, err := proxy.ParseLine("http://192.0.2.10:8080")
	if err != nil {
		t.Fatal(err)
	}
	px.Demo = true
	px.State.Status = health.StatusHealthy
	px.State.Measured = true
	pool.Upsert(px)
	gw := serve(t, pool, rotate.RoundRobin)
	res, err := gw.Client().Get("http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d", res.StatusCode)
	}
}

type harness struct {
	url string
}

func serve(t *testing.T, pool *proxy.Pool, strategy string) *harness {
	t.Helper()
	rot, err := rotate.NewHolder(strategy, 1000)
	if err != nil {
		t.Fatal(err)
	}
	gw := New(pool, rot, "X-Proxy-Session", 3*time.Second, discard())
	srv := httptest.NewServer(gw)
	t.Cleanup(func() {
		srv.Close()
		gw.Close()
	})
	return &harness{url: srv.URL}
}

func (h *harness) Client() *http.Client {
	u, err := url.Parse(h.url)
	if err != nil {
		panic(err)
	}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(u),
			DisableKeepAlives: true,
		},
	}
}

func (h *harness) get(t *testing.T, rawURL, key string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Proxy-Session", key)
	res, err := h.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", res.StatusCode)
	}
	return res.Header.Get("X-Harbor-Proxy")
}

func mustProxy(t *testing.T, raw, user, pass string, latency time.Duration) proxy.Proxy {
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
		State:    measured(latency),
	}
}

func measured(latency time.Duration) health.State {
	return health.State{
		Status:   health.StatusHealthy,
		Latency:  latency,
		Measured: true,
		Score:    80,
	}
}

func distinct(m map[string]string) map[string]bool {
	out := map[string]bool{}
	for _, id := range m {
		out[id] = true
	}
	return out
}
