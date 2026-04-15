// Package tlsutil generates a self-signed local CA + server leaf for the PoC and
// provides loaders for server-side and client-side *tls.Config values.
//
// The generator writes four files to outDir: ca.pem, ca.key, server.pem, server.key.
// Keys are ECDSA P-256. The CA is valid for up to 10 years; the server leaf for
// whatever validFor the caller passes.
//
// The client loader trusts only the provided CA — do not use InsecureSkipVerify.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// File names inside the output directory.
const (
	CAFile        = "ca.pem"
	CAKeyFile     = "ca.key"
	ServerFile    = "server.pem"
	ServerKeyFile = "server.key"
)

// ALPN is the protocol identifier negotiated on all quixiot TLS connections.
const ALPN = "h3"

// Paths bundles the file paths emitted by GenerateLocal.
type Paths struct {
	CA        string
	CAKey     string
	Server    string
	ServerKey string
}

// PathsIn returns the file paths that GenerateLocal would write inside outDir.
func PathsIn(outDir string) Paths {
	return Paths{
		CA:        filepath.Join(outDir, CAFile),
		CAKey:     filepath.Join(outDir, CAKeyFile),
		Server:    filepath.Join(outDir, ServerFile),
		ServerKey: filepath.Join(outDir, ServerKeyFile),
	}
}

// GenerateLocal creates a self-signed ECDSA P-256 CA and a server leaf signed by
// it. SAN entries that parse as IPs are added as IP SANs, everything else as DNS
// SANs. validFor controls the server leaf validity; the CA validity is 10× that,
// capped at 10 years.
func GenerateLocal(outDir string, sans []string, validFor time.Duration) (Paths, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return Paths{}, fmt.Errorf("tlsutil: mkdir %s: %w", outDir, err)
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Paths{}, fmt.Errorf("tlsutil: ca key: %w", err)
	}

	caSerial, err := randomSerial()
	if err != nil {
		return Paths{}, err
	}

	caValid := 10 * validFor
	if max := 10 * 365 * 24 * time.Hour; caValid > max {
		caValid = max
	}

	now := time.Now()
	caTemplate := x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "quixiot-local-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValid),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, caKey.Public(), caKey)
	if err != nil {
		return Paths{}, fmt.Errorf("tlsutil: sign ca: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return Paths{}, fmt.Errorf("tlsutil: parse ca: %w", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Paths{}, fmt.Errorf("tlsutil: leaf key: %w", err)
	}
	leafSerial, err := randomSerial()
	if err != nil {
		return Paths{}, err
	}

	var dnsNames []string
	var ipAddrs []net.IP
	for _, s := range sans {
		if ip := net.ParseIP(s); ip != nil {
			ipAddrs = append(ipAddrs, ip)
		} else {
			dnsNames = append(dnsNames, s)
		}
	}

	leafTemplate := x509.Certificate{
		SerialNumber:          leafSerial,
		Subject:               pkix.Name{CommonName: "quixiot-server"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddrs,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &leafTemplate, caCert, leafKey.Public(), caKey)
	if err != nil {
		return Paths{}, fmt.Errorf("tlsutil: sign leaf: %w", err)
	}

	paths := PathsIn(outDir)
	if err := writeCertPEM(paths.CA, caDER, 0o644); err != nil {
		return Paths{}, err
	}
	if err := writeECKeyPEM(paths.CAKey, caKey, 0o600); err != nil {
		return Paths{}, err
	}
	if err := writeCertPEM(paths.Server, leafDER, 0o644); err != nil {
		return Paths{}, err
	}
	if err := writeECKeyPEM(paths.ServerKey, leafKey, 0o600); err != nil {
		return Paths{}, err
	}
	return paths, nil
}

// LoadServerTLS returns a *tls.Config suitable for an HTTP/3 server: TLS 1.3 only,
// ALPN "h3", and the given leaf pair loaded.
func LoadServerTLS(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: load server pair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{ALPN},
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// LoadClientTrust returns a *tls.Config that trusts only the CA at caFile.
// ALPN is "h3"; TLS 1.3 is enforced; InsecureSkipVerify stays off.
func LoadClientTrust(caFile string) (*tls.Config, error) {
	data, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: read ca %s: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("tlsutil: no PEM certificates in %s", caFile)
	}
	return &tls.Config{
		RootCAs:    pool,
		NextProtos: []string{ALPN},
		MinVersion: tls.VersionTLS13,
	}, nil
}

func randomSerial() (*big.Int, error) {
	// 62 bits is plenty for a local CA — well under the 20-byte RFC 5280 cap.
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, fmt.Errorf("tlsutil: serial: %w", err)
	}
	return n, nil
}

func writeCertPEM(path string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("tlsutil: open %s: %w", path, err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		return fmt.Errorf("tlsutil: write %s: %w", path, err)
	}
	return nil
}

func writeECKeyPEM(path string, key *ecdsa.PrivateKey, mode os.FileMode) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("tlsutil: marshal ec key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("tlsutil: open %s: %w", path, err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}); err != nil {
		return fmt.Errorf("tlsutil: write %s: %w", path, err)
	}
	return nil
}
