package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"quixiot/internal/client"
	"quixiot/internal/config"
	"quixiot/internal/logging"
	"quixiot/internal/roles"
)

type clientConfig struct {
	ServerURL      string        `yaml:"server_url"`
	CAFile         string        `yaml:"ca_file"`
	ClientID       string        `yaml:"client_id"`
	Role           string        `yaml:"role"`
	PollInterval   time.Duration `yaml:"poll_interval"`
	UploadInterval time.Duration `yaml:"upload_interval"`
	UploadSize     int64         `yaml:"upload_size"`
	LogLevel       string        `yaml:"log_level"`
}

func defaults() clientConfig {
	return clientConfig{
		ServerURL:      "https://localhost:4444",
		CAFile:         "var/certs/ca.pem",
		ClientID:       "client-local",
		Role:           "poller",
		PollInterval:   5 * time.Second,
		UploadInterval: 30 * time.Second,
		UploadSize:     1 << 20,
		LogLevel:       "info",
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("client", flag.ContinueOnError)
	def := defaults()

	cfgPath := fs.String("config", "", "YAML config file (optional)")
	serverURL := fs.String("server-url", def.ServerURL, "HTTP/3 server base URL")
	caFile := fs.String("ca-file", def.CAFile, "CA certificate PEM path")
	clientID := fs.String("client-id", def.ClientID, "logical client ID")
	role := fs.String("role", def.Role, "client role: poller|uploader")
	pollInterval := fs.Duration("poll-interval", def.PollInterval, "poll interval for role=poller")
	uploadInterval := fs.Duration("upload-interval", def.UploadInterval, "upload interval for role=uploader")
	uploadSize := fs.Int64("upload-size", def.UploadSize, "upload size in bytes for role=uploader")
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
		case "server-url":
			cfg.ServerURL = *serverURL
		case "ca-file":
			cfg.CAFile = *caFile
		case "client-id":
			cfg.ClientID = *clientID
		case "role":
			cfg.Role = *role
		case "poll-interval":
			cfg.PollInterval = *pollInterval
		case "upload-interval":
			cfg.UploadInterval = *uploadInterval
		case "upload-size":
			cfg.UploadSize = *uploadSize
		case "log-level":
			cfg.LogLevel = *logLevel
		}
	})

	log, err := logging.New(logging.Options{Level: cfg.LogLevel})
	if err != nil {
		return err
	}
	logging.SetDefault(log)

	switch cfg.Role {
	case "poller":
	case "uploader":
	default:
		return fmt.Errorf("client: unsupported role %q", cfg.Role)
	}

	c, err := client.New(client.Options{
		BaseURL: cfg.ServerURL,
		CAFile:  cfg.CAFile,
		Logger:  log,
	})
	if err != nil {
		return err
	}
	defer c.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cfg.Role {
	case "poller":
		log.Info("starting poller",
			"server_url", cfg.ServerURL,
			"client_id", cfg.ClientID,
			"poll_interval", cfg.PollInterval.String(),
		)
		poller := roles.Poller{
			Client:   c,
			ClientID: cfg.ClientID,
			Interval: cfg.PollInterval,
			Logger:   log,
		}
		return poller.Run(ctx)
	case "uploader":
		log.Info("starting uploader",
			"server_url", cfg.ServerURL,
			"client_id", cfg.ClientID,
			"upload_interval", cfg.UploadInterval.String(),
			"upload_size", cfg.UploadSize,
		)
		uploader := roles.Uploader{
			Client:   c,
			ClientID: cfg.ClientID,
			Interval: cfg.UploadInterval,
			Size:     cfg.UploadSize,
			Logger:   log,
		}
		return uploader.Run(ctx)
	default:
		return fmt.Errorf("client: unsupported role %q", cfg.Role)
	}
}
