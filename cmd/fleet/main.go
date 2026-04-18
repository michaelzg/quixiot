package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"quixiot/internal/logging"
)

type fleetConfig struct {
	ClientBin       string
	ServerURL       string
	CAFile          string
	Role            string
	Count           int
	StaggerMS       int
	MetricsPortBase int
	MetricsHost     string
	TargetsFile     string
	LogLevel        string
}

func defaults() fleetConfig {
	return fleetConfig{
		ClientBin:       defaultClientBinary(),
		ServerURL:       "https://localhost:4444",
		CAFile:          "var/certs/ca.pem",
		Role:            "mixed",
		Count:           10,
		StaggerMS:       250,
		MetricsPortBase: 9200,
		MetricsHost:     "127.0.0.1",
		TargetsFile:     "deploy/targets/clients.json",
		LogLevel:        "info",
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("fleet", flag.ContinueOnError)
	def := defaults()

	clientBin := fs.String("client-bin", def.ClientBin, "path to the client binary to spawn")
	serverURL := fs.String("server-url", def.ServerURL, "HTTP/3 server base URL")
	caFile := fs.String("ca-file", def.CAFile, "CA certificate PEM path")
	role := fs.String("role", def.Role, "client role to launch for every child")
	count := fs.Int("count", def.Count, "number of child clients to spawn")
	staggerMS := fs.Int("stagger-ms", def.StaggerMS, "delay between child launches in milliseconds")
	metricsPortBase := fs.Int("metrics-port-base", def.MetricsPortBase, "starting TCP port for per-child Prometheus metrics (0 disables)")
	metricsHost := fs.String("metrics-host", def.MetricsHost, "host that Prometheus uses to reach child clients (host.docker.internal when scraping from a container)")
	targetsFile := fs.String("targets-file", def.TargetsFile, "Prometheus file_sd target list to write (empty disables)")
	logLevel := fs.String("log-level", def.LogLevel, "log level: debug|info|warn|error")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *count <= 0 {
		return fmt.Errorf("fleet: count must be positive")
	}
	if *staggerMS < 0 {
		return fmt.Errorf("fleet: stagger must be non-negative")
	}
	if *metricsPortBase < 0 || *metricsPortBase > 65535 {
		return fmt.Errorf("fleet: metrics-port-base must be 0..65535")
	}
	stagger := time.Duration(*staggerMS) * time.Millisecond

	log, err := logging.New(logging.Options{Level: *logLevel})
	if err != nil {
		return err
	}
	logging.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Compose per-child metrics addresses up-front so we can publish a Prometheus
	// targets file before any client comes up. This way the Grafana dashboard
	// shows real per-client breakdown the moment the fleet starts.
	children := make([]child, *count)
	for i := 0; i < *count; i++ {
		c := child{ID: fmt.Sprintf("fleet-%03d", i)}
		if *metricsPortBase > 0 {
			port := *metricsPortBase + i
			c.MetricsAddr = fmt.Sprintf("127.0.0.1:%d", port)
			c.ScrapeAddr = fmt.Sprintf("%s:%d", *metricsHost, port)
		}
		children[i] = c
	}

	if err := writeTargetsFile(*targetsFile, children); err != nil {
		return err
	}
	defer func() {
		// Rewrite to an empty list rather than deleting so Prometheus's
		// file_sd loader stays happy and the historical scrape data remains
		// queryable in Grafana after a fleet exits.
		if *targetsFile != "" {
			_ = writeTargetsFile(*targetsFile, nil)
		}
	}()

	cmds := make([]*exec.Cmd, 0, *count)
	errCh := make(chan error, *count)
	for i, c := range children {
		args := []string{
			"--server-url", *serverURL,
			"--ca-file", *caFile,
			"--client-id", c.ID,
			"--role", *role,
			"--metrics-addr", c.MetricsAddr,
			"--log-level", *logLevel,
		}
		cmd := exec.CommandContext(ctx, *clientBin, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			stop()
			return fmt.Errorf("fleet: start %s: %w", c.ID, err)
		}
		log.Info("started client",
			"client_id", c.ID,
			"pid", cmd.Process.Pid,
			"role", *role,
			"metrics_addr", c.MetricsAddr,
		)
		cmds = append(cmds, cmd)

		go func(id string, cmd *exec.Cmd) {
			if err := cmd.Wait(); err != nil && ctx.Err() == nil {
				errCh <- fmt.Errorf("fleet: child %s exited: %w", id, err)
			}
		}(c.ID, cmd)

		if i < *count-1 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(stagger):
			}
		}
	}

	log.Info("fleet active",
		"count", *count,
		"role", *role,
		"stagger", stagger.String(),
		"metrics_port_base", *metricsPortBase,
		"targets_file", *targetsFile,
	)
	select {
	case <-ctx.Done():
		for _, cmd := range cmds {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
		}
		return nil
	case err := <-errCh:
		stop()
		return err
	}
}

// child is one spawned client; ScrapeAddr is the host:port a remote Prometheus
// uses to scrape it (may differ from MetricsAddr when scraping from a Docker
// container, where 127.0.0.1 needs to become host.docker.internal).
type child struct {
	ID          string
	MetricsAddr string // bound on 127.0.0.1 (the listener)
	ScrapeAddr  string // what Prometheus uses to reach it
}

// writeTargetsFile emits a Prometheus file_sd_configs target list pointing at
// each child's metrics endpoint. The file is rewritten every fleet start so a
// running Prometheus picks up changes via its refresh_interval. Empty path or
// metrics-port-base=0 disables this side-effect.
func writeTargetsFile(path string, children []child) error {
	if path == "" {
		return nil
	}
	type target struct {
		Targets []string          `json:"targets"`
		Labels  map[string]string `json:"labels"`
	}
	var entries []target
	for _, c := range children {
		if c.ScrapeAddr == "" {
			continue
		}
		entries = append(entries, target{
			Targets: []string{c.ScrapeAddr},
			Labels: map[string]string{
				"job":       "quixiot-client",
				"client_id": c.ID,
			},
		})
	}
	if len(entries) == 0 {
		// Still rewrite as an empty list so stale entries get cleared.
		entries = []target{}
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("fleet: marshal targets: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("fleet: mkdir targets dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("fleet: write targets tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("fleet: rename targets: %w", err)
	}
	return nil
}

func defaultClientBinary() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "client")
	}
	return filepath.Join("bin", "client")
}
