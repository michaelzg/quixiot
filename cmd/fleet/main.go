package main

import (
	"context"
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
	ClientBin string
	ServerURL string
	CAFile    string
	Role      string
	Count     int
	StaggerMS int
	LogLevel  string
}

func defaults() fleetConfig {
	return fleetConfig{
		ClientBin: defaultClientBinary(),
		ServerURL: "https://localhost:4444",
		CAFile:    "var/certs/ca.pem",
		Role:      "mixed",
		Count:     10,
		StaggerMS: 250,
		LogLevel:  "info",
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
	stagger := time.Duration(*staggerMS) * time.Millisecond

	log, err := logging.New(logging.Options{Level: *logLevel})
	if err != nil {
		return err
	}
	logging.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmds := make([]*exec.Cmd, 0, *count)
	errCh := make(chan error, *count)
	for i := 0; i < *count; i++ {
		clientID := fmt.Sprintf("fleet-%03d", i)
		cmd := exec.CommandContext(ctx, *clientBin,
			"--server-url", *serverURL,
			"--ca-file", *caFile,
			"--client-id", clientID,
			"--role", *role,
			"--metrics-addr", "",
			"--log-level", *logLevel,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			stop()
			return fmt.Errorf("fleet: start %s: %w", clientID, err)
		}
		log.Info("started client", "client_id", clientID, "pid", cmd.Process.Pid, "role", *role)
		cmds = append(cmds, cmd)

		go func(clientID string, cmd *exec.Cmd) {
			if err := cmd.Wait(); err != nil && ctx.Err() == nil {
				errCh <- fmt.Errorf("fleet: child %s exited: %w", clientID, err)
			}
		}(clientID, cmd)

		if i < *count-1 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(stagger):
			}
		}
	}

	log.Info("fleet active", "count", *count, "role", *role, "stagger", stagger.String())
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

func defaultClientBinary() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "client")
	}
	return filepath.Join("bin", "client")
}
