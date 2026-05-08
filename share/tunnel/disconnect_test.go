package tunnel

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestDisconnectReasonValues(t *testing.T) {
	cases := []struct {
		name   string
		reason DisconnectReason
		want   string
	}{
		{"DisconnectClient", DisconnectClient, "client_disconnect"},
		{"DisconnectConnectionLost", DisconnectConnectionLost, "connection_lost"},
		{"DisconnectServerShutdown", DisconnectServerShutdown, "server_shutdown"},
	}
	for _, tc := range cases {
		if string(tc.reason) != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, string(tc.reason), tc.want)
		}
	}
}

func TestErrServerShutdownAsCancelCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrServerShutdown)
	if !errors.Is(context.Cause(ctx), ErrServerShutdown) {
		t.Errorf("context.Cause(ctx) = %v, want ErrServerShutdown", context.Cause(ctx))
	}
}

func TestClassifyDisconnectFromCancelCause(t *testing.T) {
	cases := []struct {
		name  string
		cause error
		want  DisconnectReason
	}{
		{"server shutdown sentinel", ErrServerShutdown, DisconnectServerShutdown},
		{"plain context.Canceled", context.Canceled, DisconnectClient},
		{"nil cause", nil, DisconnectClient},
		{"io.EOF", io.EOF, DisconnectClient},
		{"wrapped EOF", errors.New("read tcp: EOF"), DisconnectClient},
		{"abnormal error", errors.New("read tcp: connection reset by peer"), DisconnectConnectionLost},
		{"server shutdown wrapped in fmt.Errorf", errors.Join(ErrServerShutdown, errors.New("plus context")), DisconnectServerShutdown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			if tc.cause != nil {
				cancel(tc.cause)
			} else {
				cancel(nil) // cancel with nil cause; context.Canceled appears via Err but Cause is nil
			}
			got := classifyDisconnect(ctx)
			if got != tc.want {
				t.Errorf("classifyDisconnect = %q, want %q (cause=%v)", got, tc.want, tc.cause)
			}
		})
	}
}
