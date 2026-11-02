package proxy

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/health"
)

func TestDemoOpeningScene(t *testing.T) {
	rules := health.Rules{History: 20, EvictAfter: 3, ReviveAfter: 1, Budget: 1500 * time.Millisecond}
	now := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	p := NewPool()
	SeedDemo(p, now, rules)
	if p.Len() != 6 {
		t.Fatalf("len %d", p.Len())
	}
	byHost := map[string]Proxy{}
	for _, px := range p.List() {
		byHost[px.Host] = px
		if px.InRotation() {
			t.Fatalf("%s is in rotation", px.Host)
		}
		if strings.Contains(px.Password, "demo") && viewHasSecret(p, px.Password) {
			t.Fatal("password in view")
		}
	}
	ash := byHost["198.51.100.21"]
	if ash.State.Status != health.StatusHealthy || ash.State.WindowOK != 18 || ash.State.WindowN != 20 {
		t.Fatalf("ashburn %+v", ash.State)
	}
	if health.ScoreTenths(ash.State.WindowOK, ash.State.WindowN, ash.State.Latency, rules.Budget, ash.State.Measured) != 870 {
		t.Fatalf("ashburn score %v latency %s", ash.State.Score, ash.State.Latency)
	}
	if ash.Place.City != "Ashburn" || ash.Place.Country != "US" {
		t.Fatalf("place %+v", ash.Place)
	}
	london := byHost["203.0.113.30"]
	if london.State.Status != health.StatusDead || london.State.LastError != "connection refused" {
		t.Fatalf("london %+v", london.State)
	}
	if health.ScoreTenths(london.State.WindowOK, london.State.WindowN, london.State.Latency, rules.Budget, true) != 245 {
		t.Fatalf("london score %v", london.State.Score)
	}
	unknown := byHost["203.0.113.40"]
	if unknown.State.Status != health.StatusUnknown || unknown.State.WindowN != 0 {
		t.Fatalf("unknown %+v", unknown.State)
	}
	frank := byHost["192.0.2.10"]
	if health.ScoreTenths(frank.State.WindowOK, frank.State.WindowN, frank.State.Latency, rules.Budget, true) != 976 {
		t.Fatalf("frankfurt tenths")
	}
	ams := byHost["192.0.2.11"]
	sg := byHost["198.51.100.20"]
	if health.ScoreTenths(ams.State.WindowOK, ams.State.WindowN, ams.State.Latency, rules.Budget, true) != 929 {
		t.Fatalf("amsterdam %+v", ams.State)
	}
	if health.ScoreTenths(sg.State.WindowOK, sg.State.WindowN, sg.State.Latency, rules.Budget, true) != 770 {
		t.Fatalf("singapore %+v", sg.State)
	}
	stats := p.Stats()
	if stats.Healthy != 4 || stats.Dead != 1 || stats.Unknown != 1 || stats.InRotation != 0 {
		t.Fatalf("%+v", stats)
	}
	if stats.AvgLatencyMs == nil || *stats.AvgLatencyMs != 262 {
		t.Fatalf("avg %v", stats.AvgLatencyMs)
	}
	if stats.SuccessRate == nil || math.Abs(*stats.SuccessRate-0.76) > 0.0001 {
		t.Fatalf("rate %v", stats.SuccessRate)
	}
}

func viewHasSecret(p *Pool, secret string) bool {
	for _, v := range p.Views() {
		if strings.Contains(v.Username, secret) || strings.Contains(v.LastError, secret) {
			return true
		}
	}
	return false
}

func TestUpsertKeepsHealthAndClearsDemo(t *testing.T) {
	rules := health.Rules{History: 20, EvictAfter: 3, ReviveAfter: 1, Budget: 1500 * time.Millisecond}
	p := NewPool()
	SeedDemo(p, time.Now(), rules)
	line := "http://192.0.2.10:8080"
	px, err := ParseLine(line)
	if err != nil {
		t.Fatal(err)
	}
	before, ok := p.Get(px.ID)
	if !ok || !before.Demo || before.State.Checks == 0 {
		t.Fatal("missing demo row")
	}
	if p.Upsert(px) {
		t.Fatal("expected update")
	}
	after, _ := p.Get(px.ID)
	if after.Demo || after.State.Checks != 0 || after.InRotation() != true {
		t.Fatalf("after demo=%v checks=%d rotation=%v", after.Demo, after.State.Checks, after.InRotation())
	}
	px.Password = "next"
	if p.Upsert(px) {
		t.Fatal("password change created a row")
	}
	got, _ := p.Get(px.ID)
	if got.Password != "next" || got.State.Status != health.StatusUnknown {
		t.Fatalf("password update lost state unexpectedly: %+v", got.State)
	}
}
