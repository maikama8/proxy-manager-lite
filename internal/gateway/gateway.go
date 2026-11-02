// Package gateway is a local HTTP proxy. It accepts absolute-form requests
// and CONNECT, picks an upstream from the pool, and forwards the bytes.
//
// The listener does not add X-Forwarded-For. The address an origin sees is
// the upstream proxy, which is the point of sending the traffic there.
package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/dialer"
	"github.com/maikama8/proxy-manager-lite/internal/proxy"
	"github.com/maikama8/proxy-manager-lite/internal/redact"
	"github.com/maikama8/proxy-manager-lite/internal/rotate"
)

const proxyHeader = "X-Harbor-Proxy"

// Gateway forwards one request through a selected upstream.
type Gateway struct {
	pool         *proxy.Pool
	rot          *rotate.Holder
	stickyHeader string
	dialTimeout  time.Duration
	log          *slog.Logger

	mu         sync.Mutex
	transports map[string]*http.Transport
}

// New builds a gateway. dialTimeout bounds the handshake to the upstream,
// not the life of a CONNECT tunnel.
func New(pool *proxy.Pool, rot *rotate.Holder, stickyHeader string, dialTimeout time.Duration, log *slog.Logger) *Gateway {
	if stickyHeader == "" {
		stickyHeader = "X-Proxy-Session"
	}
	if dialTimeout <= 0 {
		dialTimeout = 15 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Gateway{
		pool:         pool,
		rot:          rot,
		stickyHeader: stickyHeader,
		dialTimeout:  dialTimeout,
		log:          log,
		transports:   map[string]*http.Transport{},
	}
}

// Close drops idle connections to upstream proxies.
func (g *Gateway) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, tr := range g.transports {
		tr.CloseIdleConnections()
	}
}

// ServeHTTP implements http.Handler.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		g.handleConnect(w, r)
		return
	}
	g.handleHTTP(w, r)
}

func (g *Gateway) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || (r.URL.Scheme != "http" && r.URL.Scheme != "https") || r.URL.Host == "" {
		http.Error(w, "absolute http or https URI required", http.StatusBadRequest)
		return
	}
	px, ok := g.pick(r)
	if !ok {
		http.Error(w, "no healthy proxy", http.StatusBadGateway)
		return
	}
	w.Header().Set(proxyHeader, px.ID)
	start := time.Now()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header = r.Header.Clone()
	stripHop(out.Header)
	out.Header.Del(proxyHeader)
	out.Header.Set("Via", appendVia(out.Header.Get("Via")))

	resp, err := g.transport(px).RoundTrip(out)
	if err != nil {
		g.pool.Note(px.ID, false)
		g.log.Warn("forward", "method", r.Method, "host", r.URL.Hostname(), "proxy", px.Redacted(), "err", redact.Error(err, px.Password), "ms", time.Since(start).Milliseconds())
		http.Error(w, "upstream proxy failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusProxyAuthRequired {
		g.pool.Note(px.ID, false)
		g.log.Warn("forward", "method", r.Method, "host", r.URL.Hostname(), "proxy", px.Redacted(), "err", "status 407")
		http.Error(w, "upstream proxy failed", http.StatusBadGateway)
		return
	}
	stripHop(resp.Header)
	copyHeader(w.Header(), resp.Header)
	w.Header().Set(proxyHeader, px.ID)
	w.Header().Set("Via", appendVia(w.Header().Get("Via")))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	g.pool.Note(px.ID, true)
	g.log.Info("forward", "method", r.Method, "host", r.URL.Hostname(), "proxy", px.Redacted(), "status", resp.StatusCode, "ms", time.Since(start).Milliseconds())
}

func (g *Gateway) handleConnect(w http.ResponseWriter, r *http.Request) {
	if _, _, err := net.SplitHostPort(r.Host); err != nil {
		http.Error(w, "connect target must be host:port", http.StatusBadRequest)
		return
	}
	px, ok := g.pick(r)
	if !ok {
		http.Error(w, "no healthy proxy", http.StatusBadGateway)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), g.dialTimeout)
	upstream, err := dialer.Dial(ctx, dialer.FromProxy(px), r.Host)
	cancel()
	if err != nil {
		g.pool.Note(px.ID, false)
		g.log.Warn("connect", "host", r.Host, "proxy", px.Redacted(), "err", redact.Error(err, px.Password))
		http.Error(w, "upstream proxy failed", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "tunnel is not supported", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		g.pool.Note(px.ID, false)
		return
	}
	if _, err := fmt.Fprintf(client, "HTTP/1.1 200 Connection Established\r\n%s: %s\r\n\r\n", proxyHeader, px.ID); err != nil {
		client.Close()
		upstream.Close()
		g.pool.Note(px.ID, false)
		return
	}
	if buf != nil && buf.Reader != nil {
		if n := buf.Reader.Buffered(); n > 0 {
			if peeked, perr := buf.Reader.Peek(n); perr == nil {
				_, _ = upstream.Write(peeked)
			}
		}
	}
	g.pool.Note(px.ID, true)
	g.log.Info("connect", "host", r.Host, "proxy", px.Redacted())
	splice(client, upstream)
}

func (g *Gateway) pick(r *http.Request) (proxy.Proxy, bool) {
	list := g.pool.List()
	cands := make([]rotate.Candidate, 0, len(list))
	for _, px := range list {
		cands = append(cands, rotate.Candidate{
			ID:       px.ID,
			Latency:  px.State.Latency.Milliseconds(),
			Score:    px.State.Score,
			Eligible: px.InRotation(),
			Measured: px.State.Measured,
		})
	}
	id, ok := g.rot.Pick(cands, sessionKey(r, g.stickyHeader))
	if !ok {
		return proxy.Proxy{}, false
	}
	return g.pool.Get(id)
}

func (g *Gateway) transport(px proxy.Proxy) *http.Transport {
	ep := dialer.FromProxy(px)
	sum := sha256.Sum256([]byte(ep.Username + "\n" + ep.Password))
	key := ep.ID + ":" + hex.EncodeToString(sum[:8])
	g.mu.Lock()
	defer g.mu.Unlock()
	if tr, ok := g.transports[key]; ok {
		return tr
	}
	tr := &http.Transport{
		DisableCompression:    true,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: time.Second,
		MaxIdleConnsPerHost:   4,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if ep.Scheme == proxy.SchemeSOCKS5 {
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if network != "tcp" && network != "tcp4" && network != "tcp6" {
				return nil, fmt.Errorf("unsupported network %s", network)
			}
			return dialer.Dial(ctx, ep, addr)
		}
	} else {
		tr.Proxy = http.ProxyURL(ep.ProxyURL())
	}
	g.transports[key] = tr
	return tr
}

func sessionKey(r *http.Request, header string) string {
	if header != "" {
		if v := strings.TrimSpace(r.Header.Get(header)); v != "" {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func splice(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(b, a)
		closeWrite(b)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(a, b)
		closeWrite(a)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

var hopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func stripHop(h http.Header) {
	var extra []string
	for _, item := range h.Values("Connection") {
		for _, name := range strings.Split(item, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				extra = append(extra, name)
			}
		}
	}
	for _, name := range hopHeaders {
		h.Del(name)
	}
	for _, name := range extra {
		h.Del(name)
	}
}

func copyHeader(dst, src http.Header) {
	for k, vals := range src {
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func appendVia(existing string) string {
	const via = "1.1 harbor"
	if existing == "" {
		return via
	}
	if len(existing) > 400 {
		return via
	}
	return existing + ", " + via
}
