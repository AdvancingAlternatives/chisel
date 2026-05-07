package coordinator

import (
	"net/http"
	"strings"
	"time"
)

// Client is the HTTP client for AdvancingAlternatives's bastion coordinator.
// Holds a configured *http.Client (mTLS + per-call timeout) and the base
// URL. All methods take a context.Context for cancellation propagation.
type Client struct {
	baseURL string
	http    *http.Client
	timeout time.Duration
}

// New builds a Client from cfg, validating URL + loading mTLS material.
// Errors out before any HTTP flight if cfg is invalid.
func New(cfg *Config) (*Client, error) {
	u, err := cfg.ParseURL()
	if err != nil {
		return nil, err
	}
	tlsCfg, err := cfg.LoadMTLS()
	if err != nil {
		return nil, err
	}

	timeout := cfg.timeoutOrDefault()
	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
		},
	}

	return &Client{
		baseURL: strings.TrimRight(u.String(), "/"),
		http:    httpClient,
		timeout: timeout,
	}, nil
}
