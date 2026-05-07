package coordinator

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"time"
)

// Config holds the runtime configuration for a coordinator.Client.
// Populated from CLI flags (see main.go) or test setup.
type Config struct {
	// URL is the coordinator's base URL (e.g., https://coordinator:8443).
	// Must be https; the path conventions PathLookup / PathActivate /
	// PathDeactivate are appended at call time.
	URL string

	// MTLSCertFile is the path to chisel-server.pem (this fork's
	// client cert presented to the coordinator). Required when URL is set.
	MTLSCertFile string

	// MTLSKeyFile is the path to chisel-server-key.pem matching CertFile.
	MTLSKeyFile string

	// MTLSCAFile is the path to ca.pem used to verify the coordinator's
	// server cert. Same CA that signed the client cert.
	MTLSCAFile string

	// Timeout is the per-call HTTP timeout. Default 5 seconds (anything
	// slower is a coordinator-sick signal — fail fast and let the LCM
	// retry rather than block the operator's SSH attempt).
	Timeout time.Duration
}

// ParseURL validates URL and returns the parsed *url.URL. Called at
// flag-parse time so misconfigs fail bastion-chisel startup rather than
// first-LCM-connection.
func (c *Config) ParseURL() (*url.URL, error) {
	if c.URL == "" {
		return nil, fmt.Errorf("coordinator URL is empty")
	}
	u, err := url.Parse(c.URL)
	if err != nil {
		return nil, fmt.Errorf("parse url %q: %w", c.URL, err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("coordinator URL must be https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("coordinator URL has empty host: %q", c.URL)
	}
	return u, nil
}

// TimeoutOrDefault returns Timeout if set, otherwise the 5s default.
func (c *Config) TimeoutOrDefault() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 5 * time.Second
}

// LoadMTLS reads the cert/key/ca files from disk and returns a *tls.Config
// wired for client mTLS to the coordinator. Errors out cleanly on missing
// files, malformed PEM, or empty path fields. Called once at chisel-server
// startup; the returned *tls.Config is reused across all coordinator HTTP
// calls.
func (c *Config) LoadMTLS() (*tls.Config, error) {
	if c.MTLSCertFile == "" || c.MTLSKeyFile == "" || c.MTLSCAFile == "" {
		return nil, fmt.Errorf("mTLS cert/key/ca paths must all be set")
	}

	cert, err := tls.LoadX509KeyPair(c.MTLSCertFile, c.MTLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load cert/key: %w", err)
	}

	caPEM, err := os.ReadFile(c.MTLSCAFile)
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("CA file %q has no valid PEM certs", c.MTLSCAFile)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
