package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---- version normalization --------------------------------------------------

func TestNormalizeVersion(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		want   string
		wantOK bool
	}{
		{"adhd", "adhd v0.8.6\n", "0.8.6", true},
		{"tuner", "tuner 1.2.3\n", "1.2.3", true},
		{"smoke-alarm", "ocd-smoke-alarm v0.11.0\n", "0.11.0", true},
		{"bare semver", "0.9.4", "0.9.4", true},
		{
			"go pseudo-version with dirty metadata",
			"adhd v0.8.7-0.20261018211533-c9bd168451a7+dirty",
			"0.8.7-0.20261018211533-c9bd168451a7",
			true,
		},
		{"commit only", "adhd c9bd168", "", false},
		{"empty", "", "", false},
		{"garbage", "unknown", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeVersion(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("normalizeVersion(%q) ok = %v, want %v", tc.raw, ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("normalizeVersion(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestReleaseIsNewer(t *testing.T) {
	cases := []struct {
		latest, installed string
		want              bool
	}{
		{"0.11.1", "0.8.6", true},
		{"0.11.0", "0.11.0", false},
		{"0.10.0", "0.11.0", false},
		// A source build reports a pseudo-version; the release still wins.
		{"0.11.0", "0.8.7-0.20261018211533-c9bd168451a7", true},
		// Semver prerelease: an rc is older than its release.
		{"0.11.0", "0.11.0-rc1", true},
	}
	for _, tc := range cases {
		got, err := releaseIsNewer(tc.latest, tc.installed)
		if err != nil {
			t.Fatalf("releaseIsNewer(%q, %q): %v", tc.latest, tc.installed, err)
		}
		if got != tc.want {
			t.Errorf("releaseIsNewer(%q, %q) = %v, want %v", tc.latest, tc.installed, got, tc.want)
		}
	}
	if _, err := releaseIsNewer("not-a-version", "1.0.0"); err == nil {
		t.Error("releaseIsNewer accepted an unparseable release version")
	}
}

// ---- upgradeInto ------------------------------------------------------------

// fakeReleaseManager stands in for GitHub: it reports a latest version and
// "downloads" a shell script that prints payload.
type fakeReleaseManager struct {
	latest    string
	latestErr error
	payload   string
	fetchErr  error
	fetches   int
	dest      string
}

func (f *fakeReleaseManager) LatestVersion(context.Context) (string, error) {
	if f.latestErr != nil {
		return "", f.latestErr
	}
	return f.latest, nil
}

func (f *fakeReleaseManager) FetchLatest(_ context.Context, dest string) (string, error) {
	f.fetches++
	f.dest = dest
	if f.fetchErr != nil {
		return "", f.fetchErr
	}
	if err := writeFakeBinary(dest, f.payload); err != nil {
		return "", err
	}
	return f.payload, nil
}

// writeFakeBinary writes an executable that prints exactly line when run with
// -v, which is how every managed tool is asked for its version.
func writeFakeBinary(path, line string) error {
	script := "#!/bin/sh\necho \"" + line + "\"\n"
	return os.WriteFile(path, []byte(script), 0o755) //nolint:gosec // test fixture, executable by design
}

func TestUpgradeInto_ReinstallsOlderTool(t *testing.T) {
	binDir := t.TempDir()
	if err := writeFakeBinary(filepath.Join(binDir, "adhd"), "adhd v0.8.6"); err != nil {
		t.Fatal(err)
	}

	rm := &fakeReleaseManager{latest: "0.11.1", payload: "adhd v0.11.1"}
	tool, _ := Lookup("adhd")
	res, err := upgradeInto(context.Background(), tool, rm, binDir)
	if err != nil {
		t.Fatalf("upgradeInto: %v", err)
	}

	if !res.Changed {
		t.Error("Changed = false, want true")
	}
	if res.Installed != "0.8.6" {
		t.Errorf("Installed = %q, want 0.8.6", res.Installed)
	}
	if res.Latest != "0.11.1" {
		t.Errorf("Latest = %q, want 0.11.1", res.Latest)
	}
	if rm.fetches != 1 {
		t.Errorf("fetches = %d, want 1", rm.fetches)
	}
	if want := filepath.Join(binDir, "adhd"); rm.dest != want {
		t.Errorf("fetched to %q, want %q", rm.dest, want)
	}

	out, err := exec.Command(filepath.Join(binDir, "adhd"), "-v").Output()
	if err != nil {
		t.Fatalf("run replaced binary: %v", err)
	}
	if strings.TrimSpace(string(out)) != "adhd v0.11.1" {
		t.Errorf("replaced binary reports %q, want the downloaded build", out)
	}
}

func TestUpgradeInto_SkipsCurrentTool(t *testing.T) {
	binDir := t.TempDir()
	if err := writeFakeBinary(filepath.Join(binDir, "adhd"), "adhd v0.11.1"); err != nil {
		t.Fatal(err)
	}

	rm := &fakeReleaseManager{latest: "0.11.1", payload: "adhd v0.11.1"}
	tool, _ := Lookup("adhd")
	res, err := upgradeInto(context.Background(), tool, rm, binDir)
	if err != nil {
		t.Fatalf("upgradeInto: %v", err)
	}
	if res.Changed {
		t.Error("Changed = true, want false for an up-to-date tool")
	}
	if rm.fetches != 0 {
		t.Errorf("fetches = %d, want 0 (no download for an up-to-date tool)", rm.fetches)
	}
	if res.Latest != "0.11.1" {
		t.Errorf("Latest = %q, want 0.11.1", res.Latest)
	}
}

func TestUpgradeInto_KeepsNewerThanRelease(t *testing.T) {
	binDir := t.TempDir()
	if err := writeFakeBinary(filepath.Join(binDir, "adhd"), "adhd v0.12.0"); err != nil {
		t.Fatal(err)
	}

	rm := &fakeReleaseManager{latest: "0.11.1", payload: "adhd v0.11.1"}
	tool, _ := Lookup("adhd")
	res, err := upgradeInto(context.Background(), tool, rm, binDir)
	if err != nil {
		t.Fatalf("upgradeInto: %v", err)
	}
	if res.Changed || rm.fetches != 0 {
		t.Errorf("downgraded a newer install: Changed=%v fetches=%d", res.Changed, rm.fetches)
	}
}

func TestUpgradeInto_InstallsMissingTool(t *testing.T) {
	binDir := t.TempDir()

	rm := &fakeReleaseManager{latest: "0.11.1", payload: "adhd v0.11.1"}
	tool, _ := Lookup("adhd")
	res, err := upgradeInto(context.Background(), tool, rm, binDir)
	if err != nil {
		t.Fatalf("upgradeInto: %v", err)
	}
	if !res.Changed || res.Installed != "" || res.Note != "not installed" {
		t.Errorf("missing tool: Changed=%v Installed=%q Note=%q", res.Changed, res.Installed, res.Note)
	}
	if _, statErr := os.Stat(filepath.Join(binDir, "adhd")); statErr != nil {
		t.Errorf("tool was not installed: %v", statErr)
	}
}

func TestUpgradeInto_ReinstallsUnrecognisedBuild(t *testing.T) {
	binDir := t.TempDir()
	// A source build reports a commit hash, not a version: it cannot be compared,
	// so it is replaced with the released build (same rule as lezz self-update).
	if err := writeFakeBinary(filepath.Join(binDir, "adhd"), "adhd c9bd168"); err != nil {
		t.Fatal(err)
	}

	rm := &fakeReleaseManager{latest: "0.11.1", payload: "adhd v0.11.1"}
	tool, _ := Lookup("adhd")
	res, err := upgradeInto(context.Background(), tool, rm, binDir)
	if err != nil {
		t.Fatalf("upgradeInto: %v", err)
	}
	if !res.Changed || res.Note != "unrecognised version" {
		t.Errorf("unrecognised build: Changed=%v Note=%q", res.Changed, res.Note)
	}
}

func TestUpgradeInto_ReportsErrors(t *testing.T) {
	binDir := t.TempDir()
	if err := writeFakeBinary(filepath.Join(binDir, "adhd"), "adhd v0.8.6"); err != nil {
		t.Fatal(err)
	}
	tool, _ := Lookup("adhd")

	t.Run("latest check fails", func(t *testing.T) {
		rm := &fakeReleaseManager{latestErr: errors.New("github down")}
		if _, err := upgradeInto(context.Background(), tool, rm, binDir); err == nil {
			t.Fatal("upgradeInto swallowed a latest-release error")
		}
	})

	t.Run("download fails", func(t *testing.T) {
		rm := &fakeReleaseManager{latest: "0.11.1", fetchErr: errors.New("asset missing")}
		res, err := upgradeInto(context.Background(), tool, rm, binDir)
		if err == nil {
			t.Fatal("upgradeInto swallowed a download error")
		}
		if res.Changed {
			t.Error("Changed = true after a failed download")
		}
	})
}

// ---- selectTools ------------------------------------------------------------

func TestSelectTools(t *testing.T) {
	all, err := selectTools(nil)
	if err != nil {
		t.Fatalf("selectTools(nil): %v", err)
	}
	for _, tool := range all {
		if tool.Name == "lezz" {
			t.Fatal("selectTools(nil) includes lezz; it updates through `lezz update`")
		}
	}
	if len(all) != len(Registry)-1 {
		t.Errorf("selectTools(nil) returned %d tools, want %d", len(all), len(Registry)-1)
	}

	if _, err := selectTools([]string{"adhd"}); err != nil {
		t.Errorf("selectTools(adhd): %v", err)
	}
	if _, err := selectTools([]string{"lezz"}); err == nil {
		t.Error("selectTools(lezz) succeeded; lezz should be rejected")
	}
	if _, err := selectTools([]string{"notarealtool"}); err == nil {
		t.Error("selectTools(unknown) succeeded")
	}
}
