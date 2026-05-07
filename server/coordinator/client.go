package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// Lookup queries the coordinator for a pending session matching hostname.
//
// On HTTP 200 returns the parsed session.
// On HTTP 404 returns (nil, err) with err wrapping ErrNotFound — the
// expected "no pending session" path.
// On HTTP 401/403 returns (nil, err) wrapping ErrAuth (Layer-2 cert bug).
// On HTTP 5xx or transport failure returns (nil, err) wrapping ErrTransient.
func (c *Client) Lookup(ctx context.Context, hostname string) (*Session, error) {
	q := url.Values{}
	q.Set("target_hostname", hostname)
	reqURL := c.baseURL + PathLookup + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Lookup: %w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		var s Session
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			return nil, fmt.Errorf("Lookup: decode: %w", err)
		}
		return &s, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("Lookup: %w", ErrNotFound)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Lookup: %w: %s", ErrAuth, string(body))
	case resp.StatusCode >= 500:
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Lookup: %w: %d %s", ErrTransient, resp.StatusCode, string(body))
	default:
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Lookup: unexpected status %d: %s", resp.StatusCode, string(body))
	}
}
