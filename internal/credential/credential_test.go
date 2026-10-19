package credential

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFileStoreRoundTrip(t *testing.T) {
	store := FileStore{Dir: t.TempDir()}
	if err := store.Set("svc", "acct", "secret-value"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Exists("svc", "acct"); err != nil || !ok {
		t.Fatalf("exists = %v, %v", ok, err)
	}
	got, err := store.Get("svc", "acct")
	if err != nil {
		t.Fatal(err)
	}
	if got != "secret-value" {
		t.Fatalf("got %q", got)
	}
	path, err := store.path("svc", "acct")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	if err := store.Delete("svc", "acct"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Exists("svc", "acct"); err != nil || ok {
		t.Fatalf("exists after delete = %v, %v", ok, err)
	}
	if _, err := store.Get("svc", "acct"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if err := store.Delete("svc", "acct"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestFileStoreRef(t *testing.T) {
	store := FileStore{Dir: t.TempDir()}
	ref := store.Ref("svc", "acct")
	if ref == "file:" || len(ref) <= len("file:") {
		t.Fatalf("ref = %q", ref)
	}
	want := filepath.Join(store.Dir, "svc__acct.secret")
	if ref != "file:"+want {
		t.Fatalf("ref = %q, want file:%s", ref, want)
	}
}

func TestNew(t *testing.T) {
	if _, err := New("file"); err != nil {
		t.Fatal(err)
	}
	if _, err := New("bogus"); err == nil {
		t.Fatal("bogus store accepted")
	}
	if runtime.GOOS != "darwin" {
		if _, err := New("keychain"); err == nil {
			t.Fatal("keychain accepted on non-darwin")
		}
	}
}

func TestKeychainMissing(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	_, err := (Keychain{}).Get("LEZZ_TEST_MISSING_9f7c2b", "lezz")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing keychain item: %v", err)
	}
	if ok, err := (Keychain{}).Exists("LEZZ_TEST_MISSING_9f7c2b", "lezz"); err != nil || ok {
		t.Fatalf("exists missing keychain item = %v, %v", ok, err)
	}
}

func TestKeychainRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	if os.Getenv("LEZZ_TEST_KEYCHAIN") != "1" {
		t.Skip("set LEZZ_TEST_KEYCHAIN=1 to write to the login keychain")
	}
	service := fmt.Sprintf("LEZZ_TEST_%d", time.Now().UnixNano())
	store := Keychain{}
	t.Cleanup(func() { _ = store.Delete(service, "lezz") })
	if err := store.Set(service, "lezz", "test-token"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(service, "lezz")
	if err != nil || got != "test-token" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if err := store.Delete(service, "lezz"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(service, "lezz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
}
