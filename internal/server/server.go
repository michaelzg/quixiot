package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"quixiot/internal/logging"
)

const (
	defaultVersion                    = "dev"
	defaultPollIntervalSeconds        = 5
	defaultMaxIdleTimeout             = 45 * time.Second
	defaultInitialStreamReceiveWindow = 512 * 1024
	defaultMaxStreamReceiveWindow     = 6 * 1024 * 1024
	defaultInitialConnReceiveWindow   = 1024 * 1024
	defaultMaxConnReceiveWindow       = 15 * 1024 * 1024
)

type Options struct {
	PacketConn net.PacketConn
	TLSConfig  *tls.Config
	Logger     *slog.Logger
	Version    string
	StartedAt  time.Time
}

type Server struct {
	packetConn net.PacketConn
	transport  *quic.Transport
	listener   *quic.EarlyListener
	http3      *http3.Server
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
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", s.handleState)
	mux.HandleFunc("GET /config/{clientID}", s.handleConfig)

	s.transport = &quic.Transport{Conn: opts.PacketConn}
	listener, err := s.transport.ListenEarly(http3.ConfigureTLSConfig(opts.TLSConfig), quicConfig())
	if err != nil {
		_ = s.transport.Close()
		return nil, fmt.Errorf("server: listen early: %w", err)
	}
	s.listener = listener
	s.http3 = &http3.Server{
		Handler:         mux,
		QUICConfig:      quicConfig(),
		EnableDatagrams: true,
		Logger:          log,
	}
	return s, nil
}

func (s *Server) Addr() net.Addr {
	return s.packetConn.LocalAddr()
}

func (s *Server) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.http3.ServeListener(s.listener)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		_ = s.Close()
		<-errCh
		return nil
	}
}

func (s *Server) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		if s.http3 != nil {
			if err := s.http3.Close(); err != nil && closeErr == nil {
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
		MaxIdleTimeout:                 defaultMaxIdleTimeout,
		InitialStreamReceiveWindow:     defaultInitialStreamReceiveWindow,
		MaxStreamReceiveWindow:         defaultMaxStreamReceiveWindow,
		InitialConnectionReceiveWindow: defaultInitialConnReceiveWindow,
		MaxConnectionReceiveWindow:     defaultMaxConnReceiveWindow,
		EnableDatagrams:                true,
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
