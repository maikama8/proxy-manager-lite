// Command harbor runs a proxy pool, a health checker, a local gateway, and a small dashboard.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/maikama8/proxy-manager-lite/internal/app"
	"github.com/maikama8/proxy-manager-lite/internal/config"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "harbor: %s\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("harbor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", os.Getenv("PROXYMAN_CONFIG"), "path to a YAML config file")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println("harbor", version)
		return nil
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	log := app.Logger(cfg.Log.Level)
	application, err := app.New(cfg, log, version)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := application.Load(ctx); err != nil {
		return err
	}
	log.Info("harbor listening",
		"version", version,
		"api", cfg.Server.Listen,
		"gateway", cfg.Server.Gateway,
		"strategy", cfg.Rotation.Strategy,
		"demo", cfg.Demo,
		"proxies", application.Len(),
	)
	return application.Run(ctx)
}
