package anon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/james-gibson/lezz.go/internal/debuglease"
)

func testLease(t *testing.T, dir, seed string, ttl time.Duration) (leasePath, allowed string) {
	t.Helper()
	key, allowed, err := debuglease.EnsureDefaultKey(dir)
	if err != nil {
		t.Skipf("ssh-keygen unavailable: %v", err)
	}
	lease := debuglease.New(seed, []string{"observe", "explain"}, ttl, "local")
	leasePath = filepath.Join(dir, "lease.json")
	if err := lease.Write(leasePath); err != nil {
		t.Fatal(err)
	}
	if err := debuglease.Sign(leasePath, key); err != nil {
		t.Fatal(err)
	}
	return leasePath, allowed
}

func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "anon")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

func startAnon(t *testing.T, cfg Config) *Server {
	t.Helper()
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func unixGet(t *testing.T, sock, path string) (int, map[string]any) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", sock)
		},
	}}
	resp, err := client.Get("http://unix" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestDebugSurfaceRequiresValidLease(t *testing.T) {
	dir := t.TempDir()
	leasePath, allowed := testLease(t, dir, "seed-1", time.Minute)
	sock := shortSock(t)
	a := startAnon(t, Config{
		Listen: "127.0.0.1:0", DebugSocket: sock,
		LeasePath: leasePath, AllowedSigners: allowed, SignerIdentity: "local", SeedID: "seed-1",
	})
	if !a.DebugActive() {
		t.Fatal("valid lease did not activate debug")
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket missing: %v", err)
	}
	status, body := unixGet(t, sock, "/status")
	if status != http.StatusOK || body["seed_id"] != "seed-1" || body["surface"] != "debug" {
		t.Fatalf("status: %d %+v", status, body)
	}

	// Replace with a validly signed but expired lease: activation must drop.
	expired := debuglease.New("seed-1", []string{"observe"}, time.Minute, "local")
	expired.NotBefore = time.Now().Add(-2 * time.Minute)
	expired.NotAfter = time.Now().Add(-time.Minute)
	if err := expired.Write(leasePath); err != nil {
		t.Fatal(err)
	}
	key, _, _ := debuglease.EnsureDefaultKey(dir)
	if err := debuglease.Sign(leasePath, key); err != nil {
		t.Fatal(err)
	}
	a.ReloadLease()
	if a.DebugActive() {
		t.Fatal("expired lease left debug active")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("socket was not removed")
	}
}

func TestDebugLeaseExpires(t *testing.T) {
	dir := t.TempDir()
	leasePath, allowed := testLease(t, dir, "seed-1", 200*time.Millisecond)
	a := startAnon(t, Config{
		Listen: "127.0.0.1:0", DebugSocket: shortSock(t),
		LeasePath: leasePath, AllowedSigners: allowed, SignerIdentity: "local", SeedID: "seed-1",
	})
	if !a.DebugActive() {
		t.Fatal("lease did not activate")
	}
	time.Sleep(400 * time.Millisecond)
	if a.DebugActive() {
		t.Fatal("lease did not expire")
	}
}

func TestDebugDisabledWithoutLease(t *testing.T) {
	a := startAnon(t, Config{Listen: "127.0.0.1:0"})
	if a.DebugActive() {
		t.Fatal("debug active without a lease")
	}
}
