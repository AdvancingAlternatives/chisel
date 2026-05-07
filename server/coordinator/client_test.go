package coordinator

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"
)

func TestNewClientReturnsConfiguredClient(t *testing.T) {
	cert, key, ca := genTestCert(t, t.TempDir())
	cfg := &Config{
		URL:          "https://coord:8443",
		MTLSCertFile: cert,
		MTLSKeyFile:  key,
		MTLSCAFile:   ca,
		Timeout:      3 * time.Second,
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil")
	}
	if c.baseURL != "https://coord:8443" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
}

func TestNewClientRejectsBadURL(t *testing.T) {
	cfg := &Config{URL: "http://insecure"}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("New accepted http URL")
	}
}

func TestNewClientRejectsMissingMTLSFiles(t *testing.T) {
	cfg := &Config{URL: "https://coord:8443"}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("New accepted missing mTLS files")
	}
}

// httpRoundTripperWithTLS is a small helper: returns the *http.Client
// constructed by New so we can inspect its Transport's TLSClientConfig.
func transportTLS(c *Client) *tls.Config {
	if c.http == nil {
		return nil
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		return nil
	}
	return tr.TLSClientConfig
}

func TestNewClientUsesTLS13Minimum(t *testing.T) {
	cert, key, ca := genTestCert(t, t.TempDir())
	cfg := &Config{
		URL: "https://coord:8443", MTLSCertFile: cert, MTLSKeyFile: key, MTLSCAFile: ca,
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tlsCfg := transportTLS(c)
	if tlsCfg == nil {
		t.Fatal("Transport.TLSClientConfig is nil")
	}
	if tlsCfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %d, want TLS13 (%d)", tlsCfg.MinVersion, tls.VersionTLS13)
	}
}
