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

// TestNewMintsProjectRoute pins the additive half of ADR 0003: a new seed
// declares project.route with the same value as the legacy seed.id, so a reader
// that knows only one of them resolves the same project.
func TestNewMintsProjectRoute(t *testing.T) {
	f, err := New("local-time", "base")
	if err != nil {
		t.Fatal(err)
	}
	if f.Project.Route == "" {
		t.Fatal("new seed has no project.route")
	}
	if f.Project.Route != f.Seed.ID {
		t.Fatalf("project.route %q != seed.id %q", f.Project.Route, f.Seed.ID)
	}
	if got := f.Route(); got != f.Seed.ID {
		t.Fatalf("Route() = %q, want %q", got, f.Seed.ID)
	}
}

// TestRouteFallsBackToLegacyID covers every seed.json written before ADR 0003:
// it has seed.id and no project.route, and must still resolve.
func TestRouteFallsBackToLegacyID(t *testing.T) {
	f, _ := New("legacy", "base")
	f.Project.Route = ""
	if got := f.Route(); got != f.Seed.ID {
		t.Fatalf("legacy Route() = %q, want seed.id %q", got, f.Seed.ID)
	}
}

// TestRoutePrefersProjectRoute covers a file where the two disagree: the ADR
// says prefer project.route rather than merging.
func TestRoutePrefersProjectRoute(t *testing.T) {
	f, _ := New("modern", "base")
	f.Project.Route = "20261019T210927-09001816"
	if got := f.Route(); got != "20261019T210927-09001816" {
		t.Fatalf("Route() = %q, want project.route", got)
	}
}

func TestValidateRejectsBadRoute(t *testing.T) {
	f, _ := New("x", "base")
	f.Project.Route = "has space"
	if err := f.Validate(); err == nil {
		t.Fatal("expected invalid project.route refusal")
	}
}

func TestMintRouteIsAddressable(t *testing.T) {
	seen := make(map[string]struct{}, 8)
	for i := 0; i < 8; i++ {
		route, err := MintRoute()
		if err != nil {
			t.Fatal(err)
		}
		if !idPattern.MatchString(route) {
			t.Fatalf("minted route %q is not addressable", route)
		}
		if _, dup := seen[route]; dup {
			t.Fatalf("minted route %q twice", route)
		}
		seen[route] = struct{}{}
	}
}
