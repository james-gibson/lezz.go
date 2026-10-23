package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWritePlistCarriesRuntimeArgs is the rotation contract: a child service
// defined at runtime keeps the label stable per generation and carries the
// isotope/base-port arguments, not a static profile.
func TestWritePlistCarriesRuntimeArgs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd is macOS-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	label := "co.james-gibson.lab.lezz.prod-g2"
	path, err := writePlist(label, "/tmp/lezz", []string{
		"prod", "--isotope", "lab-42", "--base-port", "21000",
		"--child", "--generation", "2",
	})
	if err != nil {
		t.Fatalf("writePlist: %v", err)
	}

	wantPath := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	plist := string(data)
	for _, want := range []string{
		"<string>" + label + "</string>",
		"<string>/tmp/lezz</string>",
		"<string>--isotope</string>",
		"<string>lab-42</string>",
		"<string>--base-port</string>",
		"<string>21000</string>",
		"<string>--child</string>",
		"<string>--generation</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<key>HOME</key>",
		"<string>" + home + "</string>",
		"<key>PATH</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q\n%s", want, plist)
		}
	}

	// The same generation must overwrite its own definition, not accumulate
	// duplicates — that is what makes repeated rotations idempotent.
	again, err := writePlist(label, "/tmp/lezz", []string{"prod", "--generation", "2"})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if again != path {
		t.Errorf("rewrite path = %q, want %q", again, path)
	}
}
