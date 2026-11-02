package dialer

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

// Probe fetches target through the proxy and returns the time until the
// status line. Status 407 and any 5xx are failures. Other responses mean the
// proxy reached something, which is what a health check is asking.
func Probe(ctx context.Context, e Endpoint, target *url.URL) (time.Duration, error) {
	if target == nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return 0, fmt.Errorf("health target must be an http or https URL")
	}
	ctx, cancel := bound(ctx)
	defer cancel()
	start := time.Now()
	var err error
	if (e.Scheme == "http" || e.Scheme == "https") && target.Scheme == "http" {
		err = probeAbsolute(ctx, e, target)
	} else {
		err = probeTunnel(ctx, e, target)
	}
	if err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

func probeAbsolute(ctx context.Context, e Endpoint, target *url.URL) error {
	conn, err := dialRaw(ctx, e)
	if err != nil {
		return err
	}
	defer conn.Close()
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n", target.String(), target.Host)
	if e.Username != "" || e.Password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(e.Username + ":" + e.Password))
		fmt.Fprintf(&b, "Proxy-Authorization: Basic %s\r\n", token)
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		return fmt.Errorf("write probe: %w", err)
	}
	code, err := readStatus(bufio.NewReader(conn))
	if err != nil {
		return err
	}
	return classify(code)
}

func probeTunnel(ctx context.Context, e Endpoint, target *url.URL) error {
	conn, err := Dial(ctx, e, hostPort(target))
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if target.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: target.Hostname(),
			MinVersion: tls.VersionTLS12,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("tls to target: %w", err)
		}
		conn = tlsConn
	}
	path := target.RequestURI()
	if path == "" {
		path = "/"
	}
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, target.Host)
	if _, err := io.WriteString(conn, req); err != nil {
		return fmt.Errorf("write probe: %w", err)
	}
	code, err := readStatus(bufio.NewReader(conn))
	if err != nil {
		return err
	}
	return classify(code)
}

func hostPort(u *url.URL) string {
	if _, _, err := net.SplitHostPort(u.Host); err == nil {
		return u.Host
	}
	port := "80"
	if u.Scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func classify(code int) error {
	if code == 407 || code >= 500 || code < 100 {
		return fmt.Errorf("status %d", code)
	}
	return nil
}
