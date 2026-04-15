package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"quixiot/internal/tlsutil"
)

const (
	defaultMaxIdleTimeout             = 45 * time.Second
	defaultKeepAlivePeriod            = 15 * time.Second
	defaultInitialStreamReceiveWindow = 512 * 1024
	defaultMaxStreamReceiveWindow     = 6 * 1024 * 1024
	defaultInitialConnReceiveWindow   = 1024 * 1024
	defaultMaxConnReceiveWindow       = 15 * 1024 * 1024
)

type Options struct {
	BaseURL string
	CAFile  string
	Logger  *slog.Logger
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	transport  *http3.Transport
	logger     *slog.Logger
}

type State struct {
	Version      string    `json:"version"`
	StartedAt    time.Time `json:"started_at"`
	UptimeMillis int64     `json:"uptime_millis"`
}

type DeviceConfig struct {
	ClientID            string `json:"client_id"`
	DesiredRole         string `json:"desired_role"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	TelemetryTopic      string `json:"telemetry_topic"`
	CommandTopic        string `json:"command_topic"`
}

func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("client: base URL is required")
	}
	if opts.CAFile == "" {
		return nil, fmt.Errorf("client: CA file is required")
	}

	baseURL, err := normalizeBaseURL(opts.BaseURL)
	if err != nil {
		return nil, err
	}
	tlsConf, err := tlsutil.LoadClientTrust(opts.CAFile)
	if err != nil {
		return nil, err
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	transport := &http3.Transport{
		TLSClientConfig: tlsConf,
		QUICConfig:      quicConfig(),
		EnableDatagrams: true,
		Logger:          log,
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Transport: transport},
		transport:  transport,
		logger:     log,
	}, nil
}

func (c *Client) GetState(ctx context.Context) (State, error) {
	var out State
	if err := c.getJSON(ctx, "/state", &out); err != nil {
		return State{}, err
	}
	return out, nil
}

func (c *Client) GetConfig(ctx context.Context, clientID string) (DeviceConfig, error) {
	var out DeviceConfig
	if clientID == "" {
		return DeviceConfig{}, fmt.Errorf("client: client ID is required")
	}
	path := fmt.Sprintf("/config/%s", url.PathEscape(clientID))
	if err := c.getJSON(ctx, path, &out); err != nil {
		return DeviceConfig{}, err
	}
	return out, nil
}

func (c *Client) Close() error {
	if c.transport == nil {
		return nil
	}
	return c.transport.Close()
}

func quicConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:                 defaultMaxIdleTimeout,
		KeepAlivePeriod:                defaultKeepAlivePeriod,
		InitialStreamReceiveWindow:     defaultInitialStreamReceiveWindow,
		MaxStreamReceiveWindow:         defaultMaxStreamReceiveWindow,
		InitialConnectionReceiveWindow: defaultInitialConnReceiveWindow,
		MaxConnectionReceiveWindow:     defaultMaxConnReceiveWindow,
		EnableDatagrams:                true,
		DisablePathMTUDiscovery:        true,
	}
}

func normalizeBaseURL(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("client: parse base URL %q: %w", raw, err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("client: base URL must use https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("client: base URL missing host")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (c *Client) getJSON(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("client: build GET %s: %w", path, err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("client: GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("client: GET %s: unexpected status %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	dec := json.NewDecoder(resp.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("client: decode %s: %w", path, err)
	}
	return nil
}
