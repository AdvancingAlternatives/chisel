package tunnel

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/jpillora/chisel/share/cio"
	"github.com/jpillora/chisel/share/settings"
)

func TestBindRemotesCallsOnRemoteBound(t *testing.T) {
	logger := cio.NewLogger("test")
	logger.Info = false
	logger.Debug = false

	var (
		callbackFired bool
		callbackPort  int
	)

	cfg := Config{
		Logger:  logger,
		Inbound: true,
		OnRemoteBound: func(ctx context.Context, r *settings.Remote, ln net.Listener) error {
			callbackFired = true
			tcp, ok := ln.Addr().(*net.TCPAddr)
			if !ok {
				t.Errorf("listener.Addr() is %T, want *net.TCPAddr", ln.Addr())
				return nil
			}
			callbackPort = tcp.Port
			return nil
		},
	}
	tun := New(cfg)

	// Bind to a free port (RemotePort=0 → OS picks).
	remote := &settings.Remote{
		Reverse:     true,
		LocalHost:   "127.0.0.1",
		LocalPort:   "0",
		LocalProto:  "tcp",
		RemoteHost:  "127.0.0.1",
		RemotePort:  "0",
		RemoteProto: "tcp",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// BindRemotes blocks; run in a goroutine so we can cancel.
	done := make(chan error, 1)
	go func() { done <- tun.BindRemotes(ctx, []*settings.Remote{remote}) }()

	// Give the bind enough time to fire the callback.
	time.Sleep(100 * time.Millisecond)

	if !callbackFired {
		t.Fatal("OnRemoteBound was not called")
	}
	if callbackPort == 0 {
		t.Errorf("callbackPort = 0, want a real OS-assigned port")
	}

	cancel()
	<-done // unblock
}

func TestBindRemotesCallbackErrTearsDownProxy(t *testing.T) {
	logger := cio.NewLogger("test")
	logger.Info = false
	logger.Debug = false

	cfg := Config{
		Logger:  logger,
		Inbound: true,
		OnRemoteBound: func(ctx context.Context, r *settings.Remote, ln net.Listener) error {
			return errors.New("simulated teardown")
		},
	}
	tun := New(cfg)

	remote := &settings.Remote{
		Reverse:    true,
		LocalHost:  "127.0.0.1",
		LocalPort:  "0",
		LocalProto: "tcp",
		RemoteHost: "127.0.0.1", RemotePort: "0", RemoteProto: "tcp",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := tun.BindRemotes(ctx, []*settings.Remote{remote})
	if err == nil {
		t.Fatal("BindRemotes returned nil, want error from OnRemoteBound")
	}
	if !contains(err.Error(), "simulated teardown") {
		t.Errorf("err = %q, want it to wrap 'simulated teardown'", err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
