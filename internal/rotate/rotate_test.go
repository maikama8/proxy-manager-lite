package rotate

import "testing"

func TestRoundRobinSkipsDeadAndWraps(t *testing.T) {
	s, err := New(RoundRobin, 10)
	if err != nil {
		t.Fatal(err)
	}
	list := []Candidate{
		{ID: "b", Eligible: true},
		{ID: "a", Eligible: true},
		{ID: "c", Eligible: false},
	}
	var got []string
	for i := 0; i < 4; i++ {
		id, ok := s.Pick(list, "ignored")
		if !ok {
			t.Fatal("expected a pick")
		}
		got = append(got, id)
	}
	want := []string{"a", "b", "a", "b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
}

func TestRoundRobinEmpty(t *testing.T) {
	s, _ := New(RoundRobin, 1)
	if _, ok := s.Pick([]Candidate{{ID: "a", Eligible: false}}, ""); ok {
		t.Fatal("picked a dead proxy")
	}
}

func TestLeastLatency(t *testing.T) {
	s := leastLatency{}
	list := []Candidate{
		{ID: "slow", Eligible: true, Measured: true, Latency: 400, Score: 90},
		{ID: "fast", Eligible: true, Measured: true, Latency: 80, Score: 70},
		{ID: "dead", Eligible: false, Measured: true, Latency: 1, Score: 100},
		{ID: "new", Eligible: true, Measured: false, Score: 0},
	}
	id, ok := s.Pick(list, "")
	if !ok || id != "fast" {
		t.Fatalf("got %s %v", id, ok)
	}

	tied := []Candidate{
		{ID: "b", Eligible: true, Measured: true, Latency: 100, Score: 50},
		{ID: "a", Eligible: true, Measured: true, Latency: 100, Score: 80},
	}
	id, ok = s.Pick(tied, "")
	if !ok || id != "a" {
		t.Fatalf("tie broke toward %s", id)
	}

	same := []Candidate{
		{ID: "b", Eligible: true, Measured: true, Latency: 100, Score: 80},
		{ID: "a", Eligible: true, Measured: true, Latency: 100, Score: 80},
	}
	id, _ = s.Pick(same, "")
	if id != "a" {
		t.Fatalf("id tie broke toward %s", id)
	}

	unmeasured := []Candidate{
		{ID: "b", Eligible: true, Measured: false},
		{ID: "a", Eligible: true, Measured: false},
	}
	id, _ = s.Pick(unmeasured, "")
	if id != "a" {
		t.Fatalf("unmeasured tie %s", id)
	}
}

func TestStickyStaysUntilProxyLeaves(t *testing.T) {
	s, err := New(Sticky, 100)
	if err != nil {
		t.Fatal(err)
	}
	base := []Candidate{
		{ID: "a", Eligible: true, Measured: true, Latency: 50},
		{ID: "b", Eligible: true, Measured: true, Latency: 200},
	}
	first, ok := s.Pick(base, "user-a")
	if !ok {
		t.Fatal("pick")
	}
	withExtra := append(append([]Candidate{}, base...), Candidate{ID: "c", Eligible: true, Measured: true, Latency: 10})
	for i := 0; i < 5; i++ {
		id, ok := s.Pick(withExtra, "user-a")
		if !ok || id != first {
			t.Fatalf("moved from %s to %s", first, id)
		}
	}
	var left []Candidate
	for _, c := range withExtra {
		if c.ID != first {
			left = append(left, c)
		}
	}
	moved, ok := s.Pick(left, "user-a")
	if !ok || moved == first {
		t.Fatalf("did not reassign, got %s", moved)
	}
	again, _ := s.Pick(left, "user-a")
	if again != moved {
		t.Fatalf("second assignment %s then %s", moved, again)
	}
}

func TestStickyEmptyKeyUsesLeastLatency(t *testing.T) {
	s, _ := New(Sticky, 10)
	list := []Candidate{
		{ID: "slow", Eligible: true, Measured: true, Latency: 500},
		{ID: "fast", Eligible: true, Measured: true, Latency: 20},
	}
	id, ok := s.Pick(list, "")
	if !ok || id != "fast" {
		t.Fatalf("got %s", id)
	}
}

func TestStickyDifferentKeysCanDiffer(t *testing.T) {
	s, _ := New(Sticky, 100)
	list := []Candidate{
		{ID: "a", Eligible: true},
		{ID: "b", Eligible: true},
	}
	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		key := string(rune('a' + i))
		id, ok := s.Pick(list, key)
		if !ok {
			t.Fatal("pick")
		}
		seen[id] = true
		id2, _ := s.Pick(list, key)
		if id2 != id {
			t.Fatal("key moved")
		}
	}
	if len(seen) != 2 {
		t.Fatalf("keys collapsed onto %v", seen)
	}
}

func TestStickyMapResetsAtCap(t *testing.T) {
	s, _ := New(Sticky, 2)
	list := []Candidate{{ID: "a", Eligible: true}, {ID: "b", Eligible: true}}
	s.Pick(list, "k1")
	s.Pick(list, "k2")
	// The third new key drops the map, then assigns itself.
	id, ok := s.Pick(list, "k3")
	if !ok || (id != "a" && id != "b") {
		t.Fatal(id)
	}
	// k1 is no longer pinned, so a fresh assignment is allowed. It must still
	// be stable on the following call.
	again, _ := s.Pick(list, "k1")
	if got, _ := s.Pick(list, "k1"); got != again {
		t.Fatal("unstable after reset")
	}
}

func TestHolderRejectsUnknown(t *testing.T) {
	if _, err := NewHolder("shuffle", 10); err == nil {
		t.Fatal("expected error")
	}
	h, err := NewHolder(RoundRobin, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Set(LeastLatency, 10); err != nil {
		t.Fatal(err)
	}
	if h.Name() != LeastLatency {
		t.Fatal(h.Name())
	}
}
