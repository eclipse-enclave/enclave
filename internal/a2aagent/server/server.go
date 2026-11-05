// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package server is the worker adapter: A2A JSON-RPC server, hub control connection and host ctl socket.
package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
)

// Defaults from proposal 17 and interfaces.md section 11, used when a bound is zero.
const (
	DefaultQueueDepth      = 8
	DefaultMaxRequestBytes = 1 << 20
	DefaultReconnectWindow = 60 * time.Second
	// KeepaliveInterval is the hub's control stream keepalive; 2 missed intervals are a loss.
	KeepaliveInterval = 15 * time.Second
	// DefaultEpochWait bounds how long a send waits for the control stream to confirm its epoch.
	DefaultEpochWait = 30 * time.Second
)

// CtlSocketPath is where the running adapter accepts ctl requests; container-local, see CR-A10-2.
const CtlSocketPath = "/tmp/enclave-a2a-agent/ctl.sock"

// HostMemberName is the host principal's member name (interfaces.md Conventions).
const HostMemberName = "_host"

// Table is the context table the server needs; Tasks gives the current queue positions.
type Table interface {
	a2aagent.ContextTable
	Tasks() []*a2aagent.TaskRecord
}

// Options configures a Server.
type Options struct {
	Config   a2aagent.Config
	Table    Table
	Executor a2aagent.Executor
	// Skills are the card skills from the tool's a2a block; empty yields one generic skill.
	Skills []a2a.AgentSkill
	// Version is the card version, normally the binary version.
	Version string
	// Listener replaces the IPv4 listener on Config.AdapterPort (tests).
	Listener net.Listener
	// CtlSocket replaces CtlSocketPath (tests).
	CtlSocket         string
	KeepaliveInterval time.Duration
	EpochWait         time.Duration
	Log               io.Writer
}

// Server implements a2aagent.Server.
type Server struct {
	opts            Options
	cfg             a2aagent.Config
	table           Table
	exec            a2aagent.Executor
	queueDepth      int
	maxRequest      int
	reconnectWindow time.Duration
	keepalive       time.Duration
	epochWait       time.Duration
	hubClient       *http.Client

	mu sync.Mutex
	// epoch is the confirmed hub epoch admissions are bound to; empty while none is confirmed.
	epoch       string
	epochSignal chan struct{}
	ctl         controlState
}

var _ a2aagent.Server = (*Server)(nil)

// New validates opts and builds a server; Serve starts it.
func New(opts Options) (*Server, error) {
	if opts.Table == nil || opts.Executor == nil {
		return nil, errors.New("a2a server: table and executor are required")
	}
	cfg := opts.Config
	if cfg.Role != a2a.RoleWorker {
		return nil, fmt.Errorf("a2a server: role %q cannot serve; only workers run the adapter", cfg.Role)
	}
	if cfg.OutboundToken == "" || cfg.InboundToken == "" || cfg.Member == "" || cfg.Group == "" || cfg.GroupGeneration == "" {
		return nil, errors.New("a2a server: incomplete configuration")
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if opts.CtlSocket == "" {
		opts.CtlSocket = CtlSocketPath
	}
	s := &Server{
		opts:            opts,
		cfg:             cfg,
		table:           opts.Table,
		exec:            opts.Executor,
		queueDepth:      positive(cfg.QueueDepth, DefaultQueueDepth),
		maxRequest:      positive(cfg.MaxRequestBytes, DefaultMaxRequestBytes),
		reconnectWindow: positive(cfg.ReconnectWindow, DefaultReconnectWindow),
		keepalive:       positive(opts.KeepaliveInterval, KeepaliveInterval),
		epochWait:       positive(opts.EpochWait, DefaultEpochWait),
		epochSignal:     make(chan struct{}),
	}
	s.hubClient = newHubClient(s.keepalive)
	s.ctl.orchestrators = map[string]*orchestratorState{}
	s.ctl.membershipSignal = make(chan struct{})
	return s, nil
}

func positive[T int | time.Duration](v, def T) T {
	if v > 0 {
		return v
	}
	return def
}

// Serve implements a2aagent.Server; the executor starts before anything that can admit work (A8).
func (s *Server) Serve(ctx context.Context) error {
	ln := s.opts.Listener
	if ln == nil {
		var err error
		ln, err = net.Listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(s.cfg.AdapterPort)))
		if err != nil {
			return fmt.Errorf("a2a server: listen: %w", err)
		}
	}
	ctlLn, err := listenCtl(s.opts.CtlSocket)
	if err != nil {
		_ = ln.Close()
		return err
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	runDone := make(chan error, 1)
	go func() { runDone <- s.exec.Run(runCtx) }()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.runControl(runCtx) }()
	go func() { defer wg.Done(); s.serveCtl(runCtx, ctlLn) }()

	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return runCtx },
	}
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(ln) }()
	s.logf("serving %s on %s", s.cfg.Member, ln.Addr())

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-srvErr:
	}
	cancelRun()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = srv.Close()
	_ = ctlLn.Close()
	_ = os.Remove(s.opts.CtlSocket)
	wg.Wait()
	s.stopTimers()
	runErr := <-runDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, runErr)
}

// ServeHTTP answers the card and JSON-RPC paths; everything else is 404.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case a2a.CardPath:
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.serveCard(w)
	case a2a.AdapterRPCPath:
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.serveRPC(w, r)
	default:
		http.NotFound(w, r)
	}
}

// authorized checks the outbound token the hub presents on every forwarded request.
func (s *Server) authorized(r *http.Request) bool {
	got := r.Header.Get(a2a.HeaderAuthorization)
	want := a2a.BearerPrefix + s.cfg.OutboundToken
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// listenCtl replaces a stale socket from a previous adapter run.
func listenCtl(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("a2a server: ctl socket: %w", err)
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("a2a server: ctl socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("a2a server: ctl socket: %w", err)
	}
	return ln, nil
}

// setEpoch publishes the confirmed epoch to waiting sends.
func (s *Server) setEpoch(epoch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.epoch == epoch {
		return
	}
	s.epoch = epoch
	close(s.epochSignal)
	s.epochSignal = make(chan struct{})
}

// waitEpoch blocks until the control stream confirmed want, the bound elapses or ctx ends.
func (s *Server) waitEpoch(ctx context.Context, want string) bool {
	bound := time.NewTimer(s.epochWait)
	defer bound.Stop()
	for {
		s.mu.Lock()
		cur, signal := s.epoch, s.epochSignal
		s.mu.Unlock()
		if cur == want {
			return true
		}
		select {
		case <-signal:
		case <-bound.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

func (s *Server) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.opts.Log, "a2a adapter: "+format+"\n", args...)
}
