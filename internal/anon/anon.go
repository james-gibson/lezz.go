// Package anon is the anonymous MCP surface: a valid MCP server with an empty
// tool registry. It has no backend, no proxy method, no credentials, and no
// way to call the real cluster. "Anonymous sees nothing" is true by
// construction, not by a filter that could regress.
package anon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/james-gibson/lezz.go/internal/debuglease"
)

// Config describes the anonymous listener.
type Config struct {
	Name           string
	Listen         string
	DebugSocket    string
	LeasePath      string
	AllowedSigners string
	SignerIdentity string
	SeedID         string
}

// Server is the null MCP surface.
type Server struct {
	cfg Config
	srv *http.Server
	ln  net.Listener

	listCalls atomic.Int64
	denied    atomic.Int64

	closeOnce sync.Once
	closeErr  error

	leaseMu   sync.RWMutex
	lease     *debuglease.Lease
	leaseGen  uint64
	debugSrv  *http.Server
	debugLn   net.Listener
	debugPath string
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// New validates the config.
func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = "anonymous"
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	return &Server{cfg: cfg}, nil
}

// Start serves the empty MCP surface.
func (s *Server) Start(parent context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("anon: listen: %w", err)
	}
	s.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	s.ReloadLease()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		defer signal.Stop(hup)
		for {
			select {
			case <-parent.Done():
				return
			case <-hup:
				s.ReloadLease()
			}
		}
	}()
	go func() {
		<-parent.Done()
		_ = s.Close()
	}()
	return nil
}

// URL is the loopback MCP endpoint.
func (s *Server) URL() string {
	if s.ln == nil {
		return ""
	}
	port := tcpPort(s.ln.Addr())
	if port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
}

// ReloadLease re-reads and verifies the lease. A missing or invalid lease
// always deactivates the debug surface: this is a lease, not a sticky mode.
func (s *Server) ReloadLease() {
	if s.cfg.LeasePath == "" {
		s.deactivateDebug()
		return
	}
	lease, err := debuglease.Verify(s.cfg.LeasePath, s.cfg.LeasePath+".sig", s.cfg.AllowedSigners, s.cfg.SignerIdentity)
	if err != nil {
		slog.Warn("anonymous debug lease rejected", "error", err)
		s.deactivateDebug()
		return
	}
	if s.cfg.SeedID != "" && lease.SeedID != s.cfg.SeedID {
		slog.Warn("anonymous debug lease seed mismatch", "lease", lease.SeedID, "expected", s.cfg.SeedID)
		s.deactivateDebug()
		return
	}
	if !lease.Active(time.Now()) {
		slog.Warn("anonymous debug lease not active", "not_before", lease.NotBefore, "not_after", lease.NotAfter)
		s.deactivateDebug()
		return
	}
	s.activateDebug(lease)
}

// DebugActive reports whether a verified, unexpired lease is serving the debug
// socket.
func (s *Server) DebugActive() bool {
	s.leaseMu.RLock()
	defer s.leaseMu.RUnlock()
	return s.debugSrv != nil && s.lease != nil && s.lease.Active(time.Now())
}

// DebugPath is the active debug socket path, or "".
func (s *Server) DebugPath() string {
	s.leaseMu.RLock()
	defer s.leaseMu.RUnlock()
	if s.debugSrv == nil {
		return ""
	}
	return s.debugPath
}

func (s *Server) activateDebug(lease *debuglease.Lease) {
	s.leaseMu.Lock()
	s.lease = lease
	s.leaseGen++
	gen := s.leaseGen
	already := s.debugSrv != nil
	s.leaseMu.Unlock()
	if already {
		s.armExpiry(lease, gen)
		return
	}
	path := s.cfg.DebugSocket
	if path == "" {
		path = defaultDebugSocket()
	}
	if len(path) > 100 {
		slog.Warn("anonymous debug socket path too long for a Unix socket", "path", path)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		slog.Warn("anonymous debug socket dir", "error", err)
		return
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		slog.Warn("anonymous debug socket", "error", err)
		return
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		slog.Warn("anonymous debug socket chmod", "error", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleDebugStatus)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	// Bound how long a client may take to send request headers (and keep an
	// idle keep-alive connection): a slow-writer on the debug socket must not
	// be able to pin a connection open indefinitely.
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.leaseMu.Lock()
	s.debugSrv = srv
	s.debugLn = ln
	s.debugPath = path
	s.leaseMu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	s.armExpiry(lease, gen)
	slog.Info("anonymous debug surface active", "socket", path, "seed", lease.SeedID, "signer", lease.Signer)
}

func (s *Server) armExpiry(lease *debuglease.Lease, gen uint64) {
	d := time.Until(lease.NotAfter)
	if d < 0 {
		d = 0
	}
	time.AfterFunc(d, func() {
		s.leaseMu.RLock()
		current := s.leaseGen == gen
		s.leaseMu.RUnlock()
		if current {
			slog.Info("anonymous debug lease expired; closing surface", "seed", lease.SeedID)
			s.deactivateDebug()
		}
	})
}

func (s *Server) deactivateDebug() {
	s.leaseMu.Lock()
	srv := s.debugSrv
	ln := s.debugLn
	path := s.debugPath
	s.debugSrv = nil
	s.debugLn = nil
	s.debugPath = ""
	s.lease = nil
	s.leaseGen++
	s.leaseMu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
	if ln != nil {
		_ = ln.Close()
	}
	if path != "" {
		_ = os.Remove(path)
	}
}

func (s *Server) handleDebugStatus(w http.ResponseWriter, _ *http.Request) {
	s.leaseMu.RLock()
	lease := s.lease
	active := s.debugSrv != nil && lease != nil && lease.Active(time.Now())
	s.leaseMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	if !active {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"active": false})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"active":     true,
		"surface":    "debug",
		"seed_id":    lease.SeedID,
		"signer":     lease.Signer,
		"actions":    lease.Actions,
		"not_before": lease.NotBefore,
		"not_after":  lease.NotAfter,
		"tick":       lease.Tick,
	})
}

func defaultDebugSocket() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lezz", "run", "anon-debug.sock")
}

// Close stops the listener.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.deactivateDebug()
		if s.srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.srv.Shutdown(ctx)
			cancel()
		}
	})
	return s.closeErr
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()
	var req rpcRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, nil, -32700, "parse error")
		return
	}
	if len(req.ID) == 0 {
		// Notifications are accepted and dropped: this surface has no state.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		// Deliberately generic: no adhd identity, no version, no capabilities
		// beyond an empty tool list.
		writeResult(w, req.ID, map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": s.cfg.Name, "version": "0"},
		})
	case "ping":
		writeResult(w, req.ID, map[string]any{})
	case "tools/list":
		s.listCalls.Add(1)
		writeResult(w, req.ID, map[string]any{"tools": []any{}})
	case "tools/call":
		// Never echo the attempted name: the response must not reveal whether a
		// tool exists. The name is logged locally for the debug surface.
		name := attemptedTool(req.Params)
		s.denied.Add(1)
		slog.Info("anonymous surface", "event", "tool_call_denied", "tool", name, "caller", r.RemoteAddr)
		writeError(w, req.ID, -32601, "method not found")
	default:
		s.denied.Add(1)
		slog.Info("anonymous surface", "event", "method_denied", "method", req.Method, "caller", r.RemoteAddr)
		writeError(w, req.ID, -32601, "method not found")
	}
}

func attemptedTool(params json.RawMessage) string {
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(params, &p)
	return p.Name
}

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}

// tcpPort reports the bound port of a TCP listener address. net.Listen("tcp")
// always yields a *net.TCPAddr, but the comma-ok form keeps a non-TCP address
// from panicking; the string form is the fallback.
func tcpPort(addr net.Addr) int {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.Port
	}
	_, portStr, err := net.SplitHostPort(addr.String())
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}
