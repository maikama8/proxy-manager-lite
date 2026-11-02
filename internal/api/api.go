// Package api is the management HTTP API and the dashboard mount.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/checker"
	"github.com/maikama8/proxy-manager-lite/internal/config"
	"github.com/maikama8/proxy-manager-lite/internal/dashboard"
	"github.com/maikama8/proxy-manager-lite/internal/proxy"
	"github.com/maikama8/proxy-manager-lite/internal/rotate"
)

// Server serves /api and the dashboard.
type Server struct {
	cfg     config.Config
	pool    *proxy.Pool
	rot     *rotate.Holder
	checker *checker.Checker
	client  *http.Client
	log     *slog.Logger
	started time.Time
	version string
}

// New returns a management server. client is used for URL imports.
func New(cfg config.Config, pool *proxy.Pool, rot *rotate.Holder, chk *checker.Checker, client *http.Client, log *slog.Logger, started time.Time, version string) *Server {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if log == nil {
		log = slog.Default()
	}
	if version == "" {
		version = "dev"
	}
	return &Server{
		cfg:     cfg,
		pool:    pool,
		rot:     rot,
		checker: chk,
		client:  client,
		log:     log,
		started: started.UTC(),
		version: version,
	}
}

// Handler is the dashboard and the API. API routes require the bearer token
// when server.token is set. The HTML does not.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/overview", s.overview)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/proxies", s.list)
	mux.HandleFunc("POST /api/proxies", s.add)
	mux.HandleFunc("POST /api/proxies/import", s.importBatch)
	mux.HandleFunc("DELETE /api/proxies/{id}", s.remove)
	mux.HandleFunc("POST /api/proxies/{id}/check", s.check)
	mux.HandleFunc("PUT /api/strategy", s.setStrategy)
	mux.Handle("/", dashboard.Handler())
	return secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect || (r.URL != nil && r.URL.IsAbs()) {
			http.Error(w, "this listener is the management API; point proxy clients at "+s.cfg.Server.Gateway, http.StatusBadRequest)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="harbor"`)
			writeJSON(w, http.StatusUnauthorized, errBody{Error: "unauthorized"})
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	stats := s.pool.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"version":    s.version,
		"strategy":   s.rot.Name(),
		"proxies":    stats.Total,
		"healthy":    stats.Healthy,
		"dead":       stats.Dead,
		"unknown":    stats.Unknown,
		"inRotation": stats.InRotation,
		"demo":       s.cfg.Demo,
	})
}

func (s *Server) overview(w http.ResponseWriter, _ *http.Request) {
	stats := s.pool.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":   s.version,
		"demo":      s.cfg.Demo,
		"strategy":  s.rot.Name(),
		"listen":    s.cfg.Server.Listen,
		"gateway":   s.cfg.Server.Gateway,
		"startedAt": s.started.Format(time.RFC3339),
		"stats":     stats,
		"proxies":   s.pool.Views(),
		"settings": map[string]any{
			"healthTarget": s.cfg.Health.Target,
			"interval":     s.cfg.Health.Interval.String(),
			"evictAfter":   s.cfg.Health.EvictAfter,
			"reviveAfter":  s.cfg.Health.ReviveAfter,
			"budgetMs":     s.cfg.Health.LatencyBudget.Milliseconds(),
			"history":      s.cfg.Health.History,
			"stickyHeader": s.cfg.Rotation.StickyHeader,
		},
	})
}

func (s *Server) stats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.pool.Stats())
}

func (s *Server) list(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"proxies": s.pool.Views()})
}

func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Proxy string `json:"proxy"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Proxy) == "" {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "proxy is required"})
		return
	}
	res := s.pool.Import(req.Proxy)
	if res.Imported == 0 && res.Updated == 0 {
		msg := "invalid proxy"
		if len(res.Errors) > 0 {
			msg = res.Errors[0].Message
		}
		writeJSON(w, http.StatusBadRequest, errBody{Error: msg})
		return
	}
	s.log.Info("proxy added", "imported", res.Imported, "updated", res.Updated)
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) importBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
		URL  string `json:"url"`
	}
	if !decode(w, r, &req) {
		return
	}
	text := strings.TrimSpace(req.Text)
	rawURL := strings.TrimSpace(req.URL)
	if text == "" && rawURL == "" {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "text or url is required"})
		return
	}
	if text != "" && rawURL != "" {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "send text or url, not both"})
		return
	}
	if rawURL != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		res, err := proxy.ImportURL(ctx, s.pool, s.client, rawURL)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, errBody{Error: "could not fetch the proxy list"})
			return
		}
		s.log.Info("proxies imported", "url", rawURL, "imported", res.Imported, "updated", res.Updated, "errors", len(res.Errors))
		writeJSON(w, http.StatusOK, res)
		return
	}
	res := s.pool.Import(text)
	s.log.Info("proxies imported", "imported", res.Imported, "updated", res.Updated, "errors", len(res.Errors))
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.pool.Remove(id) {
		writeJSON(w, http.StatusNotFound, errBody{Error: "proxy not found"})
		return
	}
	s.log.Info("proxy removed", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.checker == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody{Error: "checker is not running"})
		return
	}
	px, err := s.checker.Check(r.Context(), id)
	if errors.Is(err, checker.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody{Error: "proxy not found"})
		return
	}
	if errors.Is(err, checker.ErrDemo) {
		writeJSON(w, http.StatusBadRequest, errBody{Error: err.Error()})
		return
	}
	view := proxyView(s.pool, px.ID)
	if view.ID == "" {
		writeJSON(w, http.StatusNotFound, errBody{Error: "proxy not found"})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) setStrategy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Strategy string `json:"strategy"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Strategy))
	if err := s.rot.Set(name, s.cfg.Rotation.StickyMax); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "strategy must be round-robin, least-latency, or sticky"})
		return
	}
	s.log.Info("strategy", "name", name)
	writeJSON(w, http.StatusOK, map[string]string{"strategy": s.rot.Name()})
}

func (s *Server) authorized(r *http.Request) bool {
	token := s.cfg.Server.Token
	if token == "" {
		return true
	}
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	got := header[len(prefix):]
	if len(got) != len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func proxyView(pool *proxy.Pool, id string) proxy.View {
	for _, view := range pool.Views() {
		if view.ID == id {
			return view
		}
	}
	return proxy.View{}
}

type errBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		status := http.StatusBadRequest
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, errBody{Error: "invalid JSON"})
		return false
	}
	return true
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
