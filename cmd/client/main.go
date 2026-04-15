package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"quixiot/internal/client"
	"quixiot/internal/config"
	"quixiot/internal/logging"
	"quixiot/internal/roles"
)

type clientConfig struct {
	ServerURL         string        `yaml:"server_url"`
	CAFile            string        `yaml:"ca_file"`
	ClientID          string        `yaml:"client_id"`
	Role              string        `yaml:"role"`
	PollInterval      time.Duration `yaml:"poll_interval"`
	UploadInterval    time.Duration `yaml:"upload_interval"`
	UploadSize        int64         `yaml:"upload_size"`
	TelemetryInterval time.Duration `yaml:"telemetry_interval"`
	CommandInterval   time.Duration `yaml:"command_interval"`
	PubSubPayloadSize int           `yaml:"pubsub_payload_size"`
	TelemetryTopic    string        `yaml:"telemetry_topic"`
	CommandTopic      string        `yaml:"command_topic"`
	SubscribeTopics   string        `yaml:"subscribe_topics"`
	LogLevel          string        `yaml:"log_level"`
}

func defaults() clientConfig {
	return clientConfig{
		ServerURL:         "https://localhost:4444",
		CAFile:            "var/certs/ca.pem",
		ClientID:          "client-local",
		Role:              "poller",
		PollInterval:      5 * time.Second,
		UploadInterval:    30 * time.Second,
		UploadSize:        1 << 20,
		TelemetryInterval: 2 * time.Second,
		CommandInterval:   7 * time.Second,
		PubSubPayloadSize: 256,
		LogLevel:          "info",
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
	role := fs.String("role", def.Role, "client role: poller|uploader|publisher|subscriber")
	pollInterval := fs.Duration("poll-interval", def.PollInterval, "poll interval for role=poller")
	uploadInterval := fs.Duration("upload-interval", def.UploadInterval, "upload interval for role=uploader")
	uploadSize := fs.Int64("upload-size", def.UploadSize, "upload size in bytes for role=uploader")
	telemetryInterval := fs.Duration("telemetry-interval", def.TelemetryInterval, "telemetry publish interval for role=publisher")
	commandInterval := fs.Duration("command-interval", def.CommandInterval, "command publish interval for role=publisher")
	pubsubPayloadSize := fs.Int("pubsub-payload-size", def.PubSubPayloadSize, "pubsub payload size in bytes for role=publisher")
	telemetryTopic := fs.String("telemetry-topic", def.TelemetryTopic, "override telemetry topic for pubsub roles")
	commandTopic := fs.String("command-topic", def.CommandTopic, "override command topic for pubsub roles")
	subscribeTopics := fs.String("subscribe-topics", def.SubscribeTopics, "comma-separated topic list for role=subscriber (defaults to telemetry+command topics)")
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
		case "telemetry-interval":
			cfg.TelemetryInterval = *telemetryInterval
		case "command-interval":
			cfg.CommandInterval = *commandInterval
		case "pubsub-payload-size":
			cfg.PubSubPayloadSize = *pubsubPayloadSize
		case "telemetry-topic":
			cfg.TelemetryTopic = *telemetryTopic
		case "command-topic":
			cfg.CommandTopic = *commandTopic
		case "subscribe-topics":
			cfg.SubscribeTopics = *subscribeTopics
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
	case "publisher":
	case "subscriber":
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
	case "publisher":
		deviceCfg, err := c.GetConfig(ctx, cfg.ClientID)
		if err != nil {
			return fmt.Errorf("client: get config for publisher: %w", err)
		}
		ps, err := c.ConnectPubSub(ctx, cfg.ClientID)
		if err != nil {
			return err
		}
		defer ps.Close()

		telemetry := firstNonEmpty(cfg.TelemetryTopic, deviceCfg.TelemetryTopic)
		command := firstNonEmpty(cfg.CommandTopic, deviceCfg.CommandTopic)
		log.Info("starting publisher",
			"server_url", cfg.ServerURL,
			"client_id", cfg.ClientID,
			"telemetry_topic", telemetry,
			"command_topic", command,
			"telemetry_interval", cfg.TelemetryInterval.String(),
			"command_interval", cfg.CommandInterval.String(),
			"payload_size", cfg.PubSubPayloadSize,
		)
		publisher := roles.Publisher{
			Session:           ps,
			ClientID:          cfg.ClientID,
			TelemetryTopic:    telemetry,
			CommandTopic:      command,
			TelemetryInterval: cfg.TelemetryInterval,
			CommandInterval:   cfg.CommandInterval,
			PayloadSize:       cfg.PubSubPayloadSize,
			Logger:            log,
		}
		return publisher.Run(ctx)
	case "subscriber":
		deviceCfg, err := c.GetConfig(ctx, cfg.ClientID)
		if err != nil {
			return fmt.Errorf("client: get config for subscriber: %w", err)
		}
		ps, err := c.ConnectPubSub(ctx, cfg.ClientID)
		if err != nil {
			return err
		}
		defer ps.Close()

		topics := splitList(cfg.SubscribeTopics)
		if len(topics) == 0 {
			topics = []string{
				firstNonEmpty(cfg.TelemetryTopic, deviceCfg.TelemetryTopic),
				firstNonEmpty(cfg.CommandTopic, deviceCfg.CommandTopic),
			}
		}
		log.Info("starting subscriber",
			"server_url", cfg.ServerURL,
			"client_id", cfg.ClientID,
			"topics", topics,
		)
		subscriber := roles.Subscriber{
			Session: ps,
			Topics:  topics,
			Logger:  log,
		}
		return subscriber.Run(ctx)
	default:
		return fmt.Errorf("client: unsupported role %q", cfg.Role)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func splitList(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
