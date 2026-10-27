package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBridgeHelperProcess is not a real test: it is the child process the
// bridge launches, re-executing the test binary so the test needs no external
// server.
func TestBridgeHelperProcess(t *testing.T) {
	if os.Getenv("BRIDGE_TEST_CHILD") != "1" {
		return
	}
	if f := os.Getenv("BRIDGE_TEST_GRANDCHILD_FILE"); f != "" {
		gc := exec.Command("/bin/sleep", "300") //nolint:gosec // fixed path in a test helper
		if err := gc.Start(); err != nil {
			os.Exit(3)
		}
		_ = os.WriteFile(f, []byte(strconv.Itoa(gc.Process.Pid)), 0o600)
	}
	runFakeChild()
	os.Exit(0)
}

func runFakeChild() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	w := bufio.NewWriter(os.Stdout)
	for sc.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		if req.Method == "notifications/initialized" {
			continue
		}
		var (
			result any
			rerr   *rpcError
		)
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake-child", "version": "1.0.0"},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "echo", "description": "echo a value"},
				map[string]any{"name": "denied-tool", "description": "not allowed"},
			}}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name != "echo" {
				if p.Name == "env" {
					// Reads its own environment, so a test can prove what the
					// bridge actually handed the child.
					result = map[string]any{
						"content": []any{map[string]any{"type": "text", "text": os.Getenv(stringify(p.Arguments["name"]))}},
						"isError": false,
					}
					break
				}
				rerr = &rpcError{Code: -32601, Message: "unknown tool " + p.Name}
				break
			}
			result = map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "hello " + stringify(p.Arguments["value"])}},
				"isError": false,
			}
		case "ping":
			result = map[string]any{}
		default:
			rerr = &rpcError{Code: -32601, Message: "method not found"}
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			resp["error"] = rerr
		} else {
			resp["result"] = result
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			return
		}
		_ = w.Flush()
	}
}

func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func testConfig(t *testing.T, extraEnv map[string]string) Config {
	t.Helper()
	env := map[string]string{"BRIDGE_TEST_CHILD": "1"}
	for k, v := range extraEnv {
		env[k] = v
	}
	return Config{
		Name:         "fake",
		Command:      os.Args[0],
		Args:         []string{"-test.run=TestBridgeHelperProcess"},
		Env:          env,
		Listen:       "127.0.0.1:0",
		AllowedTools: []string{"echo"},
	}
}

func startTestBridge(t *testing.T, cfg Config) *Bridge {
	t.Helper()
	b, err := Start(context.Background(), cfg, Options{LogPath: filepath.Join(t.TempDir(), "child.log")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func post(t *testing.T, url, body string) map[string]json.RawMessage {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body)) //nolint:noctx // test
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func TestBridgeProxiesToolsAndPreservesID(t *testing.T) {
	b := startTestBridge(t, testConfig(t, nil))

	got := post(t, b.URL(), `{"jsonrpc":"2.0","id":"req-42","method":"tools/list"}`)
	if string(got["id"]) != `"req-42"` {
		t.Fatalf("id not preserved: %s", got["id"])
	}
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(got["result"], &listed); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != "echo" {
		t.Fatalf("allow-list not applied: %+v", listed.Tools)
	}

	got = post(t, b.URL(), `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{"value":"world"}}}`)
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(got["result"], &res); err != nil {
		t.Fatalf("decode call: %v (%s)", err, got["result"])
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "world") {
		t.Fatalf("unexpected call result: %s", got["result"])
	}
}

func TestBridgeRefusesDisallowedTool(t *testing.T) {
	b := startTestBridge(t, testConfig(t, nil))
	got := post(t, b.URL(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"denied-tool","arguments":{}}}`)
	if !bytes.Contains(got["error"], []byte("-32602")) {
		t.Fatalf("expected -32602, got %s", got["error"])
	}
}

func TestBridgeHealthReflectsChild(t *testing.T) {
	b := startTestBridge(t, testConfig(t, nil))
	// URL() always ends with the "/mcp" route, so TrimSuffix yields the
	// listener base without relying on strings.Index (which can return -1).
	base := strings.TrimSuffix(b.URL(), "/mcp")
	resp, err := http.Get(base + "/healthz") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health = %d", resp.StatusCode)
	}
}

func TestBridgeCloseKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	b := startTestBridge(t, testConfig(t, map[string]string{"BRIDGE_TEST_GRANDCHILD_FILE": pidFile}))

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("grandchild pid was not written")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The group kill must take the grandchild too.
	for i := 0; i < 40; i++ {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild %d survived bridge Close", pid)
}

func TestLoadConfigsOverlaysStaticFile(t *testing.T) {
	dir := t.TempDir()
	custom := `
name: qdrant
command: uvx
args: ["mcp-server-qdrant"]
env:
  COLLECTION_NAME: ${PROTO_COLLECTION:-scratch}
listen: 127.0.0.1:0
request_timeout: 5s
`
	if err := os.WriteFile(filepath.Join(dir, "qdrant.yaml"), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QDRANT_URL", "http://example.invalid")
	t.Setenv("QDRANT_API_KEY", "test-key")
	cfgs, err := LoadConfigs(dir)
	if err != nil {
		t.Fatalf("LoadConfigs: %v", err)
	}
	var q *Config
	for i := range cfgs {
		if cfgs[i].Name == "qdrant" {
			q = &cfgs[i]
		}
	}
	if q == nil {
		t.Fatal("qdrant config missing")
	}
	if q.Command != "uvx" || len(q.Args) != 1 || q.Args[0] != "mcp-server-qdrant" {
		t.Fatalf("overlay not applied: %+v", q)
	}
	if q.Env["COLLECTION_NAME"] != "scratch" {
		t.Fatalf("env default not expanded: %q", q.Env["COLLECTION_NAME"])
	}
	if q.RequestTimeout != "5s" {
		t.Fatalf("timeout not applied: %q", q.RequestTimeout)
	}
}
