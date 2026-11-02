// Package redact removes proxy passwords from errors before they are logged
// or stored on a proxy record.
package redact

import (
	"net/url"
	"strings"
)

// Error returns err.Error with the secret and its URL encodings replaced.
// A nil error returns an empty string. The result is a single line, trimmed
// to 180 bytes.
func Error(err error, secret string) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	for _, v := range variants(secret) {
		if v == "" {
			continue
		}
		s = strings.ReplaceAll(s, v, "xxxxx")
	}
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > 180 {
		s = s[:180]
	}
	return s
}

func variants(secret string) []string {
	if secret == "" {
		return nil
	}
	encoded := strings.TrimPrefix(url.UserPassword("user", secret).String(), "user:")
	out := []string{secret, url.QueryEscape(secret), url.PathEscape(secret), encoded}
	seen := make(map[string]struct{}, len(out))
	uniq := make([]string, 0, len(out))
	for _, v := range out {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		uniq = append(uniq, v)
	}
	return uniq
}
