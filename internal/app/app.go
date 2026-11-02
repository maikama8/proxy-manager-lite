// Package app wires the pool, the checker, the gateway, and the API into one process.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/maikama8/proxy-manager-lite/internal/api"
	"github.com/maikama8/proxy-manager-lite/internal/checker"
	"github.com/maikama8/proxy-manager-lite/internal/config"
	"github.com/maikama8/proxy-manager-lite/internal/gateway"
	"github.com/maikama8/proxy-manager-lite/internal/geo"
	"github.com/maikama8/proxy-manager-lite/internal/proxy"
	"github.com/maikama8/proxy-manager-lite/internal/rotate"
)

// App is a running Harbor process.
type App struct {
	cfg     config.Config
	log     *slog.Logger
	pool    *proxy.Pool
	rot     *rotate.Holder
	checker *checker.Checker
	gateway *gateway.Gateway
	api     *api.Server
	client  *http.Client
}

// New builds an App. Call Load before Run so imports finish before the
// listeners open.
func New(cfg config.Config, log *slog.Logger, version string) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	rot, err := rotate.NewHolder(cfg.Rotation.Strategy, cfg.Rotation.StickyMax)
	if err != nil {
		return nil, err
	}
	lookup, err := newLookup(cfg.Geo)
	if err != nil {
		return nil, err
	}
	pool := proxy.NewPool()
	client := &http.Client{Timeout: 15 * time.Second}
	target, err := httpURL(cfg.Health.Target)
	if err != nil {
		return nil, err
	}
	chk := checker.New(checker.Options{
		Pool:        pool,
		Geo:         lookup,
		Rules:       cfg.Health.Rules(),
		Interval:    cfg.Health.Interval,
		Recheck:     cfg.Health.RecheckInterval,
		Timeout:     cfg.Health.Timeout,
		GeoTimeout:  cfg.Geo.Timeout,
		Concurrency: cfg.Health.Concurrency,
		Log:         log,
	}, target)
	gw := gateway.New(pool, rot, cfg.Rotation.StickyHeader, cfg.Server.GatewayDialTimeout, log)
	return &App{
		cfg:     cfg,
		log:     log,
		pool:    pool,
		rot:     rot,
		checker: chk,
		gateway: gw,
		api:     api.New(cfg, pool, rot, chk, client, log, time.Now(), version),
		client:  client,
	}, nil
}

// Load applies the demo pool and the configured file or URL import.
func (a *App) Load(ctx context.Context) error {
	if a.cfg.Demo {
		proxy.SeedDemo(a.pool, time.Now(), a.cfg.Health.Rules())
	}
	if a.cfg.Pool.File != "" {
		res, err := proxy.ImportFile(a.pool, a.cfg.Pool.File)
		if err != nil {
			return err
		}
		a.log.Info("imported file", "path", a.cfg.Pool.File, "imported", res.Imported, "updated", res.Updated, "errors", len(res.Errors))
	}
	if a.cfg.Pool.URL != "" {
		res, err := proxy.ImportURL(ctx, a.pool, a.client, a.cfg.Pool.URL)
		if err != nil {
			return err
		}
		a.log.Info("imported url", "imported", res.Imported, "updated", res.Updated, "errors", len(res.Errors))
	}
	return nil
}

// Len is the pool size, for the startup log line.
func (a *App) Len() int { return a.pool.Len() }

// Run listens until ctx is canceled. The gateway has no write timeout:
// a CONNECT tunnel is a long copy, and a write deadline would close it.
func (a *App) Run(ctx context.Context) error {
	apiSrv := &http.Server{
		Addr:              a.cfg.Server.Listen,
		Handler:           a.api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       a.cfg.Server.ReadTimeout,
		WriteTimeout:      a.cfg.Server.WriteTimeout,
	}
	gwSrv := &http.Server{
		Addr:              a.cfg.Server.Gateway,
		Handler:           a.gateway,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		if err := apiSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("api: %w", err)
		}
	}()
	go func() {
		if err := gwSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("gateway: %w", err)
		}
	}()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.checker.Run(runCtx)
	if a.cfg.Pool.URL != "" && a.cfg.Pool.RefreshInterval > 0 {
		go a.refresh(runCtx)
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errCh:
		cancel()
	}

	shutCtx, shutCancel := context.WithTimeout(context.Background(), a.cfg.Server.ShutdownTimeout)
	defer shutCancel()
	_ = apiSrv.Shutdown(shutCtx)
	_ = gwSrv.Shutdown(shutCtx)
	a.gateway.Close()
	return runErr
}

func (a *App) refresh(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.Pool.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			res, err := proxy.ImportURL(ctx, a.pool, a.client, a.cfg.Pool.URL)
			if err != nil {
				a.log.Warn("refresh proxy list", "err", err)
				continue
			}
			a.log.Info("refreshed proxy list", "imported", res.Imported, "updated", res.Updated, "errors", len(res.Errors))
		}
	}
}

func newLookup(cfg config.Geo) (geo.Lookup, error) {
	switch cfg.Provider {
	case config.GeoNone, "":
		return geo.Nop{}, nil
	case config.GeoStatic:
		return geo.LoadStatic(cfg.StaticFile)
	case config.GeoHTTP:
		return geo.HTTP{RawURL: cfg.HTTPURL, Client: &http.Client{Timeout: cfg.Timeout}}, nil
	default:
		return nil, fmt.Errorf("unknown geo provider %q", cfg.Provider)
	}
}

func httpURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("health target: %w", err)
	}
	return u, nil
}

// Logger builds the process logger.
func Logger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
