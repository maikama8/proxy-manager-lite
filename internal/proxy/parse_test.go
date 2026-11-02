package proxy

import (
	"strings"
	"testing"
)

func TestParseLineFormats(t *testing.T) {
	cases := []struct {
		in           string
		scheme, host string
		port         int
		user, pass   string
	}{
		{in: "http://192.0.2.10:8080", scheme: SchemeHTTP, host: "192.0.2.10", port: 8080},
		{in: "HTTP://192.0.2.10:8080", scheme: SchemeHTTP, host: "192.0.2.10", port: 8080},
		{in: "https://edge:se%40cret@192.0.2.21:8443", scheme: SchemeHTTPS, host: "192.0.2.21", port: 8443, user: "edge", pass: "se@cret"},
		{in: "socks5://svc:secret@198.51.100.20:1080", scheme: SchemeSOCKS5, host: "198.51.100.20", port: 1080, user: "svc", pass: "secret"},
		{in: "socks5h://198.51.100.20:1080", scheme: SchemeSOCKS5, host: "198.51.100.20", port: 1080},
		{in: "192.0.2.10:8080", scheme: SchemeHTTP, host: "192.0.2.10", port: 8080},
		{in: "192.0.2.10:8080:ops:secret", scheme: SchemeHTTP, host: "192.0.2.10", port: 8080, user: "ops", pass: "secret"},
		{in: "192.0.2.10:8080:ops:p:ass", scheme: SchemeHTTP, host: "192.0.2.10", port: 8080, user: "ops", pass: "p:ass"},
		{in: "edge:secret@192.0.2.11:3128", scheme: SchemeHTTP, host: "192.0.2.11", port: 3128, user: "edge", pass: "secret"},
		{in: "http://[2001:db8::10]:8080", scheme: SchemeHTTP, host: "2001:db8::10", port: 8080},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			px, err := ParseLine(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if px.Scheme != tc.scheme || px.Host != tc.host || px.Port != tc.port || px.Username != tc.user || px.Password != tc.pass {
				t.Fatalf("got %+v", px)
			}
			if px.ID == "" || strings.Contains(px.ID, "/") {
				t.Fatalf("id %q", px.ID)
			}
			again, err := ParseLine(tc.in)
			if err != nil || again.ID != px.ID {
				t.Fatalf("id not stable")
			}
		})
	}
}

func TestParseLineRejects(t *testing.T) {
	for _, in := range []string{
		"not a proxy",
		"http://192.0.2.10",
		"ftp://192.0.2.10:21",
		"192.0.2.10:8080:onlyuser",
		"file:///etc/passwd",
		":8080",
	} {
		if _, err := ParseLine(in); err == nil {
			t.Fatalf("accepted %q", in)
		}
	}
}

func TestParseListSkipsComments(t *testing.T) {
	body := "# comment\n\nhttp://192.0.2.10:8080\nbad line\nsocks5://198.51.100.20:1080\n"
	got, errs := ParseList(strings.NewReader(body))
	if len(got) != 2 {
		t.Fatalf("parsed %d", len(got))
	}
	if len(errs) != 1 || errs[0].Line != 4 {
		t.Fatalf("errors %+v", errs)
	}
}

func TestParseJSONShapes(t *testing.T) {
	array := `["http://192.0.2.10:8080", "nope"]`
	got, errs := ParsePayload(array)
	if len(got) != 1 || len(errs) != 1 {
		t.Fatalf("array got %d errs %d", len(got), len(errs))
	}
	wrap := `{"proxies":[{"scheme":"socks5","host":"198.51.100.20","port":1080,"username":"svc","password":"secret"}]}`
	got, errs = ParsePayload(wrap)
	if len(errs) != 0 || len(got) != 1 {
		t.Fatalf("object %+v %+v", got, errs)
	}
	if got[0].Scheme != SchemeSOCKS5 || got[0].Username != "svc" || got[0].Password != "secret" || got[0].Port != 1080 {
		t.Fatalf("parsed %+v", got[0])
	}
}

func TestPasswordIsNotInID(t *testing.T) {
	a, err := ParseLine("http://u:one@192.0.2.10:8080")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseLine("http://u:two@192.0.2.10:8080")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatal("password changed the id")
	}
	c, err := ParseLine("http://other:one@192.0.2.10:8080")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == c.ID {
		t.Fatal("username did not change the id")
	}
}
