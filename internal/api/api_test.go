package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/checker"
	"github.com/maikama8/proxy-manager-lite/internal/config"
	"github.com/maikama8/proxy-manager-lite/internal/fakeproxy"
	"github.com/maikama8/proxy-manager-lite/internal/proxy"
	"github.com/maikama8/proxy-manager-lite/internal/rotate"
)

func TestOverviewMasksPasswordsAndServesDashboard(t *testing.T) {
	h := newHarness(t, "")
	proxy.SeedDemo(h.pool, time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC), h.cfg.Health.Rules())

	res := h.get(t, "/api/overview")
	body := read(t, res)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d %s", res.StatusCode, body)
	}
	if strings.Contains(body, "edge-demo") || strings.Contains(body, "svc-demo") || strings.Contains(body, "ops-demo") {
		t.Fatalf("password leaked: %s", body)
	}
	var doc struct {
		Demo     bool   `json:"demo"`
		Strategy string `json:"strategy"`
		Stats    struct {
			Healthy int `json:"healthy"`
			Dead    int `json:"dead"`
			Unknown int `json:"unknown"`
		} `json:"stats"`
		Proxies []struct {
			Host        string  `json:"host"`
			HasPassword bool    `json:"hasPassword"`
			Username    string  `json:"username"`
			Score       float64 `json:"score"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.Demo || doc.Strategy != "round-robin" || len(doc.Proxies) != 6 {
		t.Fatalf("%+v", doc)
	}
	if doc.Stats.Healthy != 4 || doc.Stats.Dead != 1 || doc.Stats.Unknown != 1 {
		t.Fatalf("stats %+v", doc.Stats)
	}
	foundAuth := false
	for _, px := range doc.Proxies {
		if px.Username == "edge" {
			foundAuth = px.HasPassword
		}
	}
	if !foundAuth {
		t.Fatal("expected the amsterdam row to show auth without the secret")
	}

	page := h.get(t, "/")
	html := read(t, page)
	if page.StatusCode != http.StatusOK || !strings.Contains(html, "Harbor") || !strings.Contains(html, "app.js") {
		t.Fatalf("dashboard %d %s", page.StatusCode, html[:min(80, len(html))])
	}
	js := h.get(t, "/app.js")
	if js.StatusCode != http.StatusOK {
		t.Fatalf("app.js %d", js.StatusCode)
	}
	js.Body.Close()
	proxyClient := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParse(t, h.http.URL))}}
	proxied, err := proxyClient.Get("http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	proxiedBody := read(t, proxied)
	if proxied.StatusCode != http.StatusBadRequest || !strings.Contains(proxiedBody, "management API") {
		t.Fatalf("api port as proxy: %d %s", proxied.StatusCode, proxiedBody)
	}
	css := h.get(t, "/app.css")
	if css.StatusCode != http.StatusOK {
		t.Fatalf("app.css %d", css.StatusCode)
	}
	css.Body.Close()
}

func TestImportStrategyDeleteAndToken(t *testing.T) {
	h := newHarness(t, "local-token")
	res := h.get(t, "/api/health")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", res.StatusCode)
	}
	res.Body.Close()

	req := h.req(t, http.MethodPost, "/api/proxies/import", `{"text":"http://ops:s3cret@192.0.2.10:8080\nbad line\n# c\n"}`)
	req.Header.Set("Authorization", "Bearer local-token")
	res = h.do(req)
	body := read(t, res)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"imported":1`) {
		t.Fatalf("import %d %s", res.StatusCode, body)
	}
	if strings.Contains(body, "s3cret") {
		t.Fatal("import response leaked the password")
	}

	list := h.authed(t, http.MethodGet, "/api/proxies", "")
	listBody := read(t, list)
	if strings.Contains(listBody, "s3cret") || !strings.Contains(listBody, `"hasPassword":true`) {
		t.Fatalf("list %s", listBody)
	}
	var listed struct {
		Proxies []struct {
			ID string `json:"id"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal([]byte(listBody), &listed); err != nil || len(listed.Proxies) != 1 {
		t.Fatalf("decode %v body %s", err, listBody)
	}

	changed := h.authed(t, http.MethodPut, "/api/strategy", `{"strategy":"least-latency"}`)
	if changed.StatusCode != http.StatusOK {
		t.Fatalf("strategy %d %s", changed.StatusCode, read(t, changed))
	}
	changed.Body.Close()
	if h.rot.Name() != rotate.LeastLatency {
		t.Fatal(h.rot.Name())
	}
	bad := h.authed(t, http.MethodPut, "/api/strategy", `{"strategy":"random"}`)
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad strategy %d", bad.StatusCode)
	}
	bad.Body.Close()

	del := h.authed(t, http.MethodDelete, "/api/proxies/"+listed.Proxies[0].ID, "")
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete %d", del.StatusCode)
	}
	del.Body.Close()
	if h.pool.Len() != 0 {
		t.Fatal("still present")
	}
}

func TestForceCheck(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	up := fakeproxy.NewHTTP(t, "", "")
	h := newHarness(t, "")
	parsed, err := proxy.ParseLine(strings.TrimPrefix(up.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	h.pool.Upsert(parsed)
	target, err := url.Parse(origin.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	h.checker = checker.New(checker.Options{
		Pool:        h.pool,
		Rules:       h.cfg.Health.Rules(),
		Timeout:     2 * time.Second,
		Concurrency: 1,
		Log:         h.log,
	}, target)
	h.rebuild()

	id := parsed.ID
	res := h.authed(t, http.MethodPost, "/api/proxies/"+id+"/check", "")
	body := read(t, res)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"status":"healthy"`) {
		t.Fatalf("check %d %s", res.StatusCode, body)
	}

	demo, err := proxy.ParseLine("http://192.0.2.50:8080")
	if err != nil {
		t.Fatal(err)
	}
	demo.Demo = true
	h.pool.Upsert(demo)
	res = h.authed(t, http.MethodPost, "/api/proxies/"+demo.ID+"/check", "")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("demo check %d %s", res.StatusCode, read(t, res))
	}
	res.Body.Close()
}

type harness struct {
	cfg     config.Config
	pool    *proxy.Pool
	rot     *rotate.Holder
	checker *checker.Checker
	log     *slog.Logger
	http    *httptest.Server
}

func newHarness(t *testing.T, token string) *harness {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.Token = token
	cfg.Demo = true
	pool := proxy.NewPool()
	rot, err := rotate.NewHolder(cfg.Rotation.Strategy, cfg.Rotation.StickyMax)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &harness{cfg: cfg, pool: pool, rot: rot, log: log}
	h.checker = checker.New(checker.Options{
		Pool:        pool,
		Rules:       cfg.Health.Rules(),
		Timeout:     time.Second,
		Concurrency: 1,
		Log:         log,
		Probe: func(context.Context, proxy.Proxy) (time.Duration, error) {
			return time.Millisecond, nil
		},
	}, nil)
	h.rebuild()
	t.Cleanup(func() {
		if h.http != nil {
			h.http.Close()
		}
	})
	return h
}

func (h *harness) rebuild() {
	if h.http != nil {
		h.http.Close()
	}
	srv := New(h.cfg, h.pool, h.rot, h.checker, &http.Client{Timeout: 5 * time.Second}, h.log, time.Now(), "0.1.0")
	h.http = httptest.NewServer(srv.Handler())
}

func (h *harness) get(t *testing.T, path string) *http.Response {
	t.Helper()
	res, err := h.http.Client().Get(h.http.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (h *harness) req(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.http.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func (h *harness) do(req *http.Request) *http.Response {
	res, err := h.http.Client().Do(req)
	if err != nil {
		panic(err)
	}
	return res
}

func (h *harness) authed(t *testing.T, method, path, body string) *http.Response {
	t.Helper()
	req := h.req(t, method, path, body)
	if h.cfg.Server.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.Server.Token)
	}
	return h.do(req)
}

func read(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(bytes.TrimSpace(b))
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
