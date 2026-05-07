package tunnel

import (
	"context"
	"errors"
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
