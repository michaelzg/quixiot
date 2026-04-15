package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"quixiot/internal/config"
	"quixiot/internal/logging"
	"quixiot/internal/proxy"
)

type proxyConfig struct {
	Listen   string `yaml:"listen"`
	Upstream string `yaml:"upstream"`
	Profile  string `yaml:"profile"`
	LogLevel string `yaml:"log_level"`
}

func defaults() proxyConfig {
	return proxyConfig{
		Listen:   "127.0.0.1:4443",
		Upstream: "127.0.0.1:4444",
		Profile:  "passthrough",
		LogLevel: "info",
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	def := defaults()

	cfgPath := fs.String("config", "", "YAML config file (optional)")
	listen := fs.String("listen", def.Listen, "client-facing UDP listen address")
	upstream := fs.String("upstream", def.Upstream, "upstream UDP address")
	profile := fs.String("profile", def.Profile, "network profile (Phase 5 supports only passthrough)")
	logLevel := fs.String("log-level", def.LogLevel, "log level: debug|info|warn|error")

	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := def
	if err := config.LoadFile(*cfgPath, &cfg); err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "listen":
			cfg.Listen = *listen
		case "upstream":
			cfg.Upstream = *upstream
		case "profile":
			cfg.Profile = *profile
		case "log-level":
			cfg.LogLevel = *logLevel
		}
	})

	if cfg.Profile != "passthrough" {
		return fmt.Errorf("proxy: unsupported profile %q (Phase 5 supports only passthrough)", cfg.Profile)
	}

	log, err := logging.New(logging.Options{Level: cfg.LogLevel})
	if err != nil {
		return err
	}
	logging.SetDefault(log)

	listenAddr, err := net.ResolveUDPAddr("udp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("proxy: resolve listen addr %q: %w", cfg.Listen, err)
	}
	listenConn, err := net.ListenUDP("udp", listenAddr)
	if err != nil {
		return fmt.Errorf("proxy: listen %q: %w", cfg.Listen, err)
	}

	upstreamAddr, err := net.ResolveUDPAddr("udp", cfg.Upstream)
	if err != nil {
		_ = listenConn.Close()
		return fmt.Errorf("proxy: resolve upstream addr %q: %w", cfg.Upstream, err)
	}

	p, err := proxy.New(proxy.Options{
		ListenConn:   listenConn,
		UpstreamAddr: upstreamAddr,
		Logger:       log,
	})
	if err != nil {
		_ = listenConn.Close()
		return err
	}
	defer p.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("proxy listening",
		"listen", listenConn.LocalAddr().String(),
		"upstream", upstreamAddr.String(),
		"profile", cfg.Profile,
		"idle_timeout", (5 * time.Minute).String(),
	)
	return p.Serve(ctx)
}
