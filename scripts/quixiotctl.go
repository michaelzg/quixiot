package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/quic-go/quic-go/http3"

	"quixiot/internal/tlsutil"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: quixiotctl <h3get|expected-upload-sha> [flags]")
	}

	switch args[0] {
	case "h3get":
		return runH3Get(args[1:])
	case "expected-upload-sha":
		return runExpectedUploadSHA(args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func runH3Get(args []string) error {
	fs := flag.NewFlagSet("h3get", flag.ContinueOnError)
	url := fs.String("url", "", "HTTP/3 URL to fetch")
	caFile := fs.String("ca-file", "", "CA certificate PEM path")
	timeout := fs.Duration("timeout", 5*time.Second, "request timeout")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *url == "" {
		return fmt.Errorf("h3get: --url is required")
	}
	if *caFile == "" {
		return fmt.Errorf("h3get: --ca-file is required")
	}

	tlsConf, err := tlsutil.LoadClientTrust(*caFile)
	if err != nil {
		return err
	}
	transport := &http3.Transport{
		TLSClientConfig: tlsConf,
	}
	defer transport.Close()

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   *timeout,
	}
	resp, err := httpClient.Get(*url)
	if err != nil {
		return fmt.Errorf("h3get: GET %s: %w", *url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("h3get: GET %s: unexpected status %s: %s", *url, resp.Status, strings.TrimSpace(string(body)))
	}
	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

func runExpectedUploadSHA(args []string) error {
	fs := flag.NewFlagSet("expected-upload-sha", flag.ContinueOnError)
	size := fs.Int64("size", 0, "upload size in bytes")
	seed := fs.Int64("seed", 0, "deterministic upload seed")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *size < 0 {
		return fmt.Errorf("expected-upload-sha: --size must be non-negative")
	}

	sum, err := deterministicSHA256(*size, *seed)
	if err != nil {
		return err
	}
	fmt.Println(sum)
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
		return "", fmt.Errorf("expected-upload-sha: hash deterministic payload: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
