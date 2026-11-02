package redact

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestErrorScrubsPassword(t *testing.T) {
	secret := "p@ss:word"
	raw := "dial " + secret + " and " + url.QueryEscape(secret)
	got := Error(errors.New(raw), secret)
	if strings.Contains(got, secret) || strings.Contains(got, url.QueryEscape(secret)) {
		t.Fatalf("secret leaked: %s", got)
	}
	if !strings.Contains(got, "xxxxx") {
		t.Fatalf("expected redaction marker, got %s", got)
	}
}

func TestErrorNil(t *testing.T) {
	if Error(nil, "x") != "" {
		t.Fatal("expected empty")
	}
}

func TestErrorTruncates(t *testing.T) {
	got := Error(errors.New(strings.Repeat("a", 400)), "")
	if len(got) != 180 {
		t.Fatalf("len %d", len(got))
	}
}
