package chserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	chshare "github.com/jpillora/chisel/share"
	"github.com/jpillora/chisel/server/coordinator"
	"github.com/jpillora/chisel/share/cnet"
	"github.com/jpillora/chisel/share/settings"
	"github.com/jpillora/chisel/share/tunnel"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
)

// handleClientHandler is the main http websocket handler for the chisel server
func (s *Server) handleClientHandler(w http.ResponseWriter, r *http.Request) {
	//websockets upgrade AND has chisel prefix
	upgrade := strings.ToLower(r.Header.Get("Upgrade"))
	protocol := r.Header.Get("Sec-WebSocket-Protocol")
	if upgrade == "websocket" {
		if protocol == chshare.ProtocolVersion {
			s.handleWebsocket(w, r)
			return
		}
		//print into server logs and silently fall-through
		s.Infof("ignored client connection using protocol '%s', expected '%s'",
			protocol, chshare.ProtocolVersion)
	}
	//proxy target was provided
	if s.reverseProxy != nil {
		s.reverseProxy.ServeHTTP(w, r)
		return
	}
	//no proxy defined, provide access to health/version checks
	switch r.URL.Path {
	case "/health":
		w.Write([]byte("OK\n"))
		return
	case "/version":
		w.Write([]byte(chshare.BuildVersion))
		return
	}
	//missing :O
	w.WriteHeader(404)
	w.Write([]byte("Not found"))
}

// handleWebsocket is responsible for handling the websocket connection
func (s *Server) handleWebsocket(w http.ResponseWriter, req *http.Request) {
	id := atomic.AddInt32(&s.sessCount, 1)
	l := s.Fork("session#%d", id)
	wsConn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		l.Debugf("Failed to upgrade (%s)", err)
		return
	}
	conn := cnet.NewWebSocketConn(wsConn)
	// perform SSH handshake on net.Conn
	l.Debugf("Handshaking with %s...", req.RemoteAddr)
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, s.sshConfig)
	if err != nil {
		s.Debugf("Failed to handshake (%s)", err)
		return
	}
	// AA-fork: ensure sshConn is closed on every return path. Upstream
	// chisel relies on the request-context teardown to GC the connection,
	// which works for paths that block in BindSSH/BindRemotes. The
	// coordinator-error early-return paths (after r.Reply(true, nil) but
	// before BindSSH) need explicit cleanup so the SSH+websocket conn
	// doesn't linger until HTTP request closure.
	defer sshConn.Close()
	// pull the users from the session map
	var user *settings.User
	if s.users.Len() > 0 {
		sid := string(sshConn.SessionID())
		u, ok := s.sessions.Get(sid)
		if !ok {
			panic("bug in ssh auth handler")
		}
		user = u
		s.sessions.Del(sid)
	}
	// chisel server handshake (reverse of client handshake)
	// verify configuration
	l.Debugf("Verifying configuration")
	// wait for request, with timeout
	var r *ssh.Request
	select {
	case r = <-reqs:
	case <-time.After(settings.EnvDuration("CONFIG_TIMEOUT", 10*time.Second)):
		l.Debugf("Timeout waiting for configuration")
		sshConn.Close()
		return
	}
	failed := func(err error) {
		l.Debugf("Failed: %s", err)
		r.Reply(false, []byte(err.Error()))
	}
	if r.Type != "config" {
		failed(s.Errorf("expecting config request"))
		return
	}
	c, err := settings.DecodeConfig(r.Payload)
	if err != nil {
		failed(s.Errorf("invalid config"))
		return
	}
	//print if client and server  versions dont match
	cv := strings.TrimPrefix(c.Version, "v")
	if cv == "" {
		cv = "<unknown>"
	}
	sv := strings.TrimPrefix(chshare.BuildVersion, "v")
	if cv != sv {
		l.Infof("Client version (%s) differs from server version (%s)", cv, sv)
	}
	//validate remotes
	for _, r := range c.Remotes {
		//if user is provided, ensure they have
		//access to the desired remotes
		if user != nil {
			addr := r.UserAddr()
			if !user.HasAccess(addr) {
				failed(s.Errorf("access to '%s' denied", addr))
				return
			}
		}
		//confirm reverse tunnels are allowed
		if r.Reverse && !s.config.Reverse {
			l.Debugf("Denied reverse port forwarding request, please enable --reverse")
			failed(s.Errorf("Reverse port forwaring not enabled on server"))
			return
		}
		//confirm reverse tunnel is available
		if r.Reverse && !r.CanListen() {
			failed(s.Errorf("Server cannot listen on %s", r.String()))
			return
		}
	}
	//successfuly validated config!
	// AA-fork: defer the success reply until after coordinator-lookup
	// gating. If r.Reply(true) fires too early, subsequent
	// r.Reply(false, ...) calls from the coordinator-lookup error path
	// are silently discarded by golang.org/x/crypto/ssh — the SSH
	// config-request channel only accepts one reply. That would mean
	// the LCM-side chisel client never sees the operator-facing reject
	// string ("no pending session for hostname X" / "coordinator
	// unreachable, retry in flight" / etc.) in its journal. Legacy
	// path (no coordinator) replies immediately so behavior matches
	// upstream.
	if s.coordClient == nil {
		r.Reply(true, nil)
	}

	// AA-fork: coordinator integration — runs only when configured.
	// Derive a request-scoped ctx that's cancelled either by the request
	// closing OR the server shutting down. Server shutdown propagates
	// cancel-cause tunnel.ErrServerShutdown so OnRemoteUnbound's
	// classifyDisconnect can pick it up locally.
	reqCtx, reqCancel := context.WithCancelCause(req.Context())
	go func() {
		select {
		case <-s.shutdownCtx.Done():
			reqCancel(context.Cause(s.shutdownCtx))
		case <-reqCtx.Done():
		}
	}()
	defer reqCancel(nil)

	var (
		sessionID  string
		hostname   = req.Host
		remoteAddr = req.RemoteAddr
	)

	if s.coordClient != nil {
		// 1. Lookup pending session for this hostname.
		sess, err := s.coordClient.Lookup(reqCtx, hostname)
		if err != nil {
			level := "info"
			if errors.Is(err, coordinator.ErrAuth) {
				level = "error"
			} else if errors.Is(err, coordinator.ErrTransient) {
				level = "warn"
			}
			s.coordLog(level, "lookup failed hostname=%s err=%v", hostname, err)
			failed(s.Errorf("%s", rejectMessage(err)))
			return
		}
		sessionID = sess.SessionID

		// 2. Override the reverse remote's port to the allocated value.
		if err := overrideReverseRemotePort(c.Remotes, sess.Port); err != nil {
			l.Errorf("override reverse port: %v", err)
			failed(s.Errorf("server config error"))
			return
		}

		// Coordinator gating passed — now confirm the SSH config request.
		r.Reply(true, nil)
	}

	//tunnel per ssh connection
	tunnelConfig := tunnel.Config{
		Logger:    l,
		Inbound:   s.config.Reverse,
		Outbound:  true, //server always accepts outbound
		Socks:     s.config.Socks5,
		KeepAlive: s.config.KeepAlive,
	}
	//enforce ACL on every channel, not just the initial config
	if user != nil {
		tunnelConfig.ACL = user.HasAccess
	}
	// AA-fork: install coordinator callbacks when configured. The closures
	// capture sessionID + hostname + remoteAddr + the client + a logger so
	// the call sites (inside tunnel package) don't need to know about the
	// chisel-server type.
	if s.coordClient != nil {
		tunnelConfig.OnRemoteBound = coordinatorBindHook(s.coordClient, sessionID, hostname, remoteAddr)
		tunnelConfig.OnRemoteUnbound = coordinatorUnbindHook(
			s.coordClient,
			func(format string, args ...interface{}) { l.Infof(format, args...) },
			s.config.Coordinator.TimeoutOrDefault(),
			sessionID,
			hostname,
		)
	}
	tunnel := tunnel.New(tunnelConfig)
	//bind
	eg, ctx := errgroup.WithContext(reqCtx)
	eg.Go(func() error {
		//connected, handover ssh connection for tunnel to use, and block
		return tunnel.BindSSH(ctx, sshConn, reqs, chans)
	})
	eg.Go(func() error {
		//connected, setup reversed-remotes?
		serverInbound := c.Remotes.Reversed(true)
		if len(serverInbound) == 0 {
			return nil
		}
		//block
		return tunnel.BindRemotes(ctx, serverInbound)
	})
	err = eg.Wait()
	if err != nil && !strings.HasSuffix(err.Error(), "EOF") {
		l.Debugf("Closed connection (%s)", err)
	} else {
		l.Debugf("Closed connection")
	}
}
