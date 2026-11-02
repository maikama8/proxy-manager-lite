package proxy

import (
	"fmt"
	"net"
	"sort"
	"sync"

	"github.com/maikama8/proxy-manager-lite/internal/health"
)

// Place is a geo label stored on a proxy. Empty fields are left blank.
type Place struct {
	Country string
	Region  string
	City    string
}

// Proxy is one upstream. Password is kept for dialing and is never copied
// into the JSON view.
type Proxy struct {
	ID       string
	Scheme   string
	Host     string
	Port     int
	Username string
	Password string
	Demo     bool
	Place    Place
	State    health.State
	Served   int
	Errors   int
}

// Addr is host:port.
func (p Proxy) Addr() string {
	return net.JoinHostPort(p.Host, fmt.Sprintf("%d", p.Port))
}

// Redacted is scheme://host:port, with no user info.
func (p Proxy) Redacted() string {
	return p.Scheme + "://" + p.Addr()
}

// InRotation reports whether the gateway may select this proxy.
// Demo rows are displayed and are not dialed. Dead rows wait for a recheck.
func (p Proxy) InRotation() bool {
	if p.Demo {
		return false
	}
	switch p.State.Status {
	case health.StatusHealthy, health.StatusUnknown:
		return true
	default:
		return false
	}
}

// View is the management API shape. It has no password.
type View struct {
	ID              string   `json:"id"`
	Scheme          string   `json:"scheme"`
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	Username        string   `json:"username,omitempty"`
	HasPassword     bool     `json:"hasPassword"`
	Demo            bool     `json:"demo"`
	Status          string   `json:"status"`
	InRotation      bool     `json:"inRotation"`
	LatencyMs       *int64   `json:"latencyMs"`
	SuccessRate     *float64 `json:"successRate"`
	WindowOK        int      `json:"windowOk"`
	WindowN         int      `json:"windowN"`
	Score           float64  `json:"score"`
	Country         string   `json:"country,omitempty"`
	Region          string   `json:"region,omitempty"`
	City            string   `json:"city,omitempty"`
	LastChecked     string   `json:"lastChecked,omitempty"`
	LastError       string   `json:"lastError,omitempty"`
	ConsecutiveFail int      `json:"consecutiveFail"`
	Checks          int      `json:"checks"`
	Served          int      `json:"served"`
	Errors          int      `json:"errors"`
}

// Stats is the pool headline.
type Stats struct {
	Total           int      `json:"total"`
	Healthy         int      `json:"healthy"`
	Dead            int      `json:"dead"`
	Unknown         int      `json:"unknown"`
	InRotation      int      `json:"inRotation"`
	AvgLatencyMs    *int64   `json:"avgLatencyMs"`
	SuccessRate     *float64 `json:"successRate"`
	Checks          int      `json:"checks"`
	GatewayRequests int      `json:"gatewayRequests"`
	GatewayErrors   int      `json:"gatewayErrors"`
}

// ImportResult is the outcome of adding a batch.
type ImportResult struct {
	Imported int         `json:"imported"`
	Updated  int         `json:"updated"`
	Errors   []LineError `json:"errors"`
}

// Pool is the in-memory proxy set.
type Pool struct {
	mu    sync.Mutex
	byID  map[string]*Proxy
	order []string
}

// NewPool returns an empty pool.
func NewPool() *Pool {
	return &Pool{byID: map[string]*Proxy{}}
}

// Upsert inserts a proxy or refreshes the credentials of an existing one.
// Health state is kept across a password change. Replacing a demo row with
// a real import clears the demo numbers so the checker starts clean.
func (p *Pool) Upsert(in Proxy) (created bool) {
	if in.ID == "" {
		in.ID = ID(in.Scheme, in.Username, in.Host, in.Port)
	}
	if in.State.Status == "" {
		in.State.Status = health.StatusUnknown
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.byID[in.ID]; ok {
		old.Scheme = in.Scheme
		old.Host = in.Host
		old.Port = in.Port
		old.Username = in.Username
		old.Password = in.Password
		if old.Demo && !in.Demo {
			old.Demo = false
			old.State = health.State{Status: health.StatusUnknown}
			old.Place = Place{}
		}
		return false
	}
	cp := in
	cp.State.Samples = append([]bool(nil), in.State.Samples...)
	p.byID[in.ID] = &cp
	p.order = append(p.order, in.ID)
	return true
}

// Import parses text or JSON and upserts every accepted proxy.
func (p *Pool) Import(body string) ImportResult {
	list, errs := ParsePayload(body)
	res := ImportResult{Errors: errs}
	for _, px := range list {
		if p.Upsert(px) {
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

// Remove deletes a proxy. It reports whether the id was present.
func (p *Pool) Remove(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.byID[id]; !ok {
		return false
	}
	delete(p.byID, id)
	next := p.order[:0]
	for _, existing := range p.order {
		if existing != id {
			next = append(next, existing)
		}
	}
	p.order = next
	return true
}

// Get returns a copy.
func (p *Pool) Get(id string) (Proxy, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	px, ok := p.byID[id]
	if !ok {
		return Proxy{}, false
	}
	return clone(*px), true
}

// List returns proxies in insertion order.
func (p *Pool) List() []Proxy {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Proxy, 0, len(p.order))
	for _, id := range p.order {
		out = append(out, clone(*p.byID[id]))
	}
	return out
}

// Len is the number of proxies.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.byID)
}

// UpdateState folds a health transition under the pool lock.
func (p *Pool) UpdateState(id string, fn func(health.State) health.State) (Proxy, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	px, ok := p.byID[id]
	if !ok {
		return Proxy{}, false
	}
	px.State = fn(px.State)
	return clone(*px), true
}

// SetPlace stores a lookup result. An empty country does not erase a label
// that is already there.
func (p *Pool) SetPlace(id string, place Place) {
	if place.Country == "" && place.City == "" && place.Region == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	px, ok := p.byID[id]
	if !ok {
		return
	}
	px.Place = place
}

// Note records one gateway attempt.
func (p *Pool) Note(id string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	px, found := p.byID[id]
	if !found {
		return
	}
	px.Served++
	if !ok {
		px.Errors++
	}
}

// IDsDue is the checker schedule. Demo proxies are never due.
func (p *Pool) IDsDue(ready func(Proxy) bool) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, 0)
	for _, id := range p.order {
		px := p.byID[id]
		if px.Demo {
			continue
		}
		if ready(clone(*px)) {
			ids = append(ids, id)
		}
	}
	return ids
}

// Stats aggregates the pool.
func (p *Pool) Stats() Stats {
	list := p.List()
	var s Stats
	s.Total = len(list)
	var latSum int64
	var latN int
	var winOK, winN int
	for _, px := range list {
		switch px.State.Status {
		case health.StatusHealthy:
			s.Healthy++
		case health.StatusDead:
			s.Dead++
		default:
			s.Unknown++
		}
		if px.InRotation() {
			s.InRotation++
		}
		s.Checks += px.State.Checks
		s.GatewayRequests += px.Served
		s.GatewayErrors += px.Errors
		winOK += px.State.WindowOK
		winN += px.State.WindowN
		if px.State.Status == health.StatusHealthy && px.State.Measured {
			latSum += px.State.Latency.Milliseconds()
			latN++
		}
	}
	if latN > 0 {
		avg := latSum / int64(latN)
		s.AvgLatencyMs = &avg
	}
	if winN > 0 {
		rate := float64(winOK) / float64(winN)
		s.SuccessRate = &rate
	}
	return s
}

// Views returns the API rows in insertion order.
func (p *Pool) Views() []View {
	list := p.List()
	out := make([]View, 0, len(list))
	for _, px := range list {
		out = append(out, toView(px))
	}
	return out
}

func toView(px Proxy) View {
	status := px.State.Status
	if status == "" {
		status = health.StatusUnknown
	}
	v := View{
		ID:              px.ID,
		Scheme:          px.Scheme,
		Host:            px.Host,
		Port:            px.Port,
		Username:        px.Username,
		HasPassword:     px.Password != "",
		Demo:            px.Demo,
		Status:          status,
		InRotation:      px.InRotation(),
		WindowOK:        px.State.WindowOK,
		WindowN:         px.State.WindowN,
		Score:           px.State.Score,
		Country:         px.Place.Country,
		Region:          px.Place.Region,
		City:            px.Place.City,
		LastError:       px.State.LastError,
		ConsecutiveFail: px.State.ConsecutiveFail,
		Checks:          px.State.Checks,
		Served:          px.Served,
		Errors:          px.Errors,
	}
	if px.State.Measured {
		ms := px.State.Latency.Milliseconds()
		v.LatencyMs = &ms
	}
	if px.State.WindowN > 0 {
		rate := px.State.SuccessRate
		v.SuccessRate = &rate
	}
	if !px.State.LastChecked.IsZero() {
		v.LastChecked = px.State.LastChecked.UTC().Format("2006-01-02T15:04:05Z")
	}
	return v
}

func clone(px Proxy) Proxy {
	px.State.Samples = append([]bool(nil), px.State.Samples...)
	return px
}

// SortIDs returns a sorted copy. Tests and rotation use a stable order.
func SortIDs(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}
