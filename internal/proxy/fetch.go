package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

const maxImportBytes = 1 << 20

// ImportFile reads a proxy list from disk.
func ImportFile(pool *Pool, path string) (ImportResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return ImportResult{}, fmt.Errorf("open proxy file: %w", err)
	}
	defer f.Close()
	list, errs := ParseList(f)
	return apply(pool, list, errs), nil
}

// ImportURL fetches a list. Only http and https are accepted, the body is
// capped at 1 MiB, and redirects may not leave http.
func ImportURL(ctx context.Context, pool *Pool, client *http.Client, rawURL string) (ImportResult, error) {
	body, err := Fetch(ctx, client, rawURL)
	if err != nil {
		return ImportResult{}, err
	}
	return pool.Import(body), nil
}

// Fetch downloads a proxy list.
func Fetch(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("proxy url must be http or https")
	}
	if client == nil {
		client = &http.Client{}
	}
	cloned := *client
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("stopped after 3 redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("redirect left http")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain, application/json")
	resp, err := cloned.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch proxy list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch proxy list: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImportBytes+1))
	if err != nil {
		return "", fmt.Errorf("read proxy list: %w", err)
	}
	if len(body) > maxImportBytes {
		return "", fmt.Errorf("proxy list is larger than %d bytes", maxImportBytes)
	}
	return string(body), nil
}

func apply(pool *Pool, list []Proxy, errs []LineError) ImportResult {
	res := ImportResult{Errors: errs}
	for _, px := range list {
		if pool.Upsert(px) {
			res.Imported++
		} else {
			res.Updated++
		}
	}
	if res.Errors == nil {
		res.Errors = []LineError{}
	}
	return res
}
