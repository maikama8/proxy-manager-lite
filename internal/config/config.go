// Package config loads Harbor settings from a YAML file and the environment.
//
// A field set in the environment replaces the file. An empty environment
// variable is a value: PROXYMAN_POOL_FILE= clears a file path from YAML.
// Durations use Go's time.ParseDuration form (30s, 1500ms).
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/maikama8/proxy-manager-lite/internal/health"
)

const (
	envPrefix = "PROXYMAN_"

	StrategyRoundRobin   = "round-robin"
	StrategyLeastLatency = "least-latency"
	StrategySticky       = "sticky"

	GeoNone   = "none"
	GeoStatic = "static"
	GeoHTTP   = "http"
)

// Config is the process configuration after defaults, the file, and the
// environment have been applied.
type Config struct {
	Server   Server   `json:"server"`
	Pool     Pool     `json:"pool"`
	Health   Health   `json:"health"`
	Geo      Geo      `json:"geo"`
	Rotation Rotation `json:"rotation"`
	Log      Log      `json:"log"`
	Demo     bool     `json:"demo"`
}

// Server is the management listener and the gateway listener.
type Server struct {
	Listen             string        `json:"listen"`
	Gateway            string        `json:"gateway"`
	Token              string        `json:"-"`
	ReadTimeout        time.Duration `json:"readTimeout"`
	WriteTimeout       time.Duration `json:"writeTimeout"`
	ShutdownTimeout    time.Duration `json:"shutdownTimeout"`
	GatewayDialTimeout time.Duration `json:"gatewayDialTimeout"`
}

// Pool is the optional startup import.
type Pool struct {
	File            string        `json:"file"`
	URL             string        `json:"url"`
	RefreshInterval time.Duration `json:"refreshInterval"`
}

// Health is the probe schedule and the scoring rules.
type Health struct {
	Target          string        `json:"target"`
	Interval        time.Duration `json:"interval"`
	Timeout         time.Duration `json:"timeout"`
	Concurrency     int           `json:"concurrency"`
	History         int           `json:"history"`
	EvictAfter      int           `json:"evictAfter"`
	ReviveAfter     int           `json:"reviveAfter"`
	RecheckInterval time.Duration `json:"recheckInterval"`
	LatencyBudget   time.Duration `json:"latencyBudget"`
}

// Rules returns the pure scoring inputs.
func (h Health) Rules() health.Rules {
	return health.Rules{
		History:     h.History,
		EvictAfter:  h.EvictAfter,
		ReviveAfter: h.ReviveAfter,
		Budget:      h.LatencyBudget,
	}
}

// Geo selects the lookup implementation.
type Geo struct {
	Provider   string        `json:"provider"`
	StaticFile string        `json:"staticFile"`
	HTTPURL    string        `json:"-"`
	Timeout    time.Duration `json:"timeout"`
}

// Rotation selects how the gateway picks a proxy.
type Rotation struct {
	Strategy     string `json:"strategy"`
	StickyHeader string `json:"stickyHeader"`
	StickyMax    int    `json:"stickyMax"`
}

// Log is the slog level name.
type Log struct {
	Level string `json:"level"`
}

// Defaults are used for any field the file and the environment leave unset.
func Defaults() Config {
	return Config{
		Server: Server{
			Listen:             "127.0.0.1:8080",
			Gateway:            "127.0.0.1:8081",
			ReadTimeout:        15 * time.Second,
			WriteTimeout:       30 * time.Second,
			ShutdownTimeout:    10 * time.Second,
			GatewayDialTimeout: 15 * time.Second,
		},
		Health: Health{
			Target:          "https://example.com/",
			Interval:        30 * time.Second,
			Timeout:         8 * time.Second,
			Concurrency:     8,
			History:         20,
			EvictAfter:      3,
			ReviveAfter:     1,
			RecheckInterval: 60 * time.Second,
			LatencyBudget:   1500 * time.Millisecond,
		},
		Geo: Geo{
			Provider: GeoNone,
			Timeout:  3 * time.Second,
		},
		Rotation: Rotation{
			Strategy:     StrategyRoundRobin,
			StickyHeader: "X-Proxy-Session",
			StickyMax:    10000,
		},
		Log: Log{Level: "info"},
	}
}

// Load reads path, then applies PROXYMAN_* environment variables.
// An empty path skips the file and uses defaults plus the environment.
func Load(path string) (Config, error) {
	cfg := Defaults()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err := applyYAML(&cfg, b); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg *Config) normalize() {
	cfg.Log.Level = strings.ToLower(strings.TrimSpace(cfg.Log.Level))
	cfg.Rotation.Strategy = strings.ToLower(strings.TrimSpace(cfg.Rotation.Strategy))
	cfg.Geo.Provider = strings.ToLower(strings.TrimSpace(cfg.Geo.Provider))
	cfg.Server.Listen = strings.TrimSpace(cfg.Server.Listen)
	cfg.Server.Gateway = strings.TrimSpace(cfg.Server.Gateway)
}

type rawConfig struct {
	Server *struct {
		Listen             *string `yaml:"listen"`
		Gateway            *string `yaml:"gateway"`
		Token              *string `yaml:"token"`
		ReadTimeout        *string `yaml:"read_timeout"`
		WriteTimeout       *string `yaml:"write_timeout"`
		ShutdownTimeout    *string `yaml:"shutdown_timeout"`
		GatewayDialTimeout *string `yaml:"gateway_dial_timeout"`
	} `yaml:"server"`
	Pool *struct {
		File            *string `yaml:"file"`
		URL             *string `yaml:"url"`
		RefreshInterval *string `yaml:"refresh_interval"`
	} `yaml:"pool"`
	Health *struct {
		Target          *string `yaml:"target"`
		Interval        *string `yaml:"interval"`
		Timeout         *string `yaml:"timeout"`
		Concurrency     *int    `yaml:"concurrency"`
		History         *int    `yaml:"history"`
		EvictAfter      *int    `yaml:"evict_after"`
		ReviveAfter     *int    `yaml:"revive_after"`
		RecheckInterval *string `yaml:"recheck_interval"`
		LatencyBudget   *string `yaml:"latency_budget"`
	} `yaml:"health"`
	Geo *struct {
		Provider   *string `yaml:"provider"`
		StaticFile *string `yaml:"static_file"`
		HTTPURL    *string `yaml:"http_url"`
		Timeout    *string `yaml:"timeout"`
	} `yaml:"geo"`
	Rotation *struct {
		Strategy     *string `yaml:"strategy"`
		StickyHeader *string `yaml:"sticky_header"`
		StickyMax    *int    `yaml:"sticky_max"`
	} `yaml:"rotation"`
	Log *struct {
		Level *string `yaml:"level"`
	} `yaml:"log"`
	Demo *bool `yaml:"demo"`
}

func applyYAML(cfg *Config, b []byte) error {
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	var raw rawConfig
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	var err error
	if s := raw.Server; s != nil {
		setString(&cfg.Server.Listen, s.Listen)
		setString(&cfg.Server.Gateway, s.Gateway)
		setString(&cfg.Server.Token, s.Token)
		err = first(err, setDuration(&cfg.Server.ReadTimeout, "server.read_timeout", s.ReadTimeout))
		err = first(err, setDuration(&cfg.Server.WriteTimeout, "server.write_timeout", s.WriteTimeout))
		err = first(err, setDuration(&cfg.Server.ShutdownTimeout, "server.shutdown_timeout", s.ShutdownTimeout))
		err = first(err, setDuration(&cfg.Server.GatewayDialTimeout, "server.gateway_dial_timeout", s.GatewayDialTimeout))
	}
	if p := raw.Pool; p != nil {
		setString(&cfg.Pool.File, p.File)
		setString(&cfg.Pool.URL, p.URL)
		err = first(err, setDuration(&cfg.Pool.RefreshInterval, "pool.refresh_interval", p.RefreshInterval))
	}
	if h := raw.Health; h != nil {
		setString(&cfg.Health.Target, h.Target)
		err = first(err, setDuration(&cfg.Health.Interval, "health.interval", h.Interval))
		err = first(err, setDuration(&cfg.Health.Timeout, "health.timeout", h.Timeout))
		setInt(&cfg.Health.Concurrency, h.Concurrency)
		setInt(&cfg.Health.History, h.History)
		setInt(&cfg.Health.EvictAfter, h.EvictAfter)
		setInt(&cfg.Health.ReviveAfter, h.ReviveAfter)
		err = first(err, setDuration(&cfg.Health.RecheckInterval, "health.recheck_interval", h.RecheckInterval))
		err = first(err, setDuration(&cfg.Health.LatencyBudget, "health.latency_budget", h.LatencyBudget))
	}
	if g := raw.Geo; g != nil {
		setString(&cfg.Geo.Provider, g.Provider)
		setString(&cfg.Geo.StaticFile, g.StaticFile)
		setString(&cfg.Geo.HTTPURL, g.HTTPURL)
		err = first(err, setDuration(&cfg.Geo.Timeout, "geo.timeout", g.Timeout))
	}
	if r := raw.Rotation; r != nil {
		setString(&cfg.Rotation.Strategy, r.Strategy)
		setString(&cfg.Rotation.StickyHeader, r.StickyHeader)
		setInt(&cfg.Rotation.StickyMax, r.StickyMax)
	}
	if l := raw.Log; l != nil {
		setString(&cfg.Log.Level, l.Level)
	}
	if raw.Demo != nil {
		cfg.Demo = *raw.Demo
	}
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	return nil
}

func applyEnv(cfg *Config) error {
	var err error
	take := func(key string, fn func(string) error) {
		if err != nil {
			return
		}
		v, ok := os.LookupEnv(envPrefix + key)
		if !ok {
			return
		}
		err = fn(v)
	}
	take("SERVER_LISTEN", func(v string) error { cfg.Server.Listen = v; return nil })
	take("SERVER_GATEWAY", func(v string) error { cfg.Server.Gateway = v; return nil })
	take("SERVER_TOKEN", func(v string) error { cfg.Server.Token = v; return nil })
	take("SERVER_READ_TIMEOUT", func(v string) error { return parseInto(&cfg.Server.ReadTimeout, "PROXYMAN_SERVER_READ_TIMEOUT", v) })
	take("SERVER_WRITE_TIMEOUT", func(v string) error { return parseInto(&cfg.Server.WriteTimeout, "PROXYMAN_SERVER_WRITE_TIMEOUT", v) })
	take("SERVER_SHUTDOWN_TIMEOUT", func(v string) error {
		return parseInto(&cfg.Server.ShutdownTimeout, "PROXYMAN_SERVER_SHUTDOWN_TIMEOUT", v)
	})
	take("SERVER_GATEWAY_DIAL_TIMEOUT", func(v string) error {
		return parseInto(&cfg.Server.GatewayDialTimeout, "PROXYMAN_SERVER_GATEWAY_DIAL_TIMEOUT", v)
	})
	take("POOL_FILE", func(v string) error { cfg.Pool.File = v; return nil })
	take("POOL_URL", func(v string) error { cfg.Pool.URL = v; return nil })
	take("POOL_REFRESH_INTERVAL", func(v string) error {
		return parseInto(&cfg.Pool.RefreshInterval, "PROXYMAN_POOL_REFRESH_INTERVAL", v)
	})
	take("HEALTH_TARGET", func(v string) error { cfg.Health.Target = v; return nil })
	take("HEALTH_INTERVAL", func(v string) error { return parseInto(&cfg.Health.Interval, "PROXYMAN_HEALTH_INTERVAL", v) })
	take("HEALTH_TIMEOUT", func(v string) error { return parseInto(&cfg.Health.Timeout, "PROXYMAN_HEALTH_TIMEOUT", v) })
	take("HEALTH_CONCURRENCY", func(v string) error { return parseIntInto(&cfg.Health.Concurrency, "PROXYMAN_HEALTH_CONCURRENCY", v) })
	take("HEALTH_HISTORY", func(v string) error { return parseIntInto(&cfg.Health.History, "PROXYMAN_HEALTH_HISTORY", v) })
	take("HEALTH_EVICT_AFTER", func(v string) error { return parseIntInto(&cfg.Health.EvictAfter, "PROXYMAN_HEALTH_EVICT_AFTER", v) })
	take("HEALTH_REVIVE_AFTER", func(v string) error { return parseIntInto(&cfg.Health.ReviveAfter, "PROXYMAN_HEALTH_REVIVE_AFTER", v) })
	take("HEALTH_RECHECK_INTERVAL", func(v string) error {
		return parseInto(&cfg.Health.RecheckInterval, "PROXYMAN_HEALTH_RECHECK_INTERVAL", v)
	})
	take("HEALTH_LATENCY_BUDGET", func(v string) error {
		return parseInto(&cfg.Health.LatencyBudget, "PROXYMAN_HEALTH_LATENCY_BUDGET", v)
	})
	take("GEO_PROVIDER", func(v string) error { cfg.Geo.Provider = v; return nil })
	take("GEO_STATIC_FILE", func(v string) error { cfg.Geo.StaticFile = v; return nil })
	take("GEO_HTTP_URL", func(v string) error { cfg.Geo.HTTPURL = v; return nil })
	take("GEO_TIMEOUT", func(v string) error { return parseInto(&cfg.Geo.Timeout, "PROXYMAN_GEO_TIMEOUT", v) })
	take("ROTATION_STRATEGY", func(v string) error { cfg.Rotation.Strategy = v; return nil })
	take("ROTATION_STICKY_HEADER", func(v string) error { cfg.Rotation.StickyHeader = v; return nil })
	take("ROTATION_STICKY_MAX", func(v string) error { return parseIntInto(&cfg.Rotation.StickyMax, "PROXYMAN_ROTATION_STICKY_MAX", v) })
	take("LOG_LEVEL", func(v string) error { cfg.Log.Level = v; return nil })
	take("DEMO", func(v string) error {
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			return fmt.Errorf("PROXYMAN_DEMO: %w", perr)
		}
		cfg.Demo = b
		return nil
	})
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	return nil
}

// Validate checks the configuration after defaults and overrides.
func (cfg Config) Validate() error {
	if err := requireAddr("server.listen", cfg.Server.Listen); err != nil {
		return err
	}
	if err := requireAddr("server.gateway", cfg.Server.Gateway); err != nil {
		return err
	}
	if cfg.Server.Listen == cfg.Server.Gateway {
		return errors.New("server.listen and server.gateway must be different addresses")
	}
	if err := positiveDur("server.read_timeout", cfg.Server.ReadTimeout); err != nil {
		return err
	}
	if err := positiveDur("server.write_timeout", cfg.Server.WriteTimeout); err != nil {
		return err
	}
	if err := positiveDur("server.shutdown_timeout", cfg.Server.ShutdownTimeout); err != nil {
		return err
	}
	if err := boundedDur("server.gateway_dial_timeout", cfg.Server.GatewayDialTimeout, time.Millisecond, 5*time.Minute); err != nil {
		return err
	}
	if cfg.Pool.URL != "" {
		if err := httpURL("pool.url", cfg.Pool.URL); err != nil {
			return err
		}
	}
	if cfg.Pool.RefreshInterval < 0 {
		return errors.New("pool.refresh_interval must be >= 0")
	}
	if cfg.Pool.RefreshInterval > 0 && cfg.Pool.URL == "" {
		return errors.New("pool.refresh_interval requires pool.url")
	}
	if err := httpURL("health.target", cfg.Health.Target); err != nil {
		return err
	}
	if cfg.Health.Interval != 0 {
		if err := boundedDur("health.interval", cfg.Health.Interval, 200*time.Millisecond, 24*time.Hour); err != nil {
			return err
		}
	}
	if err := boundedDur("health.timeout", cfg.Health.Timeout, 200*time.Millisecond, 5*time.Minute); err != nil {
		return err
	}
	if cfg.Health.Concurrency < 1 || cfg.Health.Concurrency > 256 {
		return errors.New("health.concurrency must be from 1 to 256")
	}
	if cfg.Health.History < 1 || cfg.Health.History > 1000 {
		return errors.New("health.history must be from 1 to 1000")
	}
	if cfg.Health.EvictAfter < 1 {
		return errors.New("health.evict_after must be >= 1")
	}
	if cfg.Health.ReviveAfter < 1 {
		return errors.New("health.revive_after must be >= 1")
	}
	if err := boundedDur("health.recheck_interval", cfg.Health.RecheckInterval, 200*time.Millisecond, 24*time.Hour); err != nil {
		return err
	}
	if err := boundedDur("health.latency_budget", cfg.Health.LatencyBudget, time.Millisecond, 2*time.Minute); err != nil {
		return err
	}
	switch cfg.Geo.Provider {
	case GeoNone, GeoStatic, GeoHTTP:
	default:
		return fmt.Errorf("geo.provider must be %s, %s, or %s", GeoNone, GeoStatic, GeoHTTP)
	}
	if cfg.Geo.Provider == GeoStatic && strings.TrimSpace(cfg.Geo.StaticFile) == "" {
		return errors.New("geo.static_file is required when geo.provider is static")
	}
	if cfg.Geo.Provider == GeoHTTP {
		if err := httpURL("geo.http_url", cfg.Geo.HTTPURL); err != nil {
			return err
		}
	}
	if err := positiveDur("geo.timeout", cfg.Geo.Timeout); err != nil {
		return err
	}
	switch cfg.Rotation.Strategy {
	case StrategyRoundRobin, StrategyLeastLatency, StrategySticky:
	default:
		return fmt.Errorf("rotation.strategy must be %s, %s, or %s", StrategyRoundRobin, StrategyLeastLatency, StrategySticky)
	}
	if strings.TrimSpace(cfg.Rotation.StickyHeader) == "" {
		return errors.New("rotation.sticky_header is required")
	}
	if cfg.Rotation.StickyMax < 1 || cfg.Rotation.StickyMax > 1_000_000 {
		return errors.New("rotation.sticky_max must be from 1 to 1000000")
	}
	switch cfg.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("log.level must be debug, info, warn, or error")
	}
	return nil
}

func requireAddr(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("%s must be host:port: %w", name, err)
	}
	if port == "" {
		return fmt.Errorf("%s must include a port", name)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%s has an invalid port", name)
	}
	return nil
}

func httpURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must be an http or https URL", name)
	}
	return nil
}

func positiveDur(name string, d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("%s must be > 0", name)
	}
	return nil
}

func boundedDur(name string, d, min, max time.Duration) error {
	if d < min || d > max {
		return fmt.Errorf("%s must be from %s to %s", name, min, max)
	}
	return nil
}

func setString(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

func setInt(dst *int, src *int) {
	if src != nil {
		*dst = *src
	}
}

func setDuration(dst *time.Duration, name string, src *string) error {
	if src == nil {
		return nil
	}
	return parseInto(dst, name, *src)
}

func parseInto(dst *time.Duration, name, raw string) error {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	*dst = d
	return nil
}

func parseIntInto(dst *int, name, raw string) error {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	*dst = n
	return nil
}

func first(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
