// Package service manages OS-level daemon registration for lezz tools.
// Currently supports macOS launchd (LaunchAgents); Linux systemd is planned.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/james-gibson/lezz.go/internal/tools"
)

const launchdLabel = "com.james-gibson.%s.%s" // com.james-gibson.<tool>.<profile>

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.BinPath}}</string>
		{{- range .Args}}
		<string>{{.}}</string>
		{{- end}}
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>{{.LogDir}}/{{.Label}}.log</string>
	<key>StandardErrorPath</key>
	<string>{{.LogDir}}/{{.Label}}.err</string>
</dict>
</plist>
`

type plistData struct {
	Label   string
	BinPath string
	Args    []string
	LogDir  string
}

// Install writes the launchd plist and loads it so the service starts immediately.
func Install(t tools.Tool, p tools.DaemonProfile, binPath string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("service install is only supported on macOS (got %s)", runtime.GOOS)
	}

	plistDir, err := launchAgentsDir()
	if err != nil {
		return err
	}

	logDir, err := logDir()
	if err != nil {
		return err
	}

	label := fmt.Sprintf(launchdLabel, t.Name, p.Name)
	plistPath := filepath.Join(plistDir, label+".plist")

	tmpl, err := template.New("plist").Parse(plistTemplate)
	if err != nil {
		return fmt.Errorf("parse plist template: %w", err)
	}

	f, err := os.OpenFile(plistPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // path is derived from os.UserHomeDir + a fixed subdirectory
	if err != nil {
		return fmt.Errorf("create plist %s: %w", plistPath, err)
	}
	defer func() { _ = f.Close() }()

	if err := tmpl.Execute(f, plistData{
		Label:   label,
		BinPath: binPath,
		Args:    p.Args,
		LogDir:  logDir,
	}); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// Unload first in case a previous version is running, ignore errors.
	_ = exec.Command("launchctl", "unload", plistPath).Run() //nolint:gosec // plistPath is derived from trusted home dir

	if out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil { //nolint:gosec // same
		return fmt.Errorf("launchctl load: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

// Remove unloads the service and deletes the plist.
func Remove(t tools.Tool, p tools.DaemonProfile) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("service remove is only supported on macOS (got %s)", runtime.GOOS)
	}

	plistDir, err := launchAgentsDir()
	if err != nil {
		return err
	}

	label := fmt.Sprintf(launchdLabel, t.Name, p.Name)
	plistPath := filepath.Join(plistDir, label+".plist")

	if out, err := exec.Command("launchctl", "unload", plistPath).CombinedOutput(); err != nil { //nolint:gosec // plistPath is derived from trusted home dir
		return fmt.Errorf("launchctl unload: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}

	return nil
}

// Info describes an installed lezz-managed launchd service.
type Info struct {
	Label     string
	PlistPath string
	Running   bool // true if launchctl reports the service is loaded and running
}

// List returns all lezz-managed services currently installed in ~/Library/LaunchAgents.
func List() ([]Info, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("service list is only supported on macOS (got %s)", runtime.GOOS)
	}

	dir, err := launchAgentsDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read LaunchAgents dir: %w", err)
	}

	var services []Info
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "com.james-gibson.") || !strings.HasSuffix(name, ".plist") {
			continue
		}
		label := strings.TrimSuffix(name, ".plist")
		plistPath := filepath.Join(dir, name)
		running := isRunning(label)
		services = append(services, Info{Label: label, PlistPath: plistPath, Running: running})
	}
	return services, nil
}

// Purge unloads and removes all lezz-managed services. It attempts every
// service and returns a combined error for any that fail.
func Purge() error {
	services, err := List()
	if err != nil {
		return err
	}

	var errs []string
	for _, svc := range services {
		_ = exec.Command("launchctl", "unload", svc.PlistPath).Run() //nolint:gosec // path is derived from trusted home dir
		if rmErr := os.Remove(svc.PlistPath); rmErr != nil && !os.IsNotExist(rmErr) {
			errs = append(errs, fmt.Sprintf("remove %s: %v", svc.Label, rmErr))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("purge errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// isRunning reports whether launchctl considers the label loaded.
func isRunning(label string) bool {
	out, err := exec.Command("launchctl", "list", label).CombinedOutput() //nolint:gosec // label is derived from trusted source
	if err != nil {
		return false
	}
	// launchctl list <label> exits 0 and prints JSON when the service is loaded.
	return len(out) > 0
}

// PlistPath returns the expected plist path for a tool/profile pair.
func PlistPath(t tools.Tool, p tools.DaemonProfile) (string, error) {
	dir, err := launchAgentsDir()
	if err != nil {
		return "", err
	}
	label := fmt.Sprintf(launchdLabel, t.Name, p.Name)
	return filepath.Join(dir, label+".plist"), nil
}

func launchAgentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	return dir, nil
}

func logDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".lezz", "logs")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create log dir: %w", err)
	}
	return dir, nil
}
