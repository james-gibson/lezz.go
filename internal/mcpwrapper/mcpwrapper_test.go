package mcpwrapper

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func fakeCache(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var mu sync.Mutex
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pr":
			mu.Lock()
			query = r.URL.RawQuery
			mu.Unlock()
			w.Header().Set("X-Cache", "cache")
			_, _ = w.Write([]byte(`{"number":139,"title":"cached"}`))
		case "/status":
			_, _ = w.Write([]byte(`{"hits":3,"misses":1,"fetches":1,"entries":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &query
}

func post(t *testing.T, url, body string) map[string]json.RawMessage {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body)) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func toolText(t *testing.T, m map[string]json.RawMessage) string {
	t.Helper()
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(m["result"], &r); err != nil {
		t.Fatalf("decode tool result: %v (%s)", err, m["result"])
	}
	if len(r.Content) == 0 {
		t.Fatalf("no content in %s", m["result"])
	}
	return r.Content[0].Text
}

func startWrapper(t *testing.T, cacheURL string) *Server {
	t.Helper()
	w, err := New(Config{Name: "pr", Listen: "127.0.0.1:0", CacheURL: cacheURL})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestInitializeAndToolsList(t *testing.T) {
	cache, _ := fakeCache(t)
	w := startWrapper(t, cache.URL)
	init := post(t, w.URL(), `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if !strings.Contains(string(init["result"]), "lezz-mcp-pr") {
		t.Fatalf("initialize: %s", init["result"])
	}
	// The greeting is the lab's MOTD: a workspace user's first message should say
	// what trust means here, and point at something they can check.
	if !strings.Contains(string(init["result"]), "Don't Panic") {
		t.Fatalf("initialize carries no MOTD: %s", init["result"])
	}
	list := post(t, w.URL(), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	for _, name := range []string{"pr.get", "pr.status"} {
		if !strings.Contains(string(list["result"]), name) {
			t.Fatalf("tools/list missing %s: %s", name, list["result"])
		}
	}
}

func TestToolPRGet(t *testing.T) {
	cache, query := fakeCache(t)
	w := startWrapper(t, cache.URL)
	res := post(t, w.URL(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pr.get","arguments":{"repo":"james-gibson/local-time","number":139,"kind":"view"}}}`)
	if !strings.Contains(toolText(t, res), `"number":139`) {
		t.Fatalf("pr.get: %s", res["result"])
	}
	if !strings.Contains(*query, "repo=james-gibson%2Flocal-time") || !strings.Contains(*query, "number=139") {
		t.Fatalf("query: %q", *query)
	}
}

func TestToolPRStatus(t *testing.T) {
	cache, _ := fakeCache(t)
	w := startWrapper(t, cache.URL)
	res := post(t, w.URL(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pr.status","arguments":{}}}`)
	if !strings.Contains(toolText(t, res), `"hits":3`) {
		t.Fatalf("pr.status: %s", res["result"])
	}
}

func TestUnknownTool(t *testing.T) {
	cache, _ := fakeCache(t)
	w := startWrapper(t, cache.URL)
	res := post(t, w.URL(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"queue.enqueue","arguments":{}}}`)
	if !strings.Contains(string(res["error"]), "-32602") {
		t.Fatalf("unknown tool: %s", res["error"])
	}
}

func TestRejectsBadRepo(t *testing.T) {
	cache, _ := fakeCache(t)
	w := startWrapper(t, cache.URL)
	res := post(t, w.URL(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pr.get","arguments":{"repo":"../../etc/passwd","number":1}}}`)
	if !strings.Contains(string(res["error"]), "-32602") {
		t.Fatalf("bad repo: %s", res["error"])
	}
}

func TestNotificationAccepted(t *testing.T) {
	cache, _ := fakeCache(t)
	w := startWrapper(t, cache.URL)
	resp, err := http.Post(w.URL(), "application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification status = %d", resp.StatusCode)
	}
}
