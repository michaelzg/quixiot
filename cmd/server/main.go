// Package main is the quixiot HTTP/3 + WebTransport server binary.
//
// Phase 2 scope: flag parsing, logging, config loader, and the --gen-certs
// subcommand that produces a local CA + server leaf for the PoC. The QUIC
// listener itself lands in Phase 3.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"quixiot/internal/config"
	"quixiot/internal/logging"
	"quixiot/internal/server"
	"quixiot/internal/tlsutil"
)

const buildVersion = "dev"

type serverConfig struct {
	Addr      string `yaml:"addr"`
	CertFile  string `yaml:"cert_file"`
	KeyFile   string `yaml:"key_file"`
	CAFile    string `yaml:"ca_file"`
	UploadDir string `yaml:"upload_dir"`
	LogLevel  string `yaml:"log_level"`
}

func defaults() serverConfig {
	return serverConfig{
		Addr:      ":4444",
		CertFile:  "var/certs/server.pem",
		KeyFile:   "var/certs/server.key",
		CAFile:    "var/certs/ca.pem",
		UploadDir: "var/uploads",
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
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	def := defaults()

	cfgPath := fs.String("config", "", "YAML config file (optional)")
	addr := fs.String("addr", def.Addr, "listen address (host:port)")
	certFile := fs.String("cert-file", def.CertFile, "server leaf certificate PEM")
	keyFile := fs.String("key-file", def.KeyFile, "server leaf private key PEM")
	caFile := fs.String("ca-file", def.CAFile, "CA certificate PEM path")
	uploadDir := fs.String("upload-dir", def.UploadDir, "sandboxed upload directory")
	logLevel := fs.String("log-level", def.LogLevel, "log level: debug|info|warn|error")

	genCerts := fs.Bool("gen-certs", false, "generate local CA + server leaf into --cert-dir and exit")
	certDir := fs.String("cert-dir", "var/certs", "output directory for --gen-certs")
	validFor := fs.Duration("cert-valid-for", 365*24*time.Hour, "server leaf validity (CA gets 10x, capped 10y)")
	sans := fs.String("sans", "127.0.0.1,localhost,::1", "comma-separated SANs for --gen-certs")

	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := def
	if err := config.LoadFile(*cfgPath, &cfg); err != nil {
		return err
	}
	// Explicit flags win over YAML (flag.Visit only sees flags the user set).
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "addr":
			cfg.Addr = *addr
		case "cert-file":
			cfg.CertFile = *certFile
		case "key-file":
			cfg.KeyFile = *keyFile
		case "ca-file":
			cfg.CAFile = *caFile
		case "upload-dir":
			cfg.UploadDir = *uploadDir
		case "log-level":
			cfg.LogLevel = *logLevel
		}
	})

	log, err := logging.New(logging.Options{Level: cfg.LogLevel})
	if err != nil {
		return err
	}
	logging.SetDefault(log)

	if *genCerts {
		sanList := splitSANs(*sans)
		paths, err := tlsutil.GenerateLocal(*certDir, sanList, *validFor)
		if err != nil {
			return err
		}
		log.Info("generated certs",
			"dir", *certDir,
			"ca", paths.CA,
			"ca_key", paths.CAKey,
			"server", paths.Server,
			"server_key", paths.ServerKey,
			"sans", sanList,
			"valid_for", validFor.String(),
		)
		return nil
	}

	tlsConf, err := tlsutil.LoadServerTLS(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return err
	}
	pc, err := net.ListenPacket("udp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", cfg.Addr, err)
	}
	defer pc.Close()

	srv, err := server.New(server.Options{
		PacketConn: pc,
		TLSConfig:  tlsConf,
		Logger:     log,
		Version:    buildVersion,
		UploadDir:  cfg.UploadDir,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("server listening",
		"addr", pc.LocalAddr().String(),
		"cert_file", cfg.CertFile,
		"key_file", cfg.KeyFile,
		"upload_dir", cfg.UploadDir,
	)
	return srv.Serve(ctx)
}

func splitSANs(s string) []string {
	var out []string
	for _, tok := range strings.Split(s, ",") {
		if tok = strings.TrimSpace(tok); tok != "" {
			out = append(out, tok)
		}
	}
	return out
}
