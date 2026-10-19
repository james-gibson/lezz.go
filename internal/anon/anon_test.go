package anon

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func start(t *testing.T) *Server {
	t.Helper()
	s, err := New(Config{Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func post(t *testing.T, url, body string) (int, map[string]json.RawMessage) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body)) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestInitializeIsGeneric(t *testing.T) {
	s := start(t)
	_, out := post(t, s.URL(), `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	result := string(out["result"])
	if !strings.Contains(result, `"name":"anonymous"`) {
		t.Fatalf("serverInfo: %s", result)
	}
	if strings.Contains(result, "adhd") || strings.Contains(result, "lezz") {
		t.Fatalf("leaks identity: %s", result)
	}
}

func TestToolsListIsEmpty(t *testing.T) {
	s := start(t)
	_, out := post(t, s.URL(), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if string(out["result"]) != `{"tools":[]}` {
		t.Fatalf("tools/list: %s", out["result"])
	}
}

func TestToolCallIsGeneric(t *testing.T) {
	s := start(t)
	_, out := post(t, s.URL(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"adhd.gh.run","arguments":{}}}`)
	errJSON := string(out["error"])
	if !strings.Contains(errJSON, "-32601") {
		t.Fatalf("error: %s", errJSON)
	}
	if strings.Contains(errJSON, "adhd.gh.run") {
		t.Fatalf("response echoes the tool name: %s", errJSON)
	}
}

func TestUnknownMethodIsGeneric(t *testing.T) {
	s := start(t)
	_, out := post(t, s.URL(), `{"jsonrpc":"2.0","id":1,"method":"adhd.proxy","params":{}}`)
	if !strings.Contains(string(out["error"]), "-32601") {
		t.Fatalf("error: %s", out["error"])
	}
}

func TestNotificationAccepted(t *testing.T) {
	s := start(t)
	status, _ := post(t, s.URL(), `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if status != http.StatusAccepted {
		t.Fatalf("status: %d", status)
	}
}

func TestPing(t *testing.T) {
	s := start(t)
	_, out := post(t, s.URL(), `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if string(out["result"]) != `{}` {
		t.Fatalf("ping: %s", out["result"])
	}
}

func TestHealth(t *testing.T) {
	s := start(t)
	resp, err := http.Get(strings.TrimSuffix(s.URL(), "/mcp") + "/healthz") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: %d", resp.StatusCode)
	}
}
