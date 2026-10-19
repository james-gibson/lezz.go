package seed

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewWriteLoad(t *testing.T) {
	f, err := New("local-time", "base")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.Seed.ID, "-") || f.Seed.Start.Real != 42 || f.Seed.Start.Rung != 0 {
		t.Fatalf("new seed: %+v", f)
	}
	path := filepath.Join(t.TempDir(), "seed.json")
	if err = f.Write(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Seed.ID != f.Seed.ID || got.Project.Name != "local-time" {
		t.Fatalf("round trip: %+v", got)
	}
}

func TestValidateRejectsBadID(t *testing.T) {
	f, _ := New("x", "base")
	f.Seed.ID = "has space"
	if err := f.Validate(); err == nil {
		t.Fatal("expected invalid id")
	}
}

func TestValidateRejectsNonSeedStart(t *testing.T) {
	f, _ := New("x", "base")
	f.Seed.Start.Rung = 4
	if err := f.Validate(); err == nil {
		t.Fatal("expected non-42 start refusal")
	}
}

func TestValidateRejectsNonExternalAuth(t *testing.T) {
	f, _ := New("x", "base")
	f.Auth.Mode = "github"
	if err := f.Validate(); err == nil {
		t.Fatal("expected non-external auth refusal")
	}
}

func TestResolveAuthEnv(t *testing.T) {
	t.Setenv("SEED_TEST_KEY", "present")
	ok, err := ResolveAuth("env:SEED_TEST_KEY")
	if err != nil || !ok {
		t.Fatalf("env ref: %v %v", ok, err)
	}
	if ok, _ := ResolveAuth("env:SEED_TEST_MISSING"); ok {
		t.Fatal("missing env reported present")
	}
}

func TestResolveAuthFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := ResolveAuth("file:" + path); err != nil || !ok {
		t.Fatalf("file ref: %v %v", ok, err)
	}
}

func TestResolveAuthRejectsBareValue(t *testing.T) {
	if _, err := ResolveAuth("just-a-token"); err == nil {
		t.Fatal("expected bare value refusal")
	}
}

func TestResolveAuthKeychainMissing(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("keychain refs are macOS-only")
	}
	ok, err := ResolveAuth("keychain:LEZZ_TEST_MISSING_SEED_9f7c2b")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("missing keychain item reported present")
	}
}
