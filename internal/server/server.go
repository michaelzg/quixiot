package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"

	"quixiot/internal/broker"
	"quixiot/internal/logging"
	"quixiot/internal/metrics"
	"quixiot/internal/upload"
)

const (
	defaultVersion                    = "dev"
	defaultPollIntervalSeconds        = 5
	defaultMaxIdleTimeout             = 45 * time.Second
	defaultInitialStreamReceiveWindow = 512 * 1024
	defaultMaxStreamReceiveWindow     = 6 * 1024 * 1024
	defaultInitialConnReceiveWindow   = 1024 * 1024
	defaultMaxConnReceiveWindow       = 15 * 1024 * 1024
	pubsubProtocol                    = "quixiot-pubsub-v1"
)

type Options struct {
	PacketConn net.PacketConn
	TLSConfig  *tls.Config
	Logger     *slog.Logger
	Version    string
	StartedAt  time.Time
	UploadDir  string
	Metrics    *metrics.ServerMetrics
}

type Server struct {
	packetConn net.PacketConn
	transport  *quic.Transport
	listener   *quic.EarlyListener
	http3      *http3.Server
	wt         *webtransport.Server
	broker     *broker.Broker
	metrics    *metrics.ServerMetrics
	logger     *slog.Logger
	version    string
	startedAt  time.Time
	reqSeq     uint64
	closeOnce  sync.Once
}

type stateResponse struct {
	Version      string    `json:"version"`
	StartedAt    time.Time `json:"started_at"`
	UptimeMillis int64     `json:"uptime_millis"`
}

type configResponse struct {
	ClientID            string `json:"client_id"`
	DesiredRole         string `json:"desired_role"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	TelemetryTopic      string `json:"telemetry_topic"`
	CommandTopic        string `json:"command_topic"`
}

func New(opts Options) (*Server, error) {
	if opts.PacketConn == nil {
		return nil, fmt.Errorf("server: packet conn is required")
	}
	if opts.TLSConfig == nil {
		return nil, fmt.Errorf("server: TLS config is required")
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	uploadDir := opts.UploadDir
	if uploadDir == "" {
		uploadDir = "var/uploads"
	}
	version := opts.Version
	if version == "" {
		version = defaultVersion
	}
	startedAt := opts.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}

	s := &Server{
		packetConn: opts.PacketConn,
		logger:     log,
		version:    version,
		startedAt:  startedAt,
		metrics:    opts.Metrics,
	}
	s.broker = broker.New(log, s.metrics)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", s.handleState)
	mux.HandleFunc("GET /config/{clientID}", s.handleConfig)
	mux.Handle("POST /files/{name}", upload.Handler{
		Dir:    uploadDir,
		Logger: log,
		OnStored: func(resp upload.Response) {
			if s.metrics == nil {
				return
			}
			s.metrics.UploadBytes.Observe(float64(resp.Bytes))
			s.metrics.UploadDuration.Observe(float64(resp.DurationMillis) / 1000)
		},
	})
	mux.HandleFunc("CONNECT /pubsub", s.handlePubSub)
	if s.metrics != nil {
		mux.Handle("/metrics", metrics.Handler(s.metrics.Registry))
	}

	s.transport = &quic.Transport{Conn: opts.PacketConn}
	listener, err := s.transport.ListenEarly(http3.ConfigureTLSConfig(opts.TLSConfig), quicConfig())
	if err != nil {
		_ = s.transport.Close()
		return nil, fmt.Errorf("server: listen early: %w", err)
	}
	s.listener = listener
	s.http3 = &http3.Server{
		Handler:         s.instrumentHTTP(mux),
		QUICConfig:      quicConfig(),
		EnableDatagrams: true,
		Logger:          log,
	}
	webtransport.ConfigureHTTP3Server(s.http3)
	origConnContext := s.http3.ConnContext
	s.http3.ConnContext = func(ctx context.Context, conn *quic.Conn) context.Context {
		if origConnContext != nil {
			ctx = origConnContext(ctx, conn)
		}
		if s.metrics != nil {
			start := time.Now()
			s.metrics.ConnectionsActive.Inc()
			s.metrics.ConnectionsTotal.Inc()
			go func() {
				select {
				case <-conn.HandshakeComplete():
					s.metrics.HandshakeDuration.Observe(time.Since(start).Seconds())
				case <-conn.Context().Done():
				}
			}()
			context.AfterFunc(conn.Context(), func() {
				s.metrics.ConnectionsActive.Dec()
			})
		}
		return ctx
	}
	s.wt = &webtransport.Server{
		H3:                   s.http3,
		ApplicationProtocols: []string{pubsubProtocol},
	}
	return s, nil
}

func (s *Server) Addr() net.Addr {
	return s.packetConn.LocalAddr()
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()

	for {
		conn, err := s.listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("server: accept quic conn: %w", err)
		}

		go func(conn *quic.Conn) {
			if err := s.wt.ServeQUICConn(conn); err != nil && !errors.Is(err, net.ErrClosed) && ctx.Err() == nil {
				s.logger.Warn("serve webtransport conn failed", "remote", conn.RemoteAddr().String(), "error", err)
			}
		}(conn)
	}
}

func (s *Server) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		if s.wt != nil {
			if err := s.wt.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		if s.listener != nil {
			if err := s.listener.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		if s.transport != nil {
			if err := s.transport.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
	})
	return closeErr
}

func quicConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:                   defaultMaxIdleTimeout,
		InitialStreamReceiveWindow:       defaultInitialStreamReceiveWindow,
		MaxStreamReceiveWindow:           defaultMaxStreamReceiveWindow,
		InitialConnectionReceiveWindow:   defaultInitialConnReceiveWindow,
		MaxConnectionReceiveWindow:       defaultMaxConnReceiveWindow,
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	reqLog := s.requestLogger(r)
	resp := stateResponse{
		Version:      s.version,
		StartedAt:    s.startedAt,
		UptimeMillis: time.Since(s.startedAt).Milliseconds(),
	}
	if err := writeJSON(w, http.StatusOK, resp); err != nil {
		reqLog.Error("write state response", "error", err)
		return
	}
	reqLog.Info("served state", "uptime_millis", resp.UptimeMillis)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	clientID := r.PathValue("clientID")
	reqLog := s.requestLogger(r).With("client_id", clientID)
	resp := configResponse{
		ClientID:            clientID,
		DesiredRole:         "poller",
		PollIntervalSeconds: defaultPollIntervalSeconds,
		TelemetryTopic:      fmt.Sprintf("clients/%s/telemetry", clientID),
		CommandTopic:        fmt.Sprintf("clients/%s/commands", clientID),
	}
	if err := writeJSON(w, http.StatusOK, resp); err != nil {
		reqLog.Error("write config response", "error", err)
		return
	}
	reqLog.Info("served config")
}

func (s *Server) handlePubSub(w http.ResponseWriter, r *http.Request) {
	s.requestLogger(r).Info("upgrading pubsub session")
	s.broker.HandleRequest(w, r, s.wt)
}

func (s *Server) instrumentHTTP(next http.Handler) http.Handler {
	if s.metrics == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		path := routeLabel(r)
		s.metrics.HTTPDuration.WithLabelValues(r.Method, path, strconv.Itoa(rec.status)).Observe(time.Since(start).Seconds())
		if r.ContentLength > 0 {
			s.metrics.Bytes.WithLabelValues("h3", "in").Add(float64(r.ContentLength))
		}
		if rec.bytes > 0 {
			s.metrics.Bytes.WithLabelValues("h3", "out").Add(float64(rec.bytes))
		}
	})
}

func routeLabel(r *http.Request) string {
	if r == nil {
		return "/"
	}
	pattern := r.Pattern
	if idx := strings.IndexByte(pattern, ' '); idx >= 0 {
		pattern = pattern[idx+1:]
	}
	if pattern != "" {
		return pattern
	}
	if r.URL != nil && r.URL.Path != "" {
		return r.URL.Path
	}
	return "/"
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) ReceivedSettings() <-chan struct{} {
	if settingser, ok := r.ResponseWriter.(http3.Settingser); ok {
		return settingser.ReceivedSettings()
	}
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (r *statusRecorder) Settings() *http3.Settings {
	if settingser, ok := r.ResponseWriter.(http3.Settingser); ok {
		return settingser.Settings()
	}
	return nil
}

func (r *statusRecorder) HTTPStream() *http3.Stream {
	if streamer, ok := r.ResponseWriter.(http3.HTTPStreamer); ok {
		return streamer.HTTPStream()
	}
	return nil
}

func (s *Server) requestLogger(r *http.Request) *slog.Logger {
	reqID := strconv.FormatUint(atomic.AddUint64(&s.reqSeq, 1), 10)
	return logging.RequestAttrs(s.logger, r.Method, r.URL.Path, r.RemoteAddr, reqID)
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return fmt.Errorf("server: encode JSON: %w", err)
	}
	return nil
}
