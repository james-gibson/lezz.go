package githubauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/james-gibson/lezz.go/internal/credential"
)

func testClient(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()
	var tokenCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/device/code", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.Form.Get("client_id") != "client-id" {
			t.Errorf("client_id = %q", r.Form.Get("client_id"))
		}
		if r.Form.Get("scope") != DefaultScopes {
			t.Errorf("scope = %q", r.Form.Get("scope"))
		}
		_ = json.NewEncoder(w).Encode(DeviceCode{
			DeviceCode:      "device-code",
			UserCode:        "ABCD-1234",
			VerificationURI: "https://github.com/login/device",
			ExpiresIn:       900,
			Interval:        1,
		})
	})
	mux.HandleFunc("/access_token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.Form.Get("device_code") != "device-code" {
			t.Errorf("device_code = %q", r.Form.Get("device_code"))
		}
		switch tokenCalls.Add(1) {
		case 1:
			_ = json.NewEncoder(w).Encode(Token{Error: "authorization_pending"})
		case 2:
			_ = json.NewEncoder(w).Encode(Token{Error: "slow_down"})
		default:
			_ = json.NewEncoder(w).Encode(Token{
				AccessToken: "access-token",
				TokenType:   "bearer",
				Scope:       "read:user",
			})
		}
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(Identity{
			ID:      42,
			Login:   "octocat",
			Name:    "The Octocat",
			HTMLURL: "https://github.com/octocat",
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New("client-id")
	c.DeviceCodeURL = srv.URL + "/device/code"
	c.TokenURL = srv.URL + "/access_token"
	c.UserURL = srv.URL + "/user"
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c, srv
}

func TestDeviceFlowAndIdentity(t *testing.T) {
	c, _ := testClient(t)
	dc, err := c.StartDeviceFlow(context.Background(), DefaultScopes)
	if err != nil {
		t.Fatal(err)
	}
	if dc.UserCode != "ABCD-1234" || dc.DeviceCode != "device-code" {
		t.Fatalf("device code = %+v", dc)
	}
	tok, err := c.WaitForToken(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access-token" || tok.Scope != "read:user" {
		t.Fatalf("token = %+v", tok)
	}
	id, err := c.FetchIdentity(context.Background(), tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if id.Login != "octocat" || id.ID != 42 {
		t.Fatalf("identity = %+v", id)
	}
}

func TestSaveAndLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := credential.FileStore{Dir: filepath.Join(home, "secrets")}
	rec, err := Save(store, &Token{AccessToken: "tok", Scope: "read:user"}, &Identity{ID: 7, Login: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Login != "alice" || rec.AuthRef != store.Ref(TokenService, TokenAccount) {
		t.Fatalf("record = %+v", rec)
	}
	got, err := LoadRecord()
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 7 || got.Login != "alice" {
		t.Fatalf("loaded = %+v", got)
	}
	token, err := LoadToken(store)
	if err != nil || token != "tok" {
		t.Fatalf("token = %q, %v", token, err)
	}
	info, err := os.Stat(DefaultRecordPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode = %o", info.Mode().Perm())
	}
	if err := Logout(store); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRecord(); err == nil {
		t.Fatal("record survived logout")
	}
	if _, err := store.Get(TokenService, TokenAccount); err == nil {
		t.Fatal("token survived logout")
	}
}

func TestWaitForTokenExpired(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/access_token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Token{Error: "expired_token"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New("client-id")
	c.TokenURL = srv.URL + "/access_token"
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	_, err := c.WaitForToken(context.Background(), &DeviceCode{DeviceCode: "d", ExpiresIn: 60})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err = %v", err)
	}
}

func TestSaveRejectsEmpty(t *testing.T) {
	if _, err := Save(credential.FileStore{Dir: t.TempDir()}, &Token{}, &Identity{Login: "x"}); err == nil {
		t.Fatal("empty token accepted")
	}
	if _, err := Save(credential.FileStore{Dir: t.TempDir()}, &Token{AccessToken: "t"}, &Identity{}); err == nil {
		t.Fatal("empty identity accepted")
	}
}
