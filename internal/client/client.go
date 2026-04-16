package client

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"quixiot/internal/metrics"
	"quixiot/internal/tlsutil"
)

const (
	defaultMaxIdleTimeout             = 45 * time.Second
	defaultKeepAlivePeriod            = 15 * time.Second
	defaultInitialStreamReceiveWindow = 512 * 1024
	defaultMaxStreamReceiveWindow     = 8 * 1024 * 1024
	defaultInitialConnReceiveWindow   = 1024 * 1024
	defaultMaxConnReceiveWindow       = 16 * 1024 * 1024
)

type Options struct {
	BaseURL string
	CAFile  string
	Logger  *slog.Logger
	Metrics *metrics.ClientMetrics
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	transport  *http3.Transport
	tlsConfig  *tls.Config
	metrics    *metrics.ClientMetrics
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

type UploadResult struct {
	Bytes          int64  `json:"bytes"`
	SHA256         string `json:"sha256"`
	DurationMillis int64  `json:"durationMs"`
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

	c := &Client{
		baseURL:   baseURL,
		tlsConfig: tlsConf,
		metrics:   opts.Metrics,
		logger:    log,
	}
	transport := &http3.Transport{
		TLSClientConfig: tlsConf,
		QUICConfig:      quicConfig(),
		EnableDatagrams: true,
		Logger:          log,
		Dial:            c.dialQUIC,
	}
	c.httpClient = &http.Client{Transport: transport}
	c.transport = transport
	return c, nil
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

func (c *Client) UploadDeterministic(ctx context.Context, name string, size int64, seed int64) (UploadResult, error) {
	if name == "" {
		return UploadResult{}, fmt.Errorf("client: upload name is required")
	}
	if size < 0 {
		return UploadResult{}, fmt.Errorf("client: upload size must be non-negative")
	}

	expectedSHA, err := deterministicSHA256(size, seed)
	if err != nil {
		return UploadResult{}, err
	}

	path := fmt.Sprintf("/files/%s", url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, io.NopCloser(newDeterministicReader(size, seed)))
	if err != nil {
		return UploadResult{}, fmt.Errorf("client: build POST %s: %w", path, err)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return UploadResult{}, fmt.Errorf("client: POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return UploadResult{}, fmt.Errorf("client: POST %s: unexpected status %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}

	var out UploadResult
	dec := json.NewDecoder(resp.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return UploadResult{}, fmt.Errorf("client: decode %s: %w", path, err)
	}
	if out.Bytes != size {
		return UploadResult{}, fmt.Errorf("client: upload bytes mismatch: want %d got %d", size, out.Bytes)
	}
	if out.SHA256 != expectedSHA {
		return UploadResult{}, fmt.Errorf("client: upload sha mismatch: want %s got %s", expectedSHA, out.SHA256)
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
		MaxIdleTimeout:                   defaultMaxIdleTimeout,
		KeepAlivePeriod:                  defaultKeepAlivePeriod,
		InitialStreamReceiveWindow:       defaultInitialStreamReceiveWindow,
		MaxStreamReceiveWindow:           defaultMaxStreamReceiveWindow,
		InitialConnectionReceiveWindow:   defaultInitialConnReceiveWindow,
		MaxConnectionReceiveWindow:       defaultMaxConnReceiveWindow,
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
		DisablePathMTUDiscovery:          true,
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

type deterministicReader struct {
	remaining int64
	rnd       *rand.Rand
}

func newDeterministicReader(size int64, seed int64) *deterministicReader {
	return &deterministicReader{
		remaining: size,
		rnd:       rand.New(rand.NewSource(seed)),
	}
}

func (r *deterministicReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.rnd.Read(p)
	r.remaining -= int64(n)
	if err != nil {
		return n, err
	}
	if r.remaining == 0 {
		return n, io.EOF
	}
	return n, nil
}

func deterministicSHA256(size int64, seed int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, newDeterministicReader(size, seed)); err != nil {
		return "", fmt.Errorf("client: hash deterministic upload: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c *Client) dialQUIC(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
	start := time.Now()
	conn, err := quic.DialAddrEarly(ctx, addr, tlsCfg, cfg)
	if err != nil {
		return nil, err
	}
	if c.metrics != nil {
		go func() {
			select {
			case <-conn.HandshakeComplete():
				c.metrics.HandshakeDuration.Observe(time.Since(start).Seconds())
			case <-conn.Context().Done():
				return
			}

			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				stats := conn.ConnectionStats()
				c.metrics.RTT.Set(stats.SmoothedRTT.Seconds())
				select {
				case <-conn.Context().Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	return conn, nil
}
