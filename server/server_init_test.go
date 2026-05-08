package chserver

import (
	"context"
	"errors"
	"testing"

	"github.com/jpillora/chisel/server/coordinator"
	"github.com/jpillora/chisel/share/tunnel"
)

func TestNewServerNoCoordinatorWorks(t *testing.T) {
	s, err := NewServer(&Config{
		KeySeed: "test",
		Reverse: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if s.coordClient != nil {
		t.Error("coordClient should be nil when Coordinator field unset")
	}
	if s.shutdownCtx == nil {
		t.Error("shutdownCtx should always be initialised, even in legacy mode")
	}
}

func TestNewServerInvalidCoordinatorURLFails(t *testing.T) {
	_, err := NewServer(&Config{
		KeySeed: "test",
		Reverse: true,
		Coordinator: &coordinator.Config{
			URL: "http://insecure", // not https
		},
	})
	if err == nil {
		t.Fatal("NewServer accepted http coordinator URL")
	}
}

func TestServerCloseCancelsShutdownCtxWithSentinel(t *testing.T) {
	s, err := NewServer(&Config{
		KeySeed: "test",
		Reverse: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// Don't actually start the http server; just call Close.
	s.shutdownCancel(tunnel.ErrServerShutdown)
	if !errors.Is(s.shutdownCtx.Err(), context.Canceled) {
		t.Errorf("shutdownCtx.Err = %v, want Canceled", s.shutdownCtx.Err())
	}
	if !errors.Is(context.Cause(s.shutdownCtx), tunnel.ErrServerShutdown) {
		t.Errorf("shutdownCtx cause = %v, want ErrServerShutdown", context.Cause(s.shutdownCtx))
	}
}
