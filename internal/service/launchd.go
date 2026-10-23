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

const launchdLabel = "co.james-gibson.lab.%s.%s" // co.james-gibson.lab.<tool>.<profile>
const labelPrefix = "co.james-gibson.lab."

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
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key>
		<string>{{.Home}}</string>
		<key>PATH</key>
		<string>{{.Path}}</string>
	</dict>
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
	Home    string
	Path    string
}

// Install writes the launchd plist and loads it so the service starts immediately.
//
// Before writing anything it probes the MCP address the profile claims (see
// CheckEndpoint): a service installed over a port another process owns would be
// green in `lezz service list` and serve nothing. force skips that preflight for
// the deliberate shared-port case — the operator has to say so out loud.
//
// The returned notes are the checks the preflight could not prove; the caller
// prints them. They are advisory: only a proven conflict is returned as an error,
// and then no plist is written.
func Install(t tools.Tool, p tools.DaemonProfile, binPath string, force bool) (notes []string, err error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("service install is only supported on macOS (got %s)", runtime.GOOS)
	}

	if !force {
		notes, err = CheckEndpoint(fmt.Sprintf(launchdLabel, t.Name, p.Name), p.Args)
		if err != nil {
			return notes, err
		}
	}

	return notes, InstallArgs(t.Name, p.Name, binPath, p.Args)
}

// InstallArgs is Install with the program arguments supplied at call time
// instead of taken from a static DaemonProfile. Commands whose arguments are
// only known at runtime use it — `lezz prod` installs its next generation with
// the isotope and base port the current one is running with.
//
// It deliberately skips the CheckEndpoint preflight that Install runs: the
// caller is handing fixed ports over from a generation that is still holding
// them, so a probe would find the process it is replacing and refuse.
func InstallArgs(toolName, profileName, binPath string, args []string) error {
	label := fmt.Sprintf(launchdLabel, toolName, profileName)
	return installLabel(label, binPath, args)
}

func installLabel(label, binPath string, args []string) error {
	plistPath, err := writePlist(label, binPath, args)
	if err != nil {
		return err
	}

	// Unload first in case a previous version is running, ignore errors.
	_ = exec.Command("launchctl", "unload", plistPath).Run() //nolint:gosec // plistPath is derived from trusted home dir

	if out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil { //nolint:gosec // same
		return fmt.Errorf("launchctl load: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

// writePlist renders and writes the launchd definition without loading it.
// It is separate from installLabel so the generated arguments can be inspected
// without starting a service.
func writePlist(label, binPath string, args []string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("service install is only supported on macOS (got %s)", runtime.GOOS)
	}

	plistDir, err := launchAgentsDir()
	if err != nil {
		return "", err
	}

	logDir, err := logDir()
	if err != nil {
		return "", err
	}

	plistPath := filepath.Join(plistDir, label+".plist")

	tmpl, err := template.New("plist").Parse(plistTemplate)
	if err != nil {
		return "", fmt.Errorf("parse plist template: %w", err)
	}

	f, err := os.OpenFile(plistPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // path is derived from os.UserHomeDir + a fixed subdirectory
	if err != nil {
		return "", fmt.Errorf("create plist %s: %w", plistPath, err)
	}
	defer func() { _ = f.Close() }()

	if err := tmpl.Execute(f, plistData{
		Label:   label,
		BinPath: binPath,
		Args:    args,
		LogDir:  logDir,
		Home:    os.Getenv("HOME"),
		Path:    servicePath(),
	}); err != nil {
		return "", fmt.Errorf("write plist: %w", err)
	}

	return plistPath, nil
}

// Remove unloads the service and deletes the plist.
func Remove(t tools.Tool, p tools.DaemonProfile) error {
	return RemoveArgs(t.Name, p.Name)
}

// RemoveArgs unloads and deletes a service whose profile name was chosen at
// runtime (for example a numbered `lezz prod` generation).
func RemoveArgs(toolName, profileName string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("service remove is only supported on macOS (got %s)", runtime.GOOS)
	}

	plistDir, err := launchAgentsDir()
	if err != nil {
		return err
	}

	label := fmt.Sprintf(launchdLabel, toolName, profileName)
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
		if !strings.HasPrefix(name, labelPrefix) || !strings.HasSuffix(name, ".plist") {
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

// ParseLabel extracts the tool and profile name from a lezz launchd label.
// Returns ok=false if the label does not match the expected format.
func ParseLabel(label string) (tool, profile string, ok bool) {
	body, found := strings.CutPrefix(label, labelPrefix)
	if !found {
		return "", "", false
	}
	// body is "<tool>.<profile>"; split on the last dot so tool names with
	// hyphens (e.g. "ocd-smoke-alarm") are preserved correctly.
	idx := strings.LastIndex(body, ".")
	if idx < 0 {
		return "", "", false
	}
	return body[:idx], body[idx+1:], true
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

// servicePath is the PATH a launchd job receives: the managed bin directory
// first, then the installer's PATH. launchd's default PATH omits both, which
// would make a managed tool invisible to the service.
func servicePath() string {
	path := os.Getenv("PATH")
	binDir, err := tools.BinDir()
	if err != nil || binDir == "" {
		return path
	}
	if strings.Contains(path, binDir) {
		return path
	}
	if path == "" {
		return binDir
	}
	return binDir + string(os.PathListSeparator) + path
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
