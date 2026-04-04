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
