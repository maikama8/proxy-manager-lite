// Package rotate picks a proxy for one gateway request.
//
// Round-robin walks the eligible ids in sorted order. Least-latency prefers
// the lowest measured latency, then the higher score, then the id. A proxy
// with no successful sample is slower than every measured proxy. Sticky
// keeps a key on the same proxy until that proxy leaves the eligible set,
// then hashes the key across the proxies that remain.
package rotate

import (
	"hash/fnv"
	"sort"
	"sync"
)

const (
	// RoundRobin is the default strategy name.
	RoundRobin = "round-robin"
	// LeastLatency picks the fastest measured proxy.
	LeastLatency = "least-latency"
	// Sticky pins a caller-supplied key to one proxy.
	Sticky = "sticky"
)

// Candidate is the data a strategy needs. Eligible is false for dead proxies
// and for anything the pool has held back.
type Candidate struct {
	ID       string
	Latency  int64 // milliseconds; ignored when Measured is false
	Score    float64
	Eligible bool
	Measured bool
}

// Strategy chooses one eligible candidate.
type Strategy interface {
	Name() string
	Pick(candidates []Candidate, key string) (string, bool)
}

// New returns a strategy. stickyMax caps the sticky map; at the cap the map
// is dropped and the next pick assigns again.
func New(name string, stickyMax int) (Strategy, error) {
	switch name {
	case RoundRobin:
		return &roundRobin{}, nil
	case LeastLatency:
		return leastLatency{}, nil
	case Sticky:
		if stickyMax < 1 {
			stickyMax = 1
		}
		return &sticky{max: stickyMax, assigned: map[string]string{}}, nil
	default:
		return nil, errUnknown(name)
	}
}

type unknownStrategy string

func errUnknown(name string) error { return unknownStrategy(name) }

func (e unknownStrategy) Error() string {
	return "unknown rotation strategy " + string(e)
}

// Holder is the live strategy. Replacing it drops round-robin position and
// sticky assignments.
type Holder struct {
	mu       sync.Mutex
	strategy Strategy
}

// NewHolder starts on round-robin when name is empty.
func NewHolder(name string, stickyMax int) (*Holder, error) {
	if name == "" {
		name = RoundRobin
	}
	s, err := New(name, stickyMax)
	if err != nil {
		return nil, err
	}
	return &Holder{strategy: s}, nil
}

// Set replaces the strategy.
func (h *Holder) Set(name string, stickyMax int) error {
	s, err := New(name, stickyMax)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.strategy = s
	h.mu.Unlock()
	return nil
}

// Name is the current strategy name.
func (h *Holder) Name() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.strategy.Name()
}

// Pick selects a proxy id.
func (h *Holder) Pick(candidates []Candidate, key string) (string, bool) {
	h.mu.Lock()
	s := h.strategy
	h.mu.Unlock()
	return s.Pick(candidates, key)
}

type roundRobin struct {
	mu   sync.Mutex
	next int
}

func (r *roundRobin) Name() string { return RoundRobin }

func (r *roundRobin) Pick(candidates []Candidate, _ string) (string, bool) {
	el := eligible(candidates)
	if len(el) == 0 {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := el[r.next%len(el)].ID
	r.next++
	return id, true
}

type leastLatency struct{}

func (leastLatency) Name() string { return LeastLatency }

func (leastLatency) Pick(candidates []Candidate, _ string) (string, bool) {
	el := eligible(candidates)
	if len(el) == 0 {
		return "", false
	}
	sort.Slice(el, func(i, j int) bool {
		if el[i].Measured != el[j].Measured {
			return el[i].Measured && !el[j].Measured
		}
		if el[i].Measured && el[i].Latency != el[j].Latency {
			return el[i].Latency < el[j].Latency
		}
		if el[i].Score != el[j].Score {
			return el[i].Score > el[j].Score
		}
		return el[i].ID < el[j].ID
	})
	return el[0].ID, true
}

type sticky struct {
	mu       sync.Mutex
	assigned map[string]string
	max      int
}

func (s *sticky) Name() string { return Sticky }

func (s *sticky) Pick(candidates []Candidate, key string) (string, bool) {
	if key == "" {
		return leastLatency{}.Pick(candidates, "")
	}
	el := eligible(candidates)
	if len(el) == 0 {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.assigned[key]; ok && containsID(el, id) {
		return id, true
	}
	id := el[hash(key)%len(el)].ID
	if len(s.assigned) >= s.max {
		s.assigned = map[string]string{}
	}
	s.assigned[key] = id
	return id, true
}

func eligible(in []Candidate) []Candidate {
	out := make([]Candidate, 0, len(in))
	for _, c := range in {
		if c.Eligible {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func containsID(list []Candidate, id string) bool {
	for _, c := range list {
		if c.ID == id {
			return true
		}
	}
	return false
}

func hash(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() & 0x7fffffff)
}
