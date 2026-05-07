package tunnel

import (
	"context"
	"errors"
	"net"
	"sync"
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

func TestProxyRunCallsOnRemoteUnboundOnExit(t *testing.T) {
	logger := cio.NewLogger("test")
	logger.Info = false
	logger.Debug = false

	var (
		unboundFired  bool
		unboundReason DisconnectReason
		mu            sync.Mutex
	)

	cfg := Config{
		Logger:  logger,
		Inbound: true,
		OnRemoteBound: func(ctx context.Context, r *settings.Remote, ln net.Listener) error {
			return nil
		},
		OnRemoteUnbound: func(r *settings.Remote, reason DisconnectReason) {
			mu.Lock()
			defer mu.Unlock()
			unboundFired = true
			unboundReason = reason
		},
	}
	tun := New(cfg)

	remote := &settings.Remote{
		Reverse:   true,
		LocalHost: "127.0.0.1", LocalPort: "0", LocalProto: "tcp",
		RemoteHost: "127.0.0.1", RemotePort: "0", RemoteProto: "tcp",
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() { done <- tun.BindRemotes(ctx, []*settings.Remote{remote}) }()

	// Wait until proxy is up.
	time.Sleep(100 * time.Millisecond)

	// Simulate clean client disconnect (no cause).
	cancel(nil)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if !unboundFired {
		t.Fatal("OnRemoteUnbound was not called")
	}
	if unboundReason != DisconnectClient {
		t.Errorf("reason = %q, want DisconnectClient", unboundReason)
	}
}

func TestProxyRunReportsServerShutdownReason(t *testing.T) {
	logger := cio.NewLogger("test")
	logger.Info = false
	logger.Debug = false

	var (
		mu            sync.Mutex
		unboundReason DisconnectReason
		fired         bool
	)

	cfg := Config{
		Logger:  logger,
		Inbound: true,
		OnRemoteUnbound: func(r *settings.Remote, reason DisconnectReason) {
			mu.Lock()
			defer mu.Unlock()
			unboundReason = reason
			fired = true
		},
	}
	tun := New(cfg)

	remote := &settings.Remote{
		Reverse:   true,
		LocalHost: "127.0.0.1", LocalPort: "0", LocalProto: "tcp",
		RemoteHost: "127.0.0.1", RemotePort: "0", RemoteProto: "tcp",
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() { done <- tun.BindRemotes(ctx, []*settings.Remote{remote}) }()
	time.Sleep(100 * time.Millisecond)

	cancel(ErrServerShutdown)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if !fired {
		t.Fatal("OnRemoteUnbound did not fire")
	}
	if unboundReason != DisconnectServerShutdown {
		t.Errorf("reason = %q, want DisconnectServerShutdown", unboundReason)
	}
}
