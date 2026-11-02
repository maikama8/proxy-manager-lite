// Package fakeproxy is an in-process HTTP and SOCKS5 proxy for tests.
package fakeproxy

import (
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// HTTP is an HTTP proxy that forwards absolute-form requests and CONNECT.
type HTTP struct {
	URL      string
	Hits     atomic.Int64
	LastUser string
	server   *httptest.Server
	mu       sync.Mutex
}

// NewHTTP starts a proxy. user and pass are required when non-empty.
func NewHTTP(t *testing.T, user, pass string) *HTTP {
	t.Helper()
	h := &HTTP{}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, ok := proxyBasic(r)
		h.mu.Lock()
		h.LastUser = gotUser
		h.mu.Unlock()
		if (user != "" || pass != "") && (!ok || gotUser != user || gotPass != pass) {
			w.Header().Set("Proxy-Authenticate", `Basic realm="fake"`)
			http.Error(w, "proxy auth required", http.StatusProxyAuthRequired)
			return
		}
		h.Hits.Add(1)
		if r.Method == http.MethodConnect {
			connectHTTP(w, r)
			return
		}
		forwardHTTP(w, r)
	}))
	h.URL = h.server.URL
	t.Cleanup(h.server.Close)
	return h
}

// User is the username presented on the most recent request.
func (h *HTTP) User() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.LastUser
}

func proxyBasic(r *http.Request) (string, string, bool) {
	header := r.Header.Get("Proxy-Authorization")
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return "", "", false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	return user, pass, ok
}

func forwardHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || r.URL.Scheme == "" || r.URL.Host == "" {
		http.Error(w, "absolute uri required", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header = r.Header.Clone()
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Connection")
	resp, err := cleanTransport.RoundTrip(out)
	if err != nil {
		http.Error(w, "origin failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

var cleanTransport = &http.Transport{Proxy: nil, DisableKeepAlives: true}

func connectHTTP(w http.ResponseWriter, r *http.Request) {
	dest, err := net.DialTimeout("tcp", r.Host, 3*time.Second)
	if err != nil {
		http.Error(w, "dial failed", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		dest.Close()
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		dest.Close()
		return
	}
	_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
	if buf != nil && buf.Reader != nil {
		if n := buf.Reader.Buffered(); n > 0 {
			if peeked, err := buf.Reader.Peek(n); err == nil {
				_, _ = dest.Write(peeked)
			}
		}
	}
	splice(client, dest)
}

// SOCKS is a SOCKS5 proxy with optional username/password auth.
type SOCKS struct {
	Addr string
	Hits atomic.Int64
	ln   net.Listener
}

// NewSOCKS starts a SOCKS5 server.
func NewSOCKS(t *testing.T, user, pass string) *SOCKS {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &SOCKS{Addr: ln.Addr().String(), ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, user, pass)
		}
	}()
	return s
}

func (s *SOCKS) serve(conn net.Conn, user, pass string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil || header[0] != 0x05 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	needAuth := user != "" || pass != ""
	method := byte(0x00)
	if needAuth {
		method = 0x02
	}
	if !bytesContain(methods, method) {
		_, _ = conn.Write([]byte{0x05, 0xff})
		return
	}
	if _, err := conn.Write([]byte{0x05, method}); err != nil {
		return
	}
	if needAuth {
		ver := make([]byte, 1)
		if _, err := io.ReadFull(conn, ver); err != nil {
			return
		}
		ulen := make([]byte, 1)
		if _, err := io.ReadFull(conn, ulen); err != nil {
			return
		}
		ub := make([]byte, int(ulen[0]))
		if _, err := io.ReadFull(conn, ub); err != nil {
			return
		}
		plen := make([]byte, 1)
		if _, err := io.ReadFull(conn, plen); err != nil {
			return
		}
		pb := make([]byte, int(plen[0]))
		if _, err := io.ReadFull(conn, pb); err != nil {
			return
		}
		if string(ub) != user || string(pb) != pass {
			_, _ = conn.Write([]byte{0x01, 0x01})
			return
		}
		if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
			return
		}
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil || req[1] != 0x01 {
		return
	}
	host, err := readSocksHost(conn, req[3])
	if err != nil {
		return
	}
	portb := make([]byte, 2)
	if _, err := io.ReadFull(conn, portb); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(portb)
	dest, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))), 3*time.Second)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	s.Hits.Add(1)
	_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	_ = conn.SetDeadline(time.Time{})
	_ = dest.SetDeadline(time.Time{})
	splice(conn, dest)
}

func readSocksHost(r io.Reader, atyp byte) (string, error) {
	switch atyp {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case 0x03:
		n := make([]byte, 1)
		if _, err := io.ReadFull(r, n); err != nil {
			return "", err
		}
		b := make([]byte, int(n[0]))
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return string(b), nil
	default:
		return "", io.ErrUnexpectedEOF
	}
}

func bytesContain(b []byte, v byte) bool {
	for _, x := range b {
		if x == v {
			return true
		}
	}
	return false
}

func splice(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		closeWrite(a)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		closeWrite(b)
	}()
	wg.Wait()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
