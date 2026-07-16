package e2e_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

// lockedWriter is an io.Writer that serializes writes to an underlying
// bytes.Buffer behind a mutex so the test's polling goroutine can read
// the buffer concurrently with io.Copy writes.
type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// TestCoordinatorRejectMessageReachesClient is the regression test for
// PR #1 review finding B1: r.Reply(true, nil) used to fire BEFORE the
// coordinator-lookup branch, which made the subsequent
// r.Reply(false, [bytes]) on the lookup-failure path silently
// discarded by golang.org/x/crypto/ssh. The LCM-side chisel client's
// journal would terminate with a generic disconnect message instead
// of the operator-facing reject string ("no pending session for
// hostname X" / "coordinator unreachable, retry in flight").
//
// Mechanism: when chisel-server replies r.Reply(false, [bytes]), the
// client's connection-loop returns errors.New(string(bytes)) from
// connectionOnce, then connectionLoop logs it as "Connection error:
// <reject-string>" via the chisel client's cio.Logger (writes to
// os.Stderr). We redirect os.Stderr to a pipe for the duration of
// this test and assert the reject string appears in the captured
// output.
func TestCoordinatorRejectMessageReachesClient(t *testing.T) {
	// Redirect os.Stderr so we can capture the chisel client's
	// "Connection error: ..." log line. Restore on cleanup.
	origStderr := os.Stderr
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = wPipe
	t.Cleanup(func() { os.Stderr = origStderr })

	var (
		mu      sync.Mutex
		buf     bytes.Buffer
		copyErr = make(chan error, 1)
	)
	// safeBuf wraps buf with the mutex so io.Copy writes are visible
	// to the polling goroutine in waitFor (without this, buf only
	// becomes populated when io.Copy returns on EOF).
	safeBuf := &lockedWriter{mu: &mu, buf: &buf}
	// Goroutine drains the read end of the pipe into buf. Mirror back
	// to the original stderr so test failures still print useful
	// context when -v.
	go func() {
		_, err := io.Copy(io.MultiWriter(safeBuf, origStderr), rPipe)
		copyErr <- err
	}()

	fc := newFakeCoordinator(t)
	// no preallocate — fake returns 404 for any hostname

	srv := chiselServerWithFakeCoordinator(t, fc, "lcm-fleet:secret")
	listenPort := startServer(t, srv)
	defer srv.Close()

	c, err := chclient.NewClient(&chclient.Config{
		Server:           "http://127.0.0.1:" + listenPort,
		Auth:             "lcm-fleet:secret",
		Headers:          http.Header{"Host": []string{"lcm-unknown-reject"}},
		Remotes:          []string{"R:" + availablePort() + ":127.0.0.1:22"},
		MaxRetryCount:    0,
		MaxRetryInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.Start(ctx)
	t.Cleanup(func() { c.Close() })

	// Wait for the connection-loop to give up (MaxRetryCount=0).
	// connectionLoop logs "Connection error: <reject>" then "Give up".
	// We use waitFor on the captured buffer.
	const wantSubstring = "no pending session for hostname"
	got := waitFor(2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(buf.String(), wantSubstring)
	})

	// Close the write end of the pipe and drain the copier so the
	// final buffer is complete before we assert / print on failure.
	_ = wPipe.Close()
	<-copyErr

	mu.Lock()
	captured := buf.String()
	mu.Unlock()

	if !got {
		t.Fatalf("client never logged the coordinator reject string\n"+
			"want substring: %q\n"+
			"captured stderr:\n%s",
			wantSubstring, captured)
	}

	// Sanity: the lookup actually fired on the coordinator side. If
	// it didn't, the test would be passing for the wrong reason
	// (e.g., a generic auth/disconnect log line that happened to
	// contain a similar word).
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.lookupCalls) == 0 {
		t.Errorf("coordinator lookup never fired; reject string surfaced via "+
			"some other path. captured stderr:\n%s", captured)
	}
}
