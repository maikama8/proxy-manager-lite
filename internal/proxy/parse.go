package proxy

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	SchemeHTTP   = "http"
	SchemeHTTPS  = "https"
	SchemeSOCKS5 = "socks5"

	maxLine = 2048
)

// LineError is one rejected line or JSON entry.
type LineError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

func (e LineError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Message)
}

// ParsePayload accepts a proxy list as text lines or as JSON.
//
// Text lines use any of these forms. Blank lines and lines whose first
// non-space character is # are ignored.
//
//	scheme://user:password@host:port
//	scheme://host:port
//	user:password@host:port
//	host:port
//	host:port:user:password
//
// scheme is http, https, socks5, or socks5h (treated as socks5). A line
// without a scheme is http. IPv6 hosts must use the URL form.
//
// JSON is either an array of those strings, an object {"proxies":[...]},
// or objects with scheme, host, port, username, and password.
func ParsePayload(body string) ([]Proxy, []LineError) {
	trim := strings.TrimSpace(body)
	if trim == "" {
		return nil, nil
	}
	if strings.HasPrefix(trim, "{") || strings.HasPrefix(trim, "[") {
		return parseJSON(trim)
	}
	return ParseList(strings.NewReader(body))
}

// ParseList reads a text proxy list.
func ParseList(r io.Reader) ([]Proxy, []LineError) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, maxLine)
	var (
		out  []Proxy
		errs []LineError
		line int
	)
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		px, err := ParseLine(text)
		if err != nil {
			errs = append(errs, LineError{Line: line, Message: err.Error()})
			continue
		}
		out = append(out, px)
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, LineError{Line: line, Message: err.Error()})
	}
	return out, errs
}

// ParseLine parses one proxy and returns an error for a blank or comment line.
func ParseLine(line string) (Proxy, error) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return Proxy{}, errors.New("empty proxy")
	}
	if len(s) > maxLine {
		return Proxy{}, errors.New("line is too long")
	}
	if strings.Contains(s, "://") {
		return parseURL(s)
	}
	if strings.HasPrefix(s, "[") {
		return parseURL("http://" + s)
	}
	if at := strings.LastIndex(s, "@"); at > 0 {
		return parseUserHost(s)
	}
	return parseColon(s)
}

func parseUserHost(s string) (Proxy, error) {
	at := strings.LastIndex(s, "@")
	user, pass, err := splitUserinfo(s[:at])
	if err != nil {
		return Proxy{}, err
	}
	u, err := url.Parse("http://" + s[at+1:])
	if err != nil || u.Host == "" {
		return Proxy{}, errors.New("invalid proxy URL")
	}
	u.User = url.UserPassword(user, pass)
	return parseURL(u.String())
}

func splitUserinfo(s string) (string, string, error) {
	if s == "" {
		return "", "", errors.New("missing user info")
	}
	user, pass, ok := strings.Cut(s, ":")
	if !ok {
		u, err := urlDecode(user)
		if err != nil {
			return "", "", err
		}
		return u, "", nil
	}
	u, err := urlDecode(user)
	if err != nil {
		return "", "", err
	}
	p, err := urlDecode(pass)
	if err != nil {
		return "", "", err
	}
	return u, p, nil
}

func urlDecode(s string) (string, error) {
	dec, err := url.PathUnescape(s)
	if err != nil {
		return "", fmt.Errorf("escape user info: %w", err)
	}
	return dec, nil
}

func parseColon(s string) (Proxy, error) {
	parts := strings.Split(s, ":")
	if len(parts) == 3 || len(parts) < 2 {
		return Proxy{}, errors.New("want host:port or host:port:user:password")
	}
	host := parts[0]
	port := parts[1]
	raw := "http://" + host + ":" + port
	if len(parts) >= 4 {
		user := parts[2]
		pass := strings.Join(parts[3:], ":")
		raw = "http://" + url.UserPassword(user, pass).String() + "@" + host + ":" + port
	}
	return parseURL(raw)
}

func parseURL(raw string) (Proxy, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Proxy{}, errors.New("invalid proxy URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "socks5h" {
		scheme = SchemeSOCKS5
	}
	switch scheme {
	case SchemeHTTP, SchemeHTTPS, SchemeSOCKS5:
	default:
		return Proxy{}, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	host := u.Hostname()
	portStr := u.Port()
	if host == "" || portStr == "" {
		return Proxy{}, errors.New("host and port are required")
	}
	if strings.ContainsAny(host, " /\t") {
		return Proxy{}, errors.New("invalid host")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return Proxy{}, errors.New("invalid port")
	}
	var user, pass string
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	if user == "" && pass != "" {
		return Proxy{}, errors.New("password without a username")
	}
	return Proxy{
		ID:       ID(scheme, user, host, port),
		Scheme:   scheme,
		Host:     host,
		Port:     port,
		Username: user,
		Password: pass,
	}, nil
}

// ID is a stable URL-safe id for scheme, username, host, and port.
// The password is not part of the id, so a password change updates the row.
func ID(scheme, username, host string, port int) string {
	sum := sha256.Sum256([]byte(strings.ToLower(scheme) + "\n" + username + "\n" + strings.ToLower(host) + "\n" + strconv.Itoa(port)))
	return hex.EncodeToString(sum[:8])
}

type jsonProxy struct {
	URL      string `json:"url"`
	Proxy    string `json:"proxy"`
	Scheme   string `json:"scheme"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func parseJSON(body string) ([]Proxy, []LineError) {
	var asList []json.RawMessage
	if err := json.Unmarshal([]byte(body), &asList); err == nil {
		return decodeEntries(asList)
	}
	var wrap struct {
		Proxies []json.RawMessage `json:"proxies"`
	}
	if err := json.Unmarshal([]byte(body), &wrap); err != nil {
		return nil, []LineError{{Line: 1, Message: "invalid proxy JSON"}}
	}
	return decodeEntries(wrap.Proxies)
}

func decodeEntries(raw []json.RawMessage) ([]Proxy, []LineError) {
	var (
		out  []Proxy
		errs []LineError
	)
	for i, item := range raw {
		line := i + 1
		var asString string
		if err := json.Unmarshal(item, &asString); err == nil {
			px, perr := ParseLine(asString)
			if perr != nil {
				errs = append(errs, LineError{Line: line, Message: perr.Error()})
				continue
			}
			out = append(out, px)
			continue
		}
		var obj jsonProxy
		if err := json.Unmarshal(item, &obj); err != nil {
			errs = append(errs, LineError{Line: line, Message: "invalid proxy entry"})
			continue
		}
		px, err := fromJSON(obj)
		if err != nil {
			errs = append(errs, LineError{Line: line, Message: err.Error()})
			continue
		}
		out = append(out, px)
	}
	return out, errs
}

func fromJSON(obj jsonProxy) (Proxy, error) {
	if obj.URL != "" {
		return ParseLine(obj.URL)
	}
	if obj.Proxy != "" {
		return ParseLine(obj.Proxy)
	}
	if obj.Host == "" || obj.Port == 0 {
		return Proxy{}, errors.New("proxy object needs url or host and port")
	}
	scheme := obj.Scheme
	if scheme == "" {
		scheme = SchemeHTTP
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(obj.Host, strconv.Itoa(obj.Port)),
	}
	if obj.Username != "" || obj.Password != "" {
		u.User = url.UserPassword(obj.Username, obj.Password)
	}
	return parseURL(u.String())
}
