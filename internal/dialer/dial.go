// Package dialer opens a TCP connection through an HTTP, HTTPS, or SOCKS5 proxy.
package dialer

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/proxy"
)

const fallbackTimeout = 15 * time.Second

// Endpoint is the dial information for one upstream proxy.
type Endpoint struct {
	ID       string
	Scheme   string
	Host     string
	Port     int
	Username string
	Password string
}

// FromProxy copies the dial fields off a pool record.
func FromProxy(px proxy.Proxy) Endpoint {
	return Endpoint{
		ID:       px.ID,
		Scheme:   px.Scheme,
		Host:     px.Host,
		Port:     px.Port,
		Username: px.Username,
		Password: px.Password,
	}
}

// Addr is host:port.
func (e Endpoint) Addr() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

// Redacted is scheme://host:port.
func (e Endpoint) Redacted() string {
	return e.Scheme + "://" + e.Addr()
}

// ProxyURL is the URL http.Transport expects for an HTTP or HTTPS proxy.
func (e Endpoint) ProxyURL() *url.URL {
	u := &url.URL{Scheme: e.Scheme, Host: e.Addr()}
	if e.Username != "" || e.Password != "" {
		u.User = url.UserPassword(e.Username, e.Password)
	}
	return u
}

// Dial opens a TCP connection to target (host:port) through the proxy.
// The returned connection has no deadline set. ctx bounds the handshake.
func Dial(ctx context.Context, e Endpoint, target string) (net.Conn, error) {
	if _, _, err := net.SplitHostPort(target); err != nil {
		return nil, fmt.Errorf("target must be host:port: %w", err)
	}
	ctx, cancel := bound(ctx)
	defer cancel()
	var (
		conn net.Conn
		err  error
	)
	switch e.Scheme {
	case proxy.SchemeSOCKS5:
		conn, err = dialSOCKS5(ctx, e, target)
	case proxy.SchemeHTTP, proxy.SchemeHTTPS:
		conn, err = dialHTTPConnect(ctx, e, target)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", e.Scheme)
	}
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, fallbackTimeout)
}

func dialRaw(ctx context.Context, e Endpoint) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", e.Addr())
	if err != nil {
		return nil, fmt.Errorf("dial proxy: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if e.Scheme != proxy.SchemeHTTPS {
		return conn, nil
	}
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: e.Host,
		MinVersion: tls.VersionTLS12,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tls to proxy: %w", err)
	}
	return tlsConn, nil
}

func dialHTTPConnect(ctx context.Context, e Endpoint, target string) (net.Conn, error) {
	conn, err := dialRaw(ctx, e)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if e.Username != "" || e.Password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(e.Username + ":" + e.Password))
		fmt.Fprintf(&b, "Proxy-Authorization: Basic %s\r\n", token)
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write connect: %w", err)
	}
	br := bufio.NewReader(conn)
	code, err := readStatus(br)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if code != 200 {
		conn.Close()
		return nil, fmt.Errorf("connect status %d", code)
	}
	return &prefixConn{Conn: conn, r: br}, nil
}

func readStatus(r *bufio.Reader) (int, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("read status: %w", err)
	}
	parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
		return 0, errors.New("bad status line")
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, errors.New("bad status code")
	}
	for i := 0; i < 128; i++ {
		h, err := r.ReadString('\n')
		if err != nil {
			return 0, fmt.Errorf("read headers: %w", err)
		}
		if h == "\r\n" || h == "\n" {
			return code, nil
		}
	}
	return 0, errors.New("headers too long")
}

type prefixConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *prefixConn) Read(b []byte) (int, error) {
	return p.r.Read(b)
}
