package health

import (
	"math"
	"testing"
	"time"
)

func TestScoreTenthsWorkedExamples(t *testing.T) {
	budget := 1500 * time.Millisecond
	cases := []struct {
		name       string
		ok, n      int
		latency    time.Duration
		measured   bool
		wantTenths int
	}{
		{name: "full and fast", ok: 20, n: 20, latency: 120 * time.Millisecond, measured: true, wantTenths: 976},
		{name: "ashburn", ok: 18, n: 20, latency: 300 * time.Millisecond, measured: true, wantTenths: 870},
		{name: "amsterdam", ok: 19, n: 20, latency: 180 * time.Millisecond, measured: true, wantTenths: 929},
		{name: "singapore", ok: 16, n: 20, latency: 450 * time.Millisecond, measured: true, wantTenths: 770},
		{name: "london", ok: 3, n: 20, latency: 800 * time.Millisecond, measured: true, wantTenths: 245},
		{name: "no samples", ok: 0, n: 0, measured: false, wantTenths: 0},
		{name: "failures only", ok: 0, n: 5, latency: 0, measured: false, wantTenths: 0},
		{name: "over budget", ok: 10, n: 10, latency: 2 * time.Second, measured: true, wantTenths: 700},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScoreTenths(tc.ok, tc.n, tc.latency, budget, tc.measured)
			if got != tc.wantTenths {
				t.Fatalf("ScoreTenths = %d, want %d", got, tc.wantTenths)
			}
			want := float64(tc.wantTenths) / 10
			if math.Abs(Score(tc.ok, tc.n, tc.latency, budget, tc.measured)-want) > 0.001 {
				t.Fatalf("Score drifted from tenths")
			}
		})
	}
}

func TestNextStatus(t *testing.T) {
	cases := []struct {
		name                    string
		current                 string
		ok, fail, evict, revive int
		want                    string
	}{
		{name: "blank is unknown", current: "", want: StatusUnknown, evict: 3, revive: 1},
		{name: "first success", current: StatusUnknown, ok: 1, evict: 3, revive: 1, want: StatusHealthy},
		{name: "one miss stays unknown", current: StatusUnknown, fail: 1, evict: 3, revive: 1, want: StatusUnknown},
		{name: "unknown evicted", current: StatusUnknown, fail: 3, evict: 3, revive: 1, want: StatusDead},
		{name: "healthy tolerates a miss", current: StatusHealthy, fail: 2, evict: 3, revive: 1, want: StatusHealthy},
		{name: "healthy evicted", current: StatusHealthy, fail: 3, evict: 3, revive: 1, want: StatusDead},
		{name: "one success revives", current: StatusDead, ok: 1, evict: 3, revive: 1, want: StatusHealthy},
		{name: "revive waits for two", current: StatusDead, ok: 1, evict: 3, revive: 2, want: StatusDead},
		{name: "second success revives", current: StatusDead, ok: 2, evict: 3, revive: 2, want: StatusHealthy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NextStatus(tc.current, tc.ok, tc.fail, tc.evict, tc.revive)
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestApplyEvictsAndRevives(t *testing.T) {
	rules := Rules{History: 20, EvictAfter: 3, ReviveAfter: 1, Budget: 1500 * time.Millisecond}
	var s State
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		s = Apply(s, Sample{OK: true, Latency: 100 * time.Millisecond, At: now}, rules)
	}
	if s.Status != StatusHealthy {
		t.Fatalf("status %s", s.Status)
	}
	for i := 0; i < 3; i++ {
		s = Apply(s, Sample{OK: false, Err: "connection refused", At: now.Add(time.Duration(i+1) * time.Second)}, rules)
	}
	if s.Status != StatusDead {
		t.Fatalf("status after failures %s", s.Status)
	}
	if s.LastError != "connection refused" {
		t.Fatalf("last error %q", s.LastError)
	}
	if s.Latency != 100*time.Millisecond {
		t.Fatalf("latency changed on failure: %s", s.Latency)
	}
	s = Apply(s, Sample{OK: true, Latency: 100 * time.Millisecond, At: now.Add(5 * time.Second)}, rules)
	if s.Status != StatusHealthy {
		t.Fatalf("status after revive %s", s.Status)
	}
	if s.LastError != "" {
		t.Fatalf("error not cleared: %q", s.LastError)
	}
}

func TestApplyWindowAndConstantLatency(t *testing.T) {
	rules := Rules{History: 20, EvictAfter: 3, ReviveAfter: 1, Budget: 1500 * time.Millisecond}
	var s State
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	// 9 ok, 2 fail, 9 ok. The Ashburn row.
	for i := 0; i < 20; i++ {
		ok := i < 9 || i >= 11
		sample := Sample{OK: ok, At: now.Add(time.Duration(i) * time.Second)}
		if ok {
			sample.Latency = 300 * time.Millisecond
		} else {
			sample.Err = "status 502"
		}
		s = Apply(s, sample, rules)
	}
	if s.WindowN != 20 || s.WindowOK != 18 {
		t.Fatalf("window %d/%d", s.WindowOK, s.WindowN)
	}
	if s.Status != StatusHealthy {
		t.Fatalf("status %s", s.Status)
	}
	if s.Latency != 300*time.Millisecond {
		t.Fatalf("ema %s", s.Latency)
	}
	if ScoreTenths(s.WindowOK, s.WindowN, s.Latency, rules.Budget, s.Measured) != 870 {
		t.Fatalf("score tenths %d", ScoreTenths(s.WindowOK, s.WindowN, s.Latency, rules.Budget, s.Measured))
	}
	if s.LastError != "" {
		t.Fatalf("trailing success should clear the error, got %q", s.LastError)
	}
}

func TestApplyTrimsHistory(t *testing.T) {
	rules := Rules{History: 5, EvictAfter: 3, ReviveAfter: 1, Budget: time.Second}
	var s State
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		s = Apply(s, Sample{OK: false, Err: "down", At: now}, rules)
	}
	if s.Status != StatusDead {
		t.Fatalf("status %s", s.Status)
	}
	for i := 0; i < 5; i++ {
		s = Apply(s, Sample{OK: true, Latency: 10 * time.Millisecond, At: now}, rules)
	}
	if s.WindowN != 5 || s.WindowOK != 5 {
		t.Fatalf("window %d/%d", s.WindowOK, s.WindowN)
	}
	if s.Status != StatusHealthy {
		t.Fatalf("status %s", s.Status)
	}
	if s.Checks != 10 {
		t.Fatalf("lifetime checks %d", s.Checks)
	}
}

func TestEMAMovesTowardNewSample(t *testing.T) {
	rules := Rules{History: 10, EvictAfter: 3, ReviveAfter: 1, Budget: time.Second}
	s := Apply(State{}, Sample{OK: true, Latency: 100 * time.Millisecond, At: time.Now()}, rules)
	s = Apply(s, Sample{OK: true, Latency: 200 * time.Millisecond, At: time.Now()}, rules)
	// 7/10*100 + 3/10*200 = 130
	if s.Latency != 130*time.Millisecond {
		t.Fatalf("ema %s, want 130ms", s.Latency)
	}
}
