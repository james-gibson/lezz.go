// Package mcpwrapper exposes lezz-owned sidecars as a narrow, read-only MCP
// surface. It is the first unauthenticated cluster layer: every tool here is
// safe by construction — no caller-supplied endpoint, no credentials, no
// command execution. The real adhd tools stay behind its own gate (#27).
package mcpwrapper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config describes one wrapper instance.
type Config struct {
	Name     string
	Listen   string
	CacheURL string
	Version  string
}

// Server is a read-only MCP server over the PR cache.
type Server struct {
	cfg Config
	srv *http.Server
	ln  net.Listener

	closeOnce sync.Once
	closeErr  error
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

const maxBody = 16 << 20

// New validates the wrapper config. CacheURL is local config, never a request
// field, so the wrapper cannot be turned into an SSRF hop.
func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = "pr"
	}
	if strings.TrimSpace(cfg.CacheURL) == "" {
		return nil, fmt.Errorf("mcp wrapper %s: cache URL is required", cfg.Name)
	}
	if _, err := url.Parse(cfg.CacheURL); err != nil {
		return nil, fmt.Errorf("mcp wrapper %s: cache URL: %w", cfg.Name, err)
	}
	cfg.CacheURL = strings.TrimRight(cfg.CacheURL, "/")
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	return &Server{cfg: cfg}, nil
}

// Start serves /mcp on loopback.
func (s *Server) Start(parent context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("mcp wrapper %s: listen: %w", s.cfg.Name, err)
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

// Close stops the listener.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		if s.srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.srv.Shutdown(ctx)
			cancel()
		}
	})
	return s.closeErr
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
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
		writeError(w, nil, -32700, "invalid JSON: "+err.Error())
		return
	}
	if len(req.ID) == 0 {
		// Notifications are accepted; this wrapper has no session state.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		writeResult(w, req.ID, map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "lezz-mcp-" + s.cfg.Name, "version": s.cfg.Version},
			"instructions":    Motd,
		})
	case "ping":
		writeResult(w, req.ID, map[string]any{})
	case "tools/list":
		writeResult(w, req.ID, map[string]any{"tools": tools()})
	case "tools/call":
		s.handleToolsCall(w, r, req)
	default:
		writeError(w, req.ID, -32601, "method not found")
	}
}

// Motd is the first thing a workspace user — or their agent — is told when it
// connects to a project surface. It is the note from the lab's burned test
// vector (isotope/testdata/burned), owned by Zaphod Beeblebrox: a key whose
// private half is public on purpose, which is exactly why the sentence is
// "verify it, don't believe it".
//
// It rides in `initialize.instructions` because that is the protocol's one
// greeting: not printed on every call, and not something a client can miss.
const Motd = "Don't Panic. 42 is a coordinate, not a credential; trust is what you pinned.\n" +
	"This greeting is a signed test vector (isotope/testdata/burned, key: zaphod-beeblebrox), " +
	"so anyone can check it and nobody has to trust it. Verify it; don't believe it."

func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	var call struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &call); err != nil {
		writeError(w, req.ID, -32602, "invalid tool params: "+err.Error())
		return
	}
	switch call.Name {
	case "pr.get":
		s.toolPRGet(w, r, req, call.Arguments)
	case "pr.status":
		body, _, err := s.get(r.Context(), "/status")
		if err != nil {
			writeToolError(w, req.ID, err)
			return
		}
		writeToolText(w, req.ID, string(body), nil)
	default:
		writeError(w, req.ID, -32602, "unknown tool: "+call.Name)
	}
}

func (s *Server) toolPRGet(w http.ResponseWriter, r *http.Request, req rpcRequest, args map[string]interface{}) {
	repo, _ := args["repo"].(string)
	if !repoPattern.MatchString(repo) {
		writeError(w, req.ID, -32602, "repo must be owner/name")
		return
	}
	number, ok := numberArg(args["number"])
	if !ok || number <= 0 {
		writeError(w, req.ID, -32602, "number must be a positive integer")
		return
	}
	kind := "view"
	if v, ok := args["kind"].(string); ok && v != "" {
		kind = v
	}
	if kind != "view" && kind != "diff" {
		writeError(w, req.ID, -32602, "kind must be view or diff")
		return
	}
	refresh, _ := args["refresh"].(bool)

	q := url.Values{}
	q.Set("repo", repo)
	q.Set("number", strconv.Itoa(number))
	q.Set("kind", kind)
	if refresh {
		q.Set("refresh", "1")
	}
	body, hdr, err := s.get(r.Context(), "/pr?"+q.Encode())
	if err != nil {
		writeToolError(w, req.ID, err)
		return
	}
	writeToolText(w, req.ID, string(body), map[string]any{
		"kind":       kind,
		"bytes":      len(body),
		"source":     hdr.Get("X-Cache"),
		"stale":      hdr.Get("X-Stale") == "true",
		"fetched_at": hdr.Get("X-Fetched-At"),
	})
}

func (s *Server) get(ctx context.Context, path string) ([]byte, http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.CacheURL+path, http.NoBody)
	if err != nil {
		return nil, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("cache unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.Header, fmt.Errorf("cache: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, resp.Header, nil
}

func tools() []map[string]any {
	// Deliberately absent: adhd.hurl.run, adhd.gh.run, adhd.proxy, and queue
	// writes. Each executes a caller-controlled command, accepts a caller
	// endpoint, or mutates state; the unauthenticated lobby is read-only (#27).
	// The smoke-alarm HURL scanner is a target pre-protocol gate, not a tool.
	return []map[string]any{
		{
			"name":        "pr.get",
			"description": "Read a pull request through the lezz PR cache (view metadata/files/comments or diff). Read-only.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo":    map[string]any{"type": "string", "description": "owner/name"},
					"number":  map[string]any{"type": "integer", "minimum": 1},
					"kind":    map[string]any{"type": "string", "enum": []string{"view", "diff"}},
					"refresh": map[string]any{"type": "boolean"},
				},
				"required": []string{"repo", "number"},
			},
		},
		{
			"name":        "pr.status",
			"description": "Cache statistics: hits, misses, fetches, entries.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

func numberArg(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		i := int(n)
		return i, float64(i) == n
	case int:
		return n, true
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	default:
		return 0, false
	}
}

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeToolText(w http.ResponseWriter, id json.RawMessage, text string, meta map[string]any) {
	res := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}
	if meta != nil {
		res["_meta"] = meta
	}
	writeResult(w, id, res)
}

func writeToolError(w http.ResponseWriter, id json.RawMessage, err error) {
	writeResult(w, id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": err.Error()}},
		"isError": true,
	})
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
