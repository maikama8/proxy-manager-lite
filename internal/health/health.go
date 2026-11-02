// Package health scores a proxy from its recent probes.
//
// A probe is one attempt to fetch the health target through the proxy.
// The score is 0 to 100, in steps of a tenth:
//
//	70 points × (successes in the window / samples in the window)
//	30 points × (1 − latency / budget), clamped to 0…30
//
// Latency points are awarded only after a probe has succeeded. Latency is an
// exponential moving average of successful probes, with alpha 3/10. The
// window, the budget, and the eviction thresholds come from configuration.
package health

import "time"

const (
	// StatusUnknown means the proxy has not yet succeeded and has not been
	// evicted. Unknown proxies stay in rotation.
	StatusUnknown = "unknown"
	// StatusHealthy means the proxy is in rotation.
	StatusHealthy = "healthy"
	// StatusDead means the proxy was evicted and is waiting for a recheck.
	StatusDead = "dead"

	// LatencyAlphaNumerator and LatencyAlphaDenominator are the EMA weight
	// of a new successful sample: 3/10.
	LatencyAlphaNumerator   = 3
	LatencyAlphaDenominator = 10
)

// Rules are the knobs Apply uses. They are a copy of the health section of
// the config so the math can be tested without the loader.
type Rules struct {
	History     int
	EvictAfter  int
	ReviveAfter int
	Budget      time.Duration
}

// Sample is one finished probe.
type Sample struct {
	OK      bool
	Latency time.Duration
	Err     string
	At      time.Time
}

// State is the rolling health of one proxy. Apply returns a new value and
// does not keep the caller's sample slice.
type State struct {
	Status          string
	Latency         time.Duration
	SuccessRate     float64
	Score           float64
	ConsecutiveOK   int
	ConsecutiveFail int
	Samples         []bool
	Measured        bool
	Checks          int
	Successes       int
	WindowOK        int
	WindowN         int
	LastError       string
	LastChecked     time.Time
}

// Apply folds one sample into a copy of s.
func Apply(s State, sample Sample, rules Rules) State {
	if rules.History < 1 {
		rules.History = 1
	}
	s.Samples = append(append([]bool(nil), s.Samples...), sample.OK)
	if len(s.Samples) > rules.History {
		s.Samples = append([]bool(nil), s.Samples[len(s.Samples)-rules.History:]...)
	}

	s.Checks++
	s.LastChecked = sample.At
	if sample.OK {
		s.Successes++
		s.ConsecutiveOK++
		s.ConsecutiveFail = 0
		s.LastError = ""
		s.Latency = ema(s.Latency, sample.Latency, s.Measured)
		s.Measured = true
	} else {
		s.ConsecutiveFail++
		s.ConsecutiveOK = 0
		if sample.Err != "" {
			s.LastError = sample.Err
		} else {
			s.LastError = "probe failed"
		}
	}

	s.WindowOK, s.WindowN = windowStats(s.Samples)
	if s.WindowN == 0 {
		s.SuccessRate = 0
	} else {
		s.SuccessRate = float64(s.WindowOK) / float64(s.WindowN)
	}
	s.Score = Score(s.WindowOK, s.WindowN, s.Latency, rules.Budget, s.Measured)
	s.Status = NextStatus(s.Status, s.ConsecutiveOK, s.ConsecutiveFail, rules.EvictAfter, rules.ReviveAfter)
	return s
}

func windowStats(samples []bool) (successes, n int) {
	for _, ok := range samples {
		n++
		if ok {
			successes++
		}
	}
	return successes, n
}

func ema(prev, sample time.Duration, measured bool) time.Duration {
	if !measured {
		return sample
	}
	v := (int64(LatencyAlphaDenominator-LatencyAlphaNumerator)*int64(prev) + int64(LatencyAlphaNumerator)*int64(sample)) / int64(LatencyAlphaDenominator)
	if v < 0 {
		v = 0
	}
	return time.Duration(v)
}

// Score is the tenths-truncated score described in the package comment.
// It returns a float with one decimal place (87 means 87.0).
func Score(successes, samples int, latency, budget time.Duration, measured bool) float64 {
	return float64(ScoreTenths(successes, samples, latency, budget, measured)) / 10
}

// ScoreTenths is Score in tenths of a point. 870 is 87.0.
func ScoreTenths(successes, samples int, latency, budget time.Duration, measured bool) int {
	if samples <= 0 || successes < 0 {
		return 0
	}
	if successes > samples {
		successes = samples
	}
	rate := 700 * successes / samples
	if !measured || successes == 0 || budget <= 0 {
		return rate
	}
	if latency < 0 {
		latency = 0
	}
	if latency >= budget {
		return rate
	}
	lat := int((300 * int64(budget-latency)) / int64(budget))
	return rate + lat
}

// NextStatus is the eviction state machine.
//
// Unknown becomes healthy on the first success, and dead after EvictAfter
// consecutive failures. Healthy stays healthy until those failures arrive.
// Dead stays dead until ReviveAfter consecutive successes.
func NextStatus(current string, consecOK, consecFail, evictAfter, reviveAfter int) string {
	if current == "" {
		current = StatusUnknown
	}
	if evictAfter > 0 && consecFail >= evictAfter {
		return StatusDead
	}
	if current == StatusDead {
		if reviveAfter > 0 && consecOK >= reviveAfter {
			return StatusHealthy
		}
		return StatusDead
	}
	if consecOK > 0 || current == StatusHealthy {
		return StatusHealthy
	}
	return StatusUnknown
}
