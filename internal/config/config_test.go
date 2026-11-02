package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:8080" || cfg.Server.Gateway != "127.0.0.1:8081" {
		t.Fatalf("listen %s gateway %s", cfg.Server.Listen, cfg.Server.Gateway)
	}
	if cfg.Rotation.Strategy != StrategyRoundRobin {
		t.Fatalf("strategy %s", cfg.Rotation.Strategy)
	}
	if cfg.Demo {
		t.Fatal("demo defaults off")
	}
	if cfg.Health.EvictAfter != 3 || cfg.Health.LatencyBudget != 1500*time.Millisecond {
		t.Fatalf("health defaults %+v", cfg.Health)
	}
}

func TestYAMLAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "harbor.yaml")
	body := []byte(strings.TrimSpace(`
server:
  listen: "127.0.0.1:9090"
  gateway: "127.0.0.1:9091"
health:
  interval: 0s
  target: "http://127.0.0.1:9/healthz"
rotation:
  strategy: least-latency
demo: true
log:
  level: debug
`) + "\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROXYMAN_SERVER_LISTEN", "0.0.0.0:18080")
	t.Setenv("PROXYMAN_ROTATION_STRATEGY", "sticky")
	t.Setenv("PROXYMAN_DEMO", "false")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "0.0.0.0:18080" {
		t.Fatalf("env did not win: %s", cfg.Server.Listen)
	}
	if cfg.Server.Gateway != "127.0.0.1:9091" {
		t.Fatalf("gateway from file lost: %s", cfg.Server.Gateway)
	}
	if cfg.Rotation.Strategy != StrategySticky {
		t.Fatalf("strategy %s", cfg.Rotation.Strategy)
	}
	if cfg.Demo {
		t.Fatal("env demo false lost")
	}
	if cfg.Health.Interval != 0 {
		t.Fatalf("interval %s", cfg.Health.Interval)
	}
	if cfg.Health.Timeout != 8*time.Second {
		t.Fatalf("timeout default lost: %s", cfg.Health.Timeout)
	}
	if cfg.Log.Level != "debug" {
		t.Fatalf("level %s", cfg.Log.Level)
	}
}

func TestRejectsUnknownFieldAndBadStrategy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "harbor.yaml")
	if err := os.WriteFile(path, []byte("rotation:\n  strategy: random\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "rotation.strategy") {
		t.Fatalf("got %v", err)
	}

	if err := os.WriteFile(path, []byte("nope: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestValidateSameAddressAndShortInterval(t *testing.T) {
	cfg := Defaults()
	cfg.Server.Gateway = cfg.Server.Listen
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected same-address error")
	}
	cfg = Defaults()
	cfg.Health.Interval = 50 * time.Millisecond
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected short interval error")
	}
	cfg = Defaults()
	cfg.Geo.Provider = GeoStatic
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing static file")
	}
}

func TestMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBadDurationEnv(t *testing.T) {
	t.Setenv("PROXYMAN_HEALTH_TIMEOUT", "soon")
	_, err := Load("")
	if err == nil || !strings.Contains(err.Error(), "HEALTH_TIMEOUT") {
		t.Fatalf("got %v", err)
	}
}
