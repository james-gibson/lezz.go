package debuglease

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func requireSSHKeygen(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
}

func setup(t *testing.T) (dir, key, allowed string) {
	t.Helper()
	requireSSHKeygen(t)
	dir = t.TempDir()
	key, allowed, err := EnsureDefaultKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o, want 600", info.Mode().Perm())
	}
	return dir, key, allowed
}

func TestSignVerify(t *testing.T) {
	dir, key, allowed := setup(t)
	lease := New("seed-1", []string{"observe", "explain"}, time.Minute, "local")
	path := filepath.Join(dir, "lease.json")
	if err := lease.Write(path); err != nil {
		t.Fatal(err)
	}
	if err := Sign(path, key); err != nil {
		t.Fatal(err)
	}
	got, err := Verify(path, path+".sig", allowed, "local")
	if err != nil {
		t.Fatal(err)
	}
	if got.SeedID != "seed-1" || !got.Active(time.Now()) {
		t.Fatalf("lease: %+v", got)
	}
}

func TestResignOverwrites(t *testing.T) {
	dir, key, allowed := setup(t)
	lease := New("seed-1", []string{"observe"}, time.Minute, "local")
	path := filepath.Join(dir, "lease.json")
	if err := lease.Write(path); err != nil {
		t.Fatal(err)
	}
	if err := Sign(path, key); err != nil {
		t.Fatal(err)
	}
	lease.Actions = []string{"observe", "shadow"}
	if err := lease.Write(path); err != nil {
		t.Fatal(err)
	}
	if err := Sign(path, key); err != nil {
		t.Fatalf("re-sign: %v", err)
	}
	if _, err := Verify(path, path+".sig", allowed, "local"); err != nil {
		t.Fatalf("re-signed lease did not verify: %v", err)
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	dir, key, allowed := setup(t)
	lease := New("seed-1", []string{"observe"}, time.Minute, "local")
	path := filepath.Join(dir, "lease.json")
	if err := lease.Write(path); err != nil {
		t.Fatal(err)
	}
	if err := Sign(path, key); err != nil {
		t.Fatal(err)
	}
	lease.Actions = []string{"observe", "shadow"}
	if err := lease.Write(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(path, path+".sig", allowed, "local"); err == nil {
		t.Fatal("tampered lease verified")
	}
}

func TestExpiredLeaseIsInactive(t *testing.T) {
	l := New("seed-1", []string{"observe"}, -time.Minute, "local")
	if l.Active(time.Now()) {
		t.Fatal("expired lease reported active")
	}
}

func TestRejectsInvokeAction(t *testing.T) {
	l := New("seed-1", []string{"invoke"}, time.Minute, "local")
	if err := l.Validate(); err == nil {
		t.Fatal("invoke action accepted")
	}
}

func TestWrongIdentityRejected(t *testing.T) {
	dir, key, allowed := setup(t)
	lease := New("seed-1", []string{"observe"}, time.Minute, "local")
	path := filepath.Join(dir, "lease.json")
	if err := lease.Write(path); err != nil {
		t.Fatal(err)
	}
	if err := Sign(path, key); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(path, path+".sig", allowed, "someone-else"); err == nil {
		t.Fatal("wrong identity accepted")
	}
}
