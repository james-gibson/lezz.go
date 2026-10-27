package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resolved(value string) func(string) (string, error) {
	return func(string) (string, error) { return value, nil }
}

func failingRef(err error) func(string) (string, error) {
	return func(string) (string, error) { return "", err }
}

func TestResolveChildEnvMergesSecretsAtSpawn(t *testing.T) {
	cfg := Config{
		Name:    "fake",
		Env:     map[string]string{"PLAIN": "value"},
		Secrets: map[string]string{"TOKEN": "keychain:LEZZ_GITHUB_TOKEN"},
	}
	env, err := resolveChildEnv(cfg, Options{ResolveSecret: resolved("s3cr3t")})
	if err != nil {
		t.Fatalf("resolveChildEnv: %v", err)
	}
	if env["PLAIN"] != "value" || env["TOKEN"] != "s3cr3t" {
		t.Fatalf("unexpected environment: %#v", env)
	}
}

func TestResolveChildEnvFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		opts Options
		want string
	}{
		{
			name: "no resolver",
			cfg:  Config{Name: "b", Secrets: map[string]string{"TOKEN": "keychain:X"}},
			want: "needs a secret resolver",
		},
		{
			name: "unresolvable ref",
			cfg:  Config{Name: "b", Secrets: map[string]string{"TOKEN": "keychain:X"}},
			opts: Options{ResolveSecret: failingRef(errors.New("credential not found"))},
			want: "resolve secret TOKEN",
		},
		{
			name: "empty value",
			cfg:  Config{Name: "b", Secrets: map[string]string{"TOKEN": "env:EMPTY"}},
			opts: Options{ResolveSecret: resolved("")},
			want: "is empty",
		},
		{
			name: "collision with env",
			cfg: Config{
				Name:    "b",
				Env:     map[string]string{"TOKEN": "literal"},
				Secrets: map[string]string{"TOKEN": "keychain:X"},
			},
			opts: Options{ResolveSecret: resolved("s3cr3t")},
			want: "both env and secrets",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveChildEnv(tc.cfg, tc.opts)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			// The ref is a name, never a value: nothing sensitive is echoed.
			if strings.Contains(err.Error(), "s3cr3t") {
				t.Fatalf("error leaked the secret: %q", err)
			}
		})
	}
}

// The point of the feature: the child gets the credential, the HTTP surface and
// the log do not.
func TestBridgeDeliversSecretToChildOnly(t *testing.T) {
	const secret = "gho_test_secret_value"
	logPath := filepath.Join(t.TempDir(), "child.log")
	cfg := testConfig(t, nil)
	cfg.AllowedTools = []string{"echo", "env"}
	cfg.Secrets = map[string]string{"LEMON_GITHUB_TOKEN": "keychain:LEZZ_GITHUB_TOKEN"}

	b, err := Start(context.Background(), cfg, Options{
		LogPath:       logPath,
		ResolveSecret: resolved(secret),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	got := post(t, b.URL(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"env","arguments":{"name":"LEMON_GITHUB_TOKEN"}}}`)
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if decodeErr := json.Unmarshal(got["result"], &res); decodeErr != nil {
		t.Fatalf("decode call: %v (%s)", decodeErr, got["result"])
	}
	if len(res.Content) == 0 || res.Content[0].Text != secret {
		t.Fatalf("child did not receive the secret: %s", got["result"])
	}

	base := strings.TrimSuffix(b.URL(), "/mcp")
	status, err := http.Get(base + "/status") //nolint:noctx // test
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	body, _ := io.ReadAll(status.Body)
	_ = status.Body.Close()
	if bytes.Contains(body, []byte(secret)) {
		t.Fatalf("/status leaked the secret: %s", body)
	}
	if bytes.Contains(body, []byte("keychain:LEZZ_GITHUB_TOKEN")) {
		t.Fatalf("/status exposed the secret reference: %s", body)
	}

	if _, statErr := os.Stat(logPath); statErr == nil {
		logged, _ := os.ReadFile(logPath) //nolint:gosec // test-owned temp path
		if bytes.Contains(logged, []byte(secret)) {
			t.Fatalf("child log leaked the secret")
		}
	}
}

func TestBridgeRefusesToStartWithoutItsSecret(t *testing.T) {
	cfg := testConfig(t, nil)
	cfg.Secrets = map[string]string{"LEMON_GITHUB_TOKEN": "keychain:LEZZ_GITHUB_TOKEN"}

	_, err := Start(context.Background(), cfg, Options{
		LogPath:       filepath.Join(t.TempDir(), "child.log"),
		ResolveSecret: failingRef(errors.New("credential not found")),
	})
	if err == nil {
		t.Fatal("expected Start to fail when the secret cannot be resolved")
	}
	if !strings.Contains(err.Error(), "LEMON_GITHUB_TOKEN") {
		t.Fatalf("error does not name the missing variable: %v", err)
	}
}
