// Package bridge runs a local MCP stdio server behind a loopback HTTP
// endpoint. lezz owns the process and the static config; consumers (adhd,
// smoke-alarm) only ever see an ordinary HTTP MCP endpoint.
//
// The bridge deliberately speaks newline-delimited JSON-RPC to the child
// (MCP's stdio transport), not LSP Content-Length framing. It keeps one
// long-lived child, initializes it once, and multiplexes HTTP requests onto
// its stdin/stdout.
//
// Scope: tools only. Server-initiated requests are refused with -32601 and
// notifications are logged and dropped, rather than pretending to support them.
package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is one static bridge definition. lezz embeds the defaults and may
// overlay them from ~/.lezz/lab/bridges/*.yaml.
type Config struct {
	Name           string            `yaml:"name"`
	Repo           string            `yaml:"repo,omitempty"`
	Command        string            `yaml:"command"`
	Args           []string          `yaml:"args,omitempty"`
	Env            map[string]string `yaml:"env,omitempty"`
	Cwd            string            `yaml:"cwd,omitempty"`
	Listen         string            `yaml:"listen,omitempty"` // default 127.0.0.1:0
	AllowedTools   []string          `yaml:"allowed_tools,omitempty"`
	RequestTimeout string            `yaml:"request_timeout,omitempty"` // default 120s
}

// Options carries runtime-only settings that must not live in the static config.
type Options struct {
	LogPath string // child stderr; empty means os.Stderr
}

// Bridge is a running child process plus its HTTP surface.
type Bridge struct {
	cfg     Config
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	logFile *os.File
	srv     *http.Server
	ln      net.Listener
	baseURL string
	timeout time.Duration

	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]chan rpcMessage
	nextID    uint64
	waitCh    chan error

	initialized atomic.Bool
	exited      atomic.Bool
	done        chan struct{}
	doneOnce    sync.Once
	closeOnce   sync.Once
	initResult  json.RawMessage
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Start launches the child, performs the MCP initialize handshake, serves
// /mcp, /healthz and /status on loopback, and returns once the listener is up.
func Start(ctx context.Context, cfg Config, opts Options) (*Bridge, error) {
	if strings.TrimSpace(cfg.Name) == "" {
		return nil, errors.New("bridge: name is required")
	}
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("bridge %q: command is required", cfg.Name)
	}
	timeout := 120 * time.Second
	if cfg.RequestTimeout != "" {
		d, err := time.ParseDuration(cfg.RequestTimeout)
		if err != nil {
			return nil, fmt.Errorf("bridge %q: request_timeout: %w", cfg.Name, err)
		}
		timeout = d
	}

	bin, err := exec.LookPath(cfg.Command)
	if err != nil {
		return nil, fmt.Errorf("bridge %q: resolve %q: %w", cfg.Name, cfg.Command, err)
	}

	// Own the pipes explicitly instead of using StdoutPipe/StdinPipe: cmd.Wait
	// may then run concurrently with the reader without closing it underneath.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("bridge %q: stdin pipe: %w", cfg.Name, err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, fmt.Errorf("bridge %q: stdout pipe: %w", cfg.Name, err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, fmt.Errorf("bridge %q: stderr pipe: %w", cfg.Name, err)
	}

	cmd := exec.Command(bin, cfg.Args...) //nolint:gosec // command is from lezz's own static config
	cmd.Dir = cfg.Cwd
	cmd.Env = childEnv(cfg.Env)
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if startErr := cmd.Start(); startErr != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
		return nil, fmt.Errorf("bridge %q: start %s: %w", cfg.Name, cfg.Command, startErr)
	}
	// The child holds its own copies now.
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()

	b := &Bridge{
		cfg:     cfg,
		cmd:     cmd,
		stdin:   stdinW,
		stdout:  bufio.NewReaderSize(stdoutR, 64*1024),
		timeout: timeout,
		pending: make(map[string]chan rpcMessage),
		waitCh:  make(chan error, 1),
		done:    make(chan struct{}),
	}

	if opts.LogPath != "" {
		f, openErr := os.OpenFile(opts.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if openErr != nil {
			b.killGroup()
			return nil, fmt.Errorf("bridge %q: open log: %w", cfg.Name, openErr)
		}
		b.logFile = f
	}
	go b.copyStderr(stderrR)
	go func() { b.waitCh <- cmd.Wait() }()
	go b.readLoop()

	// Fail fast if the child dies during the handshake.
	initCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := b.call(initCtx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "lezz-bridge", "version": "0.1.0"},
	})
	if err != nil {
		_ = b.Close()
		return nil, fmt.Errorf("bridge %q: initialize: %w%s", cfg.Name, err, b.tailLog())
	}
	b.initResult = res
	b.initialized.Store(true)

	if notifyErr := b.notify("notifications/initialized", map[string]any{}); notifyErr != nil {
		_ = b.Close()
		return nil, fmt.Errorf("bridge %q: initialized notification: %w", cfg.Name, notifyErr)
	}

	// Discover tools now so /status and the allow-list have a concrete surface.
	if _, listErr := b.listTools(initCtx); listErr != nil {
		_ = b.Close()
		return nil, fmt.Errorf("bridge %q: tools/list: %w%s", cfg.Name, listErr, b.tailLog())
	}

	listen := cfg.Listen
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		_ = b.Close()
		return nil, fmt.Errorf("bridge %q: listen %s: %w", cfg.Name, listen, err)
	}
	port := tcpPort(ln.Addr())
	if port == 0 {
		_ = ln.Close()
		_ = b.Close()
		return nil, fmt.Errorf("bridge %q: listener %v has no TCP port", cfg.Name, ln.Addr())
	}
	b.ln = ln
	b.baseURL = fmt.Sprintf("http://127.0.0.1:%d/mcp", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", b.handleMCP)
	mux.HandleFunc("/healthz", b.handleHealth)
	mux.HandleFunc("/status", b.handleStatus)
	b.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = b.srv.Serve(ln) }()

	go func() {
		select {
		case <-ctx.Done():
			_ = b.Close()
		case <-b.done:
		}
	}()
	return b, nil
}

// URL is the loopback MCP endpoint consumers should use.
func (b *Bridge) URL() string { return b.baseURL }

// Name is the configured skill name.
func (b *Bridge) Name() string { return b.cfg.Name }

// Repo is the optional provenance slug.
func (b *Bridge) Repo() string { return b.cfg.Repo }

// Alive reports whether the child is still running and initialized.
func (b *Bridge) Alive() bool { return b.initialized.Load() && !b.exited.Load() }

// Close terminates the child's process group and stops the HTTP listener.
func (b *Bridge) Close() error {
	b.closeOnce.Do(func() {
		if b.srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = b.srv.Shutdown(ctx)
			cancel()
		}
		if b.stdin != nil {
			_ = b.stdin.Close()
		}
		b.killGroup()
		select {
		case waitErr := <-b.waitCh:
			// The child was killed deliberately, so a non-nil exit status is expected.
			_ = waitErr
		case <-time.After(5 * time.Second):
			if b.cmd.Process != nil {
				_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGKILL)
			}
			select {
			case waitErr := <-b.waitCh:
				// Same: SIGKILL exit status carries no information here.
				_ = waitErr
			case <-time.After(2 * time.Second):
			}
		}
		b.exited.Store(true)
		b.shutdownDone()
		if b.logFile != nil {
			_ = b.logFile.Close()
		}
	})
	return nil
}

func (b *Bridge) shutdownDone() {
	b.doneOnce.Do(func() { close(b.done) })
}

func (b *Bridge) killGroup() {
	if b.cmd == nil || b.cmd.Process == nil {
		return
	}
	// Negative pid signals the whole group created by Setpgid, so wrapper
	// commands (uvx/npx) cannot leave the real server orphaned.
	_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGTERM)
}

func (b *Bridge) copyStderr(r io.Reader) {
	defer func() {
		if f, ok := r.(*os.File); ok {
			_ = f.Close()
		}
	}()
	// io.Discard already has type io.Writer, so the log file can replace it.
	dst := io.Discard
	if b.logFile != nil {
		dst = b.logFile
	}
	_, _ = io.Copy(dst, r)
}

func (b *Bridge) tailLog() string {
	if b.cfg.Name == "" {
		return ""
	}
	if b.logFile == nil {
		return ""
	}
	// Best effort: the log is diagnostic only.
	data, err := os.ReadFile(b.logFile.Name())
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return "\n--- bridge log ---\n" + strings.Join(lines, "\n")
}

func (b *Bridge) readLoop() {
	defer func() {
		b.exited.Store(true)
		b.failPending(errors.New("bridge child exited"))
		b.shutdownDone()
	}()
	for {
		line, err := b.stdout.ReadBytes('\n')
		if len(line) > 0 {
			b.handleChildLine(line)
		}
		if err != nil {
			return
		}
	}
}

func (b *Bridge) handleChildLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		b.logf("child sent non-JSON line (%d bytes): %s", len(line), truncate(string(line), 200))
		return
	}
	if len(msg.ID) > 0 {
		if msg.Method == "" {
			b.deliver(msg)
			return
		}
		// Server-to-client request. Tools-only bridge: refuse it rather than
		// silently dropping it and leaving the child waiting.
		_ = b.writeMessage(rpcMessage{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error: &rpcError{
				Code:    -32601,
				Message: "server-initiated requests are not supported by the lezz bridge",
			},
		})
		return
	}
	if msg.Method != "" {
		b.logf("child notification %s dropped", msg.Method)
	}
}

func (b *Bridge) deliver(msg rpcMessage) {
	b.pendingMu.Lock()
	ch := b.pending[string(msg.ID)]
	b.pendingMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func (b *Bridge) failPending(err error) {
	b.pendingMu.Lock()
	pending := b.pending
	b.pending = make(map[string]chan rpcMessage)
	b.pendingMu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- rpcMessage{Error: &rpcError{Code: -32603, Message: err.Error()}}:
		default:
		}
	}
}

func (b *Bridge) writeMessage(msg rpcMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if _, err := b.stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func (b *Bridge) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddUint64(&b.nextID, 1)
	key := strconv.FormatUint(id, 10)
	ch := make(chan rpcMessage, 1)
	b.pendingMu.Lock()
	b.pending[key] = ch
	b.pendingMu.Unlock()
	defer func() {
		b.pendingMu.Lock()
		delete(b.pending, key)
		b.pendingMu.Unlock()
	}()

	var rawParams json.RawMessage
	if params != nil {
		var err error
		rawParams, err = json.Marshal(params)
		if err != nil {
			return nil, err
		}
	}
	if err := b.writeMessage(rpcMessage{
		JSONRPC: "2.0",
		ID:      json.RawMessage(key),
		Method:  method,
		Params:  rawParams,
	}); err != nil {
		return nil, err
	}

	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, msg.Error
		}
		return msg.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.done:
		return nil, errors.New("bridge child exited")
	}
}

func (b *Bridge) notify(method string, params any) error {
	var rawParams json.RawMessage
	if params != nil {
		var err error
		rawParams, err = json.Marshal(params)
		if err != nil {
			return err
		}
	}
	return b.writeMessage(rpcMessage{JSONRPC: "2.0", Method: method, Params: rawParams})
}

func (b *Bridge) listTools(ctx context.Context) ([]string, error) {
	res, err := b.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(res, &payload); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(payload.Tools))
	for _, t := range payload.Tools {
		names = append(names, t.Name)
	}
	return names, nil
}

func (b *Bridge) allowed(name string) bool {
	if len(b.cfg.AllowedTools) == 0 {
		return true
	}
	for _, a := range b.cfg.AllowedTools {
		if a == name {
			return true
		}
	}
	return false
}

func (b *Bridge) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if !b.Alive() {
		http.Error(w, "child not running", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

func (b *Bridge) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, _ := b.listTools(ctx)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":    b.cfg.Name,
		"url":     b.baseURL,
		"command": b.cfg.Command,
		"args":    b.cfg.Args,
		"pid":     b.cmd.Process.Pid,
		"alive":   b.Alive(),
		"tools":   tools,
		"allowed": b.cfg.AllowedTools,
		"scope":   "tools-only",
	})
}

func (b *Bridge) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeRPCError(w, nil, -32700, "read body: "+err.Error())
		return
	}
	var req rpcMessage
	if unmarshalErr := json.Unmarshal(body, &req); unmarshalErr != nil {
		writeRPCError(w, nil, -32700, "invalid JSON: "+unmarshalErr.Error())
		return
	}
	if strings.TrimSpace(req.Method) == "" {
		writeRPCError(w, req.ID, -32600, "method is required")
		return
	}
	if len(req.ID) == 0 {
		// Notification: acknowledge it. The child session is already
		// initialized by the bridge; there is nothing to forward.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), b.timeout)
	defer cancel()

	var result json.RawMessage
	switch req.Method {
	case "initialize":
		result = b.initResult
	case "ping":
		result = json.RawMessage(`{}`)
	case "tools/list":
		result, err = b.call(ctx, "tools/list", nil)
		if err == nil && len(b.cfg.AllowedTools) > 0 {
			result = b.filterTools(result)
		}
	case "tools/call":
		var params struct {
			Name string `json:"name"`
		}
		if len(req.Params) > 0 {
			if paramsErr := json.Unmarshal(req.Params, &params); paramsErr != nil {
				writeRPCError(w, req.ID, -32602, "invalid tool params: "+paramsErr.Error())
				return
			}
		}
		if params.Name == "" {
			writeRPCError(w, req.ID, -32602, "tool name is required")
			return
		}
		if !b.allowed(params.Name) {
			writeRPCError(w, req.ID, -32602, fmt.Sprintf("tool %q is not allowed by the bridge config", params.Name))
			return
		}
		result, err = b.call(ctx, "tools/call", req.Params)
	default:
		result, err = b.call(ctx, req.Method, req.Params)
	}
	if err != nil {
		code := -32603
		var re *rpcError
		if errors.As(err, &re) {
			code = re.Code
		}
		writeRPCError(w, req.ID, code, err.Error())
		return
	}
	writeRPCResult(w, req.ID, result)
}

func (b *Bridge) filterTools(raw json.RawMessage) json.RawMessage {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(payload["tools"], &tools); err != nil {
		return raw
	}
	kept := make([]json.RawMessage, 0, len(tools))
	for _, t := range tools {
		var head struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(t, &head) == nil && b.allowed(head.Name) {
			kept = append(kept, t)
		}
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return raw
	}
	payload["tools"] = encoded
	out, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return out
}

func writeRPCResult(w http.ResponseWriter, id, result json.RawMessage) {
	if len(result) == 0 {
		result = json.RawMessage(`{}`)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}

func (b *Bridge) logf(format string, args ...any) {
	if b.logFile == nil {
		return
	}
	_, _ = fmt.Fprintf(b.logFile, "%s bridge: %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// ---- config loading -------------------------------------------------------

// LoadConfigs reads the embedded defaults and overlays any YAML files in
// overrideDir (typically ~/.lezz/lab/bridges). Files with the same name
// replace the embedded definition; new names are appended.
func LoadConfigs(overrideDir string) ([]Config, error) {
	cfgs, err := embeddedConfigs()
	if err != nil {
		return nil, err
	}
	if overrideDir != "" {
		paths, _ := filepath.Glob(filepath.Join(overrideDir, "*.yaml"))
		for _, p := range paths {
			data, readErr := os.ReadFile(p) //nolint:gosec // operator-owned config directory
			if readErr != nil {
				return nil, fmt.Errorf("read bridge config %s: %w", p, readErr)
			}
			var c Config
			if err := yaml.Unmarshal(data, &c); err != nil {
				return nil, fmt.Errorf("parse bridge config %s: %w", p, err)
			}
			if err := c.normalize(); err != nil {
				return nil, fmt.Errorf("bridge config %s: %w", p, err)
			}
			replaced := false
			for i := range cfgs {
				if cfgs[i].Name == c.Name {
					cfgs[i] = c
					replaced = true
					break
				}
			}
			if !replaced {
				cfgs = append(cfgs, c)
			}
		}
	}
	for i := range cfgs {
		if err := cfgs[i].normalize(); err != nil {
			return nil, err
		}
	}
	return cfgs, nil
}

func embeddedConfigs() ([]Config, error) {
	entries, err := configFS.ReadDir("configs")
	if err != nil {
		return nil, err
	}
	out := make([]Config, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := configFS.ReadFile("configs/" + e.Name())
		if err != nil {
			return nil, err
		}
		var c Config
		if err := yaml.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("embedded bridge config %s: %w", e.Name(), err)
		}
		out = append(out, c)
	}
	return out, nil
}

func (c *Config) normalize() error {
	c.Name = strings.TrimSpace(c.Name)
	c.Command = strings.TrimSpace(c.Command)
	if c.Name == "" {
		return errors.New("bridge: name is required")
	}
	if c.Command == "" {
		return fmt.Errorf("bridge %q: command is required", c.Name)
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:0"
	}
	if c.RequestTimeout == "" {
		c.RequestTimeout = "120s"
	}
	if _, err := time.ParseDuration(c.RequestTimeout); err != nil {
		return fmt.Errorf("bridge %q: request_timeout: %w", c.Name, err)
	}
	if c.Cwd != "" {
		c.Cwd = expandEnv(c.Cwd)
	}
	for i := range c.Args {
		c.Args[i] = expandEnv(c.Args[i])
	}
	expanded := make(map[string]string, len(c.Env))
	for k, v := range c.Env {
		expanded[k] = expandEnv(v)
	}
	c.Env = expanded
	return nil
}

// expandEnv supports ${VAR} and ${VAR:-default}. The default form is what lets
// the checked-in config name a scratch collection without hardcoding it.
func expandEnv(s string) string {
	return os.Expand(s, func(key string) string {
		if i := strings.Index(key, ":-"); i >= 0 {
			name, def := key[:i], key[i+2:]
			if v := os.Getenv(name); v != "" {
				return v
			}
			return def
		}
		return os.Getenv(key)
	})
}

// childEnv is a minimal allow-list, not an inheritance of the parent. Secrets
// the operator did not name in the bridge config never reach the child.
func childEnv(extra map[string]string) []string {
	keep := []string{
		"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TZ",
		"SSL_CERT_FILE", "SSL_CERT_DIR",
		"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
		"UV_CACHE_DIR", "UV_PYTHON", "UV_PYTHON_INSTALL_DIR",
	}
	env := make([]string, 0, len(keep)+len(extra))
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
