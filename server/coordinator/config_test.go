package coordinator

import (
	"strings"
	"testing"
	"time"
)

func TestParseURLAcceptsValidHTTPS(t *testing.T) {
	cfg := &Config{URL: "https://coordinator:8443"}
	u, err := cfg.ParseURL()
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	if u.Scheme != "https" {
		t.Errorf("Scheme = %q", u.Scheme)
	}
	if u.Host != "coordinator:8443" {
		t.Errorf("Host = %q", u.Host)
	}
}

func TestParseURLRejectsHTTP(t *testing.T) {
	cfg := &Config{URL: "http://coordinator:8443"}
	_, err := cfg.ParseURL()
	if err == nil {
		t.Fatal("ParseURL accepted http://, want error")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("err = %q, want it to mention https", err)
	}
}

func TestParseURLRejectsEmptyHost(t *testing.T) {
	cfg := &Config{URL: "https:///path"}
	_, err := cfg.ParseURL()
	if err == nil {
		t.Fatal("ParseURL accepted empty host, want error")
	}
}

func TestParseURLRejectsMalformed(t *testing.T) {
	cfg := &Config{URL: "://broken"}
	_, err := cfg.ParseURL()
	if err == nil {
		t.Fatal("ParseURL accepted malformed URL, want error")
	}
}

func TestConfigDefaultTimeout(t *testing.T) {
	cfg := &Config{URL: "https://x:8443"}
	if cfg.TimeoutOrDefault() != 5*time.Second {
		t.Errorf("default timeout = %v, want 5s", cfg.TimeoutOrDefault())
	}

	cfg.Timeout = 12 * time.Second
	if cfg.TimeoutOrDefault() != 12*time.Second {
		t.Errorf("explicit timeout = %v, want 12s", cfg.TimeoutOrDefault())
	}
}
