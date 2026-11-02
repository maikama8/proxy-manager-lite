package proxy

import (
	"testing"
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/health"
)

func TestPoolRecordEvicts(t *testing.T) {
	p := NewPool()
	res := p.Import("http://192.0.2.10:8080\n")
	if res.Imported != 1 {
		t.Fatal(res)
	}
	px := p.List()[0]
	rules := health.Rules{History: 20, EvictAfter: 3, ReviveAfter: 2, Budget: time.Second}
	now := time.Now()
	for i := 0; i < 3; i++ {
		updated, ok := p.UpdateState(px.ID, func(st health.State) health.State {
			return health.Apply(st, health.Sample{OK: false, Err: "connection refused", At: now}, rules)
		})
		if !ok {
			t.Fatal("missing")
		}
		px = updated
	}
	if px.State.Status != health.StatusDead || px.InRotation() {
		t.Fatalf("status %s rotation %v", px.State.Status, px.InRotation())
	}
	p.UpdateState(px.ID, func(st health.State) health.State {
		return health.Apply(st, health.Sample{OK: true, Latency: 20 * time.Millisecond, At: now}, rules)
	})
	got, _ := p.Get(px.ID)
	if got.State.Status != health.StatusDead {
		t.Fatal("revived too early")
	}
	p.UpdateState(px.ID, func(st health.State) health.State {
		return health.Apply(st, health.Sample{OK: true, Latency: 20 * time.Millisecond, At: now}, rules)
	})
	got, _ = p.Get(px.ID)
	if got.State.Status != health.StatusHealthy || !got.InRotation() {
		t.Fatalf("status %s", got.State.Status)
	}
}

func TestRemoveAndNote(t *testing.T) {
	p := NewPool()
	p.Import("http://192.0.2.10:8080")
	id := p.List()[0].ID
	p.Note(id, false)
	got, _ := p.Get(id)
	if got.Served != 1 || got.Errors != 1 {
		t.Fatalf("note %+v", got)
	}
	if !p.Remove(id) || p.Remove(id) {
		t.Fatal("remove")
	}
	if p.Len() != 0 {
		t.Fatal("len")
	}
}

func TestViewsOmitPassword(t *testing.T) {
	p := NewPool()
	p.Import("http://ops:s3cret@192.0.2.10:8080")
	views := p.Views()
	if len(views) != 1 || !views[0].HasPassword || views[0].Username != "ops" {
		t.Fatalf("%+v", views)
	}
}
