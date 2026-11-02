package proxy

import (
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/health"
)

// Demo passwords are obvious placeholders. They are never returned by the API.
const (
	demoPassEdge = "edge-demo"
	demoPassSvc  = "svc-demo"
	demoPassOps  = "ops-demo"
)

// SeedDemo inserts the fixed opening pool. The addresses are from the
// documentation ranges in RFC 5737. Nothing is dialed. Scores come from
// health.Apply, so the board and the formula cannot drift apart.
func SeedDemo(p *Pool, now time.Time, rules health.Rules) {
	type spec struct {
		line    string
		city    string
		region  string
		country string
		samples []health.Sample
	}
	end := now.UTC()
	specs := []spec{
		{
			line: "http://192.0.2.10:8080", city: "Frankfurt", region: "Hesse", country: "DE",
			samples: pattern(20, 0, 0, 120*time.Millisecond, "", end.Add(-6*time.Second)),
		},
		{
			line: "http://edge:" + demoPassEdge + "@192.0.2.11:8080", city: "Amsterdam", country: "NL",
			samples: pattern(9, 1, 10, 180*time.Millisecond, "status 502", end.Add(-18*time.Second)),
		},
		{
			line: "https://198.51.100.21:8443", city: "Ashburn", region: "Virginia", country: "US",
			samples: pattern(9, 2, 9, 300*time.Millisecond, "status 502", end.Add(-4*time.Second)),
		},
		{
			line: "socks5://svc:" + demoPassSvc + "@198.51.100.20:1080", city: "Singapore", country: "SG",
			samples: pattern(8, 4, 8, 450*time.Millisecond, "i/o timeout", end.Add(-35*time.Second)),
		},
		{
			line: "http://ops:" + demoPassOps + "@203.0.113.30:3128", city: "London", country: "GB",
			samples: pattern(3, 17, 0, 800*time.Millisecond, "connection refused", end.Add(-70*time.Second)),
		},
		{
			line: "socks5://203.0.113.40:1080",
		},
	}
	for _, sp := range specs {
		px, err := ParseLine(sp.line)
		if err != nil {
			continue
		}
		px.Demo = true
		px.Place = Place{Country: sp.country, Region: sp.region, City: sp.city}
		var st health.State
		for _, sample := range sp.samples {
			st = health.Apply(st, sample, rules)
		}
		if st.Status == "" {
			st.Status = health.StatusUnknown
		}
		px.State = st
		p.Upsert(px)
	}
}

// pattern builds okAtStart successes, then failures, then okAtEnd successes.
// Every success uses the same latency, so the moving average stays there.
func pattern(okAtStart, fail, okAtEnd int, latency time.Duration, errText string, end time.Time) []health.Sample {
	n := okAtStart + fail + okAtEnd
	out := make([]health.Sample, 0, n)
	for i := 0; i < n; i++ {
		ok := i < okAtStart || i >= okAtStart+fail
		sample := health.Sample{OK: ok, At: end.Add(-time.Duration(n-1-i) * time.Second)}
		if ok {
			sample.Latency = latency
		} else {
			sample.Err = errText
		}
		out = append(out, sample)
	}
	return out
}
