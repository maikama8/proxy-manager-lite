// Package checker probes the pool on a schedule and folds the results into
// health.Apply. Dead proxies are visited on the recheck interval. Demo
// proxies are never probed.
package checker

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/dialer"
	"github.com/maikama8/proxy-manager-lite/internal/geo"
	"github.com/maikama8/proxy-manager-lite/internal/health"
	"github.com/maikama8/proxy-manager-lite/internal/proxy"
	"github.com/maikama8/proxy-manager-lite/internal/redact"
)

// ErrNotFound is returned when the id is not in the pool.
var ErrNotFound = errors.New("proxy not found")

// ErrDemo is returned when a check is requested for a demo row.
var ErrDemo = errors.New("demo proxies are not probed")

// ProbeFunc measures one proxy. The default implementation fetches the
// health target through it.
type ProbeFunc func(ctx context.Context, px proxy.Proxy) (time.Duration, error)

// Options configures a checker.
type Options struct {
	Pool        *proxy.Pool
	Probe       ProbeFunc
	Geo         geo.Lookup
	Rules       health.Rules
	Interval    time.Duration
	Recheck     time.Duration
	Timeout     time.Duration
	GeoTimeout  time.Duration
	Concurrency int
	Log         *slog.Logger
}

// Checker runs probes.
type Checker struct {
	pool        *proxy.Pool
	probe       ProbeFunc
	geo         geo.Lookup
	rules       health.Rules
	interval    time.Duration
	recheck     time.Duration
	timeout     time.Duration
	geoTimeout  time.Duration
	concurrency int
	log         *slog.Logger
}

// New returns a checker. A nil probe fetches target through the proxy.
func New(opt Options, target *url.URL) *Checker {
	probe := opt.Probe
	if probe == nil {
		probe = func(ctx context.Context, px proxy.Proxy) (time.Duration, error) {
			return dialer.Probe(ctx, dialer.FromProxy(px), target)
		}
	}
	lookup := opt.Geo
	if lookup == nil {
		lookup = geo.Nop{}
	}
	log := opt.Log
	if log == nil {
		log = slog.Default()
	}
	n := opt.Concurrency
	if n < 1 {
		n = 1
	}
	return &Checker{
		pool:        opt.Pool,
		probe:       probe,
		geo:         lookup,
		rules:       opt.Rules,
		interval:    opt.Interval,
		recheck:     opt.Recheck,
		timeout:     opt.Timeout,
		geoTimeout:  opt.GeoTimeout,
		concurrency: n,
		log:         log,
	}
}

// Run probes immediately, then on each interval. A zero interval returns
// without probing so tests and a disabled schedule can share the constructor.
func (c *Checker) Run(ctx context.Context) {
	if c.interval <= 0 {
		return
	}
	c.Pass(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Pass(ctx)
		}
	}
}

// Pass probes every proxy that is due.
func (c *Checker) Pass(ctx context.Context) {
	now := time.Now()
	ids := c.pool.IDsDue(func(px proxy.Proxy) bool {
		return Due(px, now, c.interval, c.recheck)
	})
	if len(ids) == 0 {
		return
	}
	sem := make(chan struct{}, c.concurrency)
	var wg sync.WaitGroup
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := c.Check(ctx, id); err != nil {
				c.log.Debug("health check", "id", id, "err", err)
			}
		}(id)
	}
	wg.Wait()
}

// Check probes one proxy and stores the result.
func (c *Checker) Check(ctx context.Context, id string) (proxy.Proxy, error) {
	px, ok := c.pool.Get(id)
	if !ok {
		return proxy.Proxy{}, ErrNotFound
	}
	if px.Demo {
		return px, ErrDemo
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	lat, err := c.probe(pctx, px)
	cancel()
	sample := health.Sample{OK: err == nil, Latency: lat, At: time.Now()}
	var probeErr error
	if err != nil {
		sample.Err = redact.Error(err, px.Password)
		if sample.Err == "" {
			sample.Err = "probe failed"
		}
		sample.Latency = 0
		probeErr = errors.New(sample.Err)
	}
	updated, ok := c.pool.UpdateState(id, func(st health.State) health.State {
		return health.Apply(st, sample, c.rules)
	})
	if !ok {
		return proxy.Proxy{}, ErrNotFound
	}
	if err == nil && updated.Place.Country == "" && updated.Place.City == "" {
		c.lookup(ctx, updated)
		if again, ok := c.pool.Get(id); ok {
			updated = again
		}
	}
	if probeErr != nil {
		return updated, probeErr
	}
	return updated, nil
}

func (c *Checker) lookup(ctx context.Context, px proxy.Proxy) {
	timeout := c.geoTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	gctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	place, err := c.geo.Lookup(gctx, px.Host)
	if err != nil {
		c.log.Debug("geo lookup", "host", px.Host, "err", err)
		return
	}
	c.pool.SetPlace(px.ID, proxy.Place{Country: place.Country, Region: place.Region, City: place.City})
}

// Due reports whether a proxy should be probed at now.
func Due(px proxy.Proxy, now time.Time, interval, recheck time.Duration) bool {
	if px.Demo {
		return false
	}
	if px.State.LastChecked.IsZero() {
		return true
	}
	wait := interval
	if px.State.Status == health.StatusDead {
		wait = recheck
	}
	if wait <= 0 {
		return false
	}
	return !now.Before(px.State.LastChecked.Add(wait))
}
