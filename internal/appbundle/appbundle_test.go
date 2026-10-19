package appbundle

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallReadUninstall(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	if _, err := exec.LookPath("codesign"); err != nil {
		t.Skip("codesign unavailable")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	app := filepath.Join(dir, "Lezz.app")
	info, err := Install(Config{
		AppPath:   app,
		Binary:    bin,
		Version:   "v1.2.3",
		Identity:  "-",
		URLScheme: "lezz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.BundleID != DefaultBundleID {
		t.Fatalf("bundle id = %q", info.BundleID)
	}
	if info.Version != "1.2.3" || info.Build != "1" {
		t.Fatalf("version = %q build = %q", info.Version, info.Build)
	}
	if !info.Background {
		t.Fatal("expected background app (LSUIElement)")
	}
	if _, err := os.Stat(filepath.Join(app, "Contents", "MacOS", ExecutableName)); err != nil {
		t.Fatalf("executable missing: %v", err)
	}
	if out, err := exec.Command("plutil", "-lint", filepath.Join(app, "Contents", "Info.plist")).CombinedOutput(); err != nil {
		t.Fatalf("plist lint: %v\n%s", err, out)
	}
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		t.Fatalf("codesign verify: %v\n%s", err, out)
	}
	if err := Uninstall(app, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(app); !os.IsNotExist(err) {
		t.Fatalf("bundle still present: %v", err)
	}
}

func TestInstallRefusesOverwrite(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(t.TempDir(), "Lezz.app")
	cfg := Config{AppPath: app, Binary: bin, Version: "dev", Identity: "-"}
	if _, err := Install(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(cfg); err == nil {
		t.Fatal("second install overwrote without Force")
	}
	cfg.Force = true
	if _, err := Install(cfg); err != nil {
		t.Fatalf("forced reinstall: %v", err)
	}
}

func TestPlistVersions(t *testing.T) {
	cases := map[string]string{
		"dev":        "0.0.0",
		"v1.2.3":     "1.2.3",
		"2.0.0-rc.1": "2.0.0",
		"":           "0.0.0",
	}
	for in, want := range cases {
		short := plistVersions(in)
		if short != want {
			t.Errorf("plistVersions(%q) = %q; want %q", in, short, want)
		}
	}
}

func TestPlistEscaping(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(t.TempDir(), "Lezz.app")
	_, err = Install(Config{
		AppPath:  app,
		Name:     `Lezz & "Lab"`,
		Binary:   bin,
		Version:  "dev",
		Identity: "-",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Uninstall(app, true) })
	if out, err := exec.Command("plutil", "-lint", filepath.Join(app, "Contents", "Info.plist")).CombinedOutput(); err != nil {
		t.Fatalf("plist lint: %v\n%s", err, out)
	}
}
