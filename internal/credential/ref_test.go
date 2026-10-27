package credential

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveEnv(t *testing.T) {
	t.Setenv("LEZZ_TEST_SECRET", "value-from-env")
	got, err := Resolve("env:LEZZ_TEST_SECRET")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "value-from-env" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveEnvMissing(t *testing.T) {
	os.Unsetenv("LEZZ_TEST_ABSENT")
	if _, err := Resolve("env:LEZZ_TEST_ABSENT"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestResolveFileRequires0600(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "good.secret")
	if err := os.WriteFile(good, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve("file:" + good)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "file-secret" {
		t.Fatalf("trailing newline not trimmed: %q", got)
	}

	loose := filepath.Join(dir, "loose.secret")
	if err := os.WriteFile(loose, []byte("file-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("file:" + loose); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("expected a permission refusal, got %v", err)
	}
}

func TestResolveFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Resolve("file:" + filepath.Join(dir, "absent")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := Resolve("file:" + dir); err == nil {
		t.Fatal("expected a directory to be refused")
	}
}

func TestResolveRejectsBadRefs(t *testing.T) {
	for _, ref := range []string{"", "no-colon", "env:", "vault:secret/thing", "keychain:"} {
		if _, err := Resolve(ref); err == nil {
			t.Fatalf("expected %q to be refused", ref)
		}
	}
}
