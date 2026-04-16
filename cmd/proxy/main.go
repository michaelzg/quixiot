package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"quixiot/internal/config"
	"quixiot/internal/impair"
	"quixiot/internal/logging"
	"quixiot/internal/metrics"
	"quixiot/internal/proxy"
)

type proxyConfig struct {
	Listen      string `yaml:"listen"`
	Upstream    string `yaml:"upstream"`
	Profile     string `yaml:"profile"`
	MetricsAddr string `yaml:"metrics_addr"`
	LogLevel    string `yaml:"log_level"`
}

func defaults() proxyConfig {
	return proxyConfig{
		Listen:      "127.0.0.1:4443",
		Upstream:    "127.0.0.1:4444",
		Profile:     "passthrough",
		MetricsAddr: "127.0.0.1:9104",
		LogLevel:    "info",
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
	profile := fs.String("profile", def.Profile, "network profile: passthrough|wifi-good|cellular-lte|cellular-3g|satellite|flaky or a profile YAML path")
	metricsAddr := fs.String("metrics-addr", def.MetricsAddr, "Prometheus metrics listen address")
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
		case "metrics-addr":
			cfg.MetricsAddr = *metricsAddr
		case "log-level":
			cfg.LogLevel = *logLevel
		}
	})

	profileConfig, profileSource, err := loadProfile(cfg.Profile)
	if err != nil {
		return err
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

	proxyMetrics := metrics.NewProxy()
	p, err := proxy.New(proxy.Options{
		ListenConn:   listenConn,
		UpstreamAddr: upstreamAddr,
		Logger:       log,
		Profile:      profileConfig,
		Metrics:      proxyMetrics,
	})
	if err != nil {
		_ = listenConn.Close()
		return err
	}
	defer p.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serveMetrics(ctx, cfg.MetricsAddr, proxyMetrics.Registry, log); err != nil {
		_ = p.Close()
		return err
	}

	log.Info("proxy listening",
		"listen", listenConn.LocalAddr().String(),
		"upstream", upstreamAddr.String(),
		"profile", profileConfig.Name,
		"profile_source", profileSource,
		"profile_seed", profileConfig.Seed,
		"metrics_addr", cfg.MetricsAddr,
		"idle_timeout", (5 * time.Minute).String(),
	)
	return p.Serve(ctx)
}

func loadProfile(raw string) (impair.Profile, string, error) {
	if raw == "" {
		raw = "passthrough"
	}
	if profile, ok := impair.BuiltinProfile(raw); ok {
		return profile, "builtin:" + raw, nil
	}

	path := resolveProfilePath(raw)
	var profile impair.Profile
	if err := config.LoadFile(path, &profile); err != nil {
		return impair.Profile{}, "", fmt.Errorf("proxy: load profile %q: %w", raw, err)
	}
	normalized, err := impair.NormalizeProfile(profile)
	if err != nil {
		return impair.Profile{}, "", fmt.Errorf("proxy: validate profile %q: %w", raw, err)
	}
	if normalized.Name == "custom" {
		normalized.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return normalized, path, nil
}

func resolveProfilePath(raw string) string {
	if strings.ContainsRune(raw, filepath.Separator) || strings.HasSuffix(raw, ".yaml") || strings.HasSuffix(raw, ".yml") {
		return raw
	}
	return filepath.Join("configs", "proxy-"+raw+".yaml")
}

func serveMetrics(ctx context.Context, addr string, reg *prometheus.Registry, log *slog.Logger) error {
	if addr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("proxy: listen metrics %q: %w", addr, err)
	}
	srv := &http.Server{
		Handler: metrics.Handler(reg),
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Error("proxy metrics server failed", "addr", addr, "error", err)
		}
	}()
	return nil
}
