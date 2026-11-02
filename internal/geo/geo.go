// Package geo resolves a proxy host to a place. The checker calls Lookup
// and ignores errors, so a lookup problem never fails a health probe.
package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Place is a label for the dashboard. Fields may be empty.
type Place struct {
	Country string `json:"country"`
	Region  string `json:"region"`
	City    string `json:"city"`
}

// Lookup finds a place for a proxy host. The host is whatever was imported:
// an IP or a name. Implementations should not dial the proxy.
type Lookup interface {
	Lookup(ctx context.Context, host string) (Place, error)
}

// Nop returns an empty place.
type Nop struct{}

// Lookup implements Lookup.
func (Nop) Lookup(context.Context, string) (Place, error) {
	return Place{}, nil
}

// Static reads a JSON object keyed by host or IP.
type Static struct {
	byHost map[string]Place
}

// LoadStatic reads a JSON file of host to place.
func LoadStatic(path string) (*Static, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read geo file: %w", err)
	}
	var raw map[string]Place
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse geo file: %w", err)
	}
	out := make(map[string]Place, len(raw))
	for host, place := range raw {
		out[strings.ToLower(strings.TrimSpace(host))] = place
	}
	return &Static{byHost: out}, nil
}

// Lookup implements Lookup. An unknown host is an empty place, not an error.
func (s *Static) Lookup(_ context.Context, host string) (Place, error) {
	if s == nil {
		return Place{}, nil
	}
	return s.byHost[strings.ToLower(strings.TrimSpace(host))], nil
}

// HTTP calls a user-configured URL. "{host}" in the URL is replaced with the
// host. Without that placeholder the host is appended as a path segment.
// A 200 body is JSON with country, region, and city. Other statuses are errors.
type HTTP struct {
	RawURL string
	Client *http.Client
}

// Lookup implements Lookup.
func (h HTTP) Lookup(ctx context.Context, host string) (Place, error) {
	endpoint, err := h.endpoint(host)
	if err != nil {
		return Place{}, err
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Place{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Place{}, fmt.Errorf("geo lookup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Place{}, fmt.Errorf("geo lookup: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return Place{}, fmt.Errorf("geo lookup: %w", err)
	}
	var place Place
	if err := json.Unmarshal(body, &place); err != nil {
		return Place{}, fmt.Errorf("geo lookup: %w", err)
	}
	return place, nil
}

func (h HTTP) endpoint(host string) (string, error) {
	raw := strings.TrimSpace(h.RawURL)
	if raw == "" {
		return "", errors.New("geo http url is empty")
	}
	if strings.Contains(raw, "{host}") {
		raw = strings.ReplaceAll(raw, "{host}", url.PathEscape(host))
	} else {
		raw = strings.TrimRight(raw, "/") + "/" + url.PathEscape(host)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("geo http url must be http or https")
	}
	return u.String(), nil
}
