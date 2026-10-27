// Package appbundle builds a macOS .app wrapper around the lezz binary.
//
// The wrapper gives lezz a stable bundle identity. macOS attributes TCC
// permissions (Local Network, Full Disk Access, …) to the app bundle when it is
// launched through LaunchServices, rather than to the terminal that happened to
// start the CLI. The same identity is the prerequisite for keychain access and
// passkeys.
package appbundle

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBundleID is the reverse-DNS identity macOS associates with lezz.
	DefaultBundleID = "co.james-gibson.lezz"
	// DefaultName is the bundle and display name.
	DefaultName = "Lezz"
	// ExecutableName is the file name inside Contents/MacOS.
	ExecutableName = "lezz"
)

// Config describes an app bundle to build.
type Config struct {
	AppPath      string // exact .app path; default ~/Applications/Lezz.app
	Name         string // CFBundleName/CFBundleDisplayName; default Lezz
	BundleID     string // CFBundleIdentifier; default co.james-gibson.lezz
	Version      string // CFBundleShortVersionString source
	Binary       string // binary to wrap; default the running executable
	Identity     string // codesign identity; "-" for ad-hoc
	Entitlements string // optional codesign entitlements plist
	URLScheme    string // optional CFBundleURLSchemes entry; empty disables
	Dock         bool   // true = LSUIElement false (Dock icon); false = background app
	Force        bool   // replace an existing bundle
}

// Info is the observable state of an installed bundle.
type Info struct {
	Path         string   `json:"path"`
	BundleID     string   `json:"bundle_id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Build        string   `json:"build"`
	Executable   string   `json:"executable"`
	Background   bool     `json:"background"`
	Signature    string   `json:"signature"`
	Authority    string   `json:"authority"`
	TeamID       string   `json:"team_id"`
	Running      []int    `json:"running,omitempty"`
	LocalNetwork string   `json:"local_network"`
	Bonjour      []string `json:"bonjour,omitempty"`
}

// DefaultPath returns ~/Applications/Lezz.app.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Applications", DefaultName+".app"), nil
}

// DefaultIdentity returns CODESIGN_IDENTITY, or "-" for an ad-hoc signature.
func DefaultIdentity() string {
	if id := strings.TrimSpace(os.Getenv("CODESIGN_IDENTITY")); id != "" {
		return id
	}
	return "-"
}

// Install writes the bundle, signs it, and registers it with LaunchServices.
func Install(cfg Config) (Info, error) {
	if runtime.GOOS != "darwin" {
		return Info{}, fmt.Errorf("app bundles are only supported on macOS (got %s)", runtime.GOOS)
	}
	if cfg.Name == "" {
		cfg.Name = DefaultName
	}
	if cfg.BundleID == "" {
		cfg.BundleID = DefaultBundleID
	}
	if cfg.Identity == "" {
		cfg.Identity = DefaultIdentity()
	}
	if cfg.AppPath == "" {
		p, err := DefaultPath()
		if err != nil {
			return Info{}, err
		}
		cfg.AppPath = p
	}
	if cfg.Binary == "" {
		exe, err := os.Executable()
		if err != nil {
			return Info{}, err
		}
		cfg.Binary = exe
	}

	appPath := filepath.Clean(cfg.AppPath)
	if !strings.HasSuffix(appPath, ".app") {
		appPath += ".app"
	}
	src, err := filepath.EvalSymlinks(cfg.Binary)
	if err != nil {
		return Info{}, fmt.Errorf("resolve binary: %w", err)
	}
	if strings.HasPrefix(src, appPath+string(os.PathSeparator)) {
		return Info{}, fmt.Errorf("cannot replace the bundle that is running this command (%s); run app install from a CLI build", appPath)
	}
	if _, err := os.Stat(appPath); err == nil {
		if !cfg.Force {
			return Info{}, fmt.Errorf("%s already exists (use --force)", appPath)
		}
		if pids, _ := RunningPIDs(appPath); len(pids) > 0 {
			if err := Stop(appPath); err != nil {
				return Info{}, err
			}
		}
		if err := os.RemoveAll(appPath); err != nil {
			return Info{}, fmt.Errorf("replace %s: %w", appPath, err)
		}
	}

	macosDir := filepath.Join(appPath, "Contents", "MacOS")
	resourcesDir := filepath.Join(appPath, "Contents", "Resources")
	if err := os.MkdirAll(macosDir, 0o755); err != nil { //nolint:gosec // G301: .app bundle dirs must be world-readable for LaunchServices.
		return Info{}, fmt.Errorf("create bundle: %w", err)
	}
	if err := os.MkdirAll(resourcesDir, 0o755); err != nil { //nolint:gosec // G301: .app bundle dirs must be world-readable for LaunchServices.
		return Info{}, fmt.Errorf("create bundle resources: %w", err)
	}
	if err := copyFile(src, filepath.Join(macosDir, ExecutableName), 0o755); err != nil {
		return Info{}, fmt.Errorf("copy binary: %w", err)
	}

	short := plistVersions(cfg.Version)
	plist := renderInfoPlist(cfg, short)
	if err := os.WriteFile(filepath.Join(appPath, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil { //nolint:gosec // G306: bundle resources must be world-readable.
		return Info{}, fmt.Errorf("write Info.plist: %w", err)
	}
	if err := os.WriteFile(filepath.Join(appPath, "Contents", "PkgInfo"), []byte("APPL????"), 0o644); err != nil { //nolint:gosec // G306: bundle resources must be world-readable.
		return Info{}, fmt.Errorf("write PkgInfo: %w", err)
	}

	if err := Sign(appPath, cfg.BundleID, cfg.Identity, cfg.Entitlements); err != nil {
		return Info{}, err
	}
	_ = Register(appPath)
	return ReadInfo(appPath)
}

// Sign codesigns the bundle. An empty identity means ad-hoc.
func Sign(appPath, bundleID, identity, entitlements string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("codesign is only available on macOS")
	}
	if identity == "" {
		identity = "-"
	}
	args := []string{"--force", "--deep", "--sign", identity, "--identifier", bundleID}
	if entitlements != "" {
		if _, err := os.Stat(entitlements); err != nil {
			return fmt.Errorf("entitlements: %w", err)
		}
		args = append(args, "--entitlements", entitlements)
	}
	args = append(args, appPath)
	if out, err := exec.Command("codesign", args...).CombinedOutput(); err != nil { //nolint:gosec // G204: fixed binary, argv-only, no shell.
		return fmt.Errorf("codesign: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", appPath).CombinedOutput(); err != nil { //nolint:gosec // G204: fixed binary, argv-only, no shell.
		return fmt.Errorf("codesign verify: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// BundleExecutable returns the executable CFBundleExecutable names inside the
// bundle. It falls back to ExecutableName for a bundle without a plist entry.
func BundleExecutable(appPath string) (string, error) {
	appPath = filepath.Clean(appPath)
	out, err := exec.Command("plutil", "-convert", "json", "-o", "-", filepath.Join(appPath, "Contents", "Info.plist")).Output() //nolint:gosec // G204: fixed binary, argv-only, no shell.
	if err != nil {
		return "", fmt.Errorf("read %s: %w", filepath.Join(appPath, "Contents", "Info.plist"), err)
	}
	var p struct {
		Executable string `json:"CFBundleExecutable"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return "", fmt.Errorf("parse %s: %w", filepath.Join(appPath, "Contents", "Info.plist"), err)
	}
	if p.Executable == "" {
		return ExecutableName, nil
	}
	return p.Executable, nil
}

// ExecutablePath is the full path of the bundle's executable.
func ExecutablePath(appPath string) (string, error) {
	name, err := BundleExecutable(appPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(appPath), "Contents", "MacOS", name), nil
}

// ReadInfo reads the bundle's Info.plist and signature state.
func ReadInfo(appPath string) (Info, error) {
	if runtime.GOOS != "darwin" {
		return Info{}, fmt.Errorf("app bundles are only supported on macOS (got %s)", runtime.GOOS)
	}
	appPath = filepath.Clean(appPath)
	plistPath := filepath.Join(appPath, "Contents", "Info.plist")
	out, err := exec.Command("plutil", "-convert", "json", "-o", "-", plistPath).Output() //nolint:gosec // G204: fixed binary, argv-only, no shell.
	if err != nil {
		return Info{}, fmt.Errorf("read %s: %w", plistPath, err)
	}
	var p struct {
		BundleID       string   `json:"CFBundleIdentifier"`
		Name           string   `json:"CFBundleName"`
		ShortVersion   string   `json:"CFBundleShortVersionString"`
		Build          string   `json:"CFBundleVersion"`
		Executable     string   `json:"CFBundleExecutable"`
		UIElement      bool     `json:"LSUIElement"`
		LocalNetwork   string   `json:"NSLocalNetworkUsageDescription"`
		Bonjour        []string `json:"NSBonjourServices"`
		URLTypes       []any    `json:"CFBundleURLTypes"`
		MinimumSystem  string   `json:"LSMinimumSystemVersion"`
		ApplicationCat string   `json:"LSApplicationCategoryType"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return Info{}, fmt.Errorf("parse %s: %w", plistPath, err)
	}
	info := Info{
		Path:         appPath,
		BundleID:     p.BundleID,
		Name:         p.Name,
		Version:      p.ShortVersion,
		Build:        p.Build,
		Executable:   p.Executable,
		Background:   p.UIElement,
		LocalNetwork: p.LocalNetwork,
		Bonjour:      p.Bonjour,
	}
	executable, execErr := ExecutablePath(appPath)
	if execErr != nil {
		return Info{}, execErr
	}
	if cs, err := exec.Command("codesign", "-dv", "--verbose=4", executable).CombinedOutput(); err == nil { //nolint:gosec // G204: fixed binary, argv-only, no shell.
		for _, line := range strings.Split(string(cs), "\n") {
			switch {
			case strings.HasPrefix(line, "Authority="):
				if info.Authority == "" {
					info.Authority = strings.TrimPrefix(line, "Authority=")
				}
			case strings.HasPrefix(line, "TeamIdentifier="):
				info.TeamID = strings.TrimPrefix(line, "TeamIdentifier=")
			case strings.HasPrefix(line, "Signature="):
				info.Signature = strings.TrimPrefix(line, "Signature=")
			}
		}
	}
	info.Running, _ = RunningPIDs(appPath)
	return info, nil
}

// Launch opens the app through LaunchServices. No args means the app's default
// (the base cluster). wait blocks until the app exits; newInstance uses open -n.
func Launch(appPath string, args []string, wait, newInstance bool) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("app bundles are only supported on macOS (got %s)", runtime.GOOS)
	}
	if _, err := os.Stat(appPath); err != nil {
		return fmt.Errorf("%s: %w (run: lezz app install)", appPath, err)
	}
	openArgs := make([]string, 0, 8)
	if wait {
		openArgs = append(openArgs, "-W")
	}
	if newInstance {
		openArgs = append(openArgs, "-n")
	}
	openArgs = append(openArgs, "-a", appPath)
	if len(args) > 0 {
		openArgs = append(openArgs, "--args")
		openArgs = append(openArgs, args...)
	}
	if out, err := exec.Command("open", openArgs...).CombinedOutput(); err != nil { //nolint:gosec // G204: fixed binary, argv-only, no shell.
		return fmt.Errorf("open: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall unregisters and removes the bundle. A running app is refused unless
// force is set, in which case it is stopped first.
func Uninstall(appPath string, force bool) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("app bundles are only supported on macOS (got %s)", runtime.GOOS)
	}
	if pids, _ := RunningPIDs(appPath); len(pids) > 0 {
		if !force {
			return fmt.Errorf("lezz is running (pid %v); quit it or use --force", pids)
		}
		if err := Stop(appPath); err != nil {
			return err
		}
	}
	_ = Unregister(appPath)
	return os.RemoveAll(appPath)
}

// RunningPIDs returns the pids whose command is the bundle executable.
// The executable is read from the bundle, not assumed to be lezz: the same
// helpers manage lezz.sea.app.
func RunningPIDs(appPath string) ([]int, error) {
	exe, err := ExecutablePath(appPath)
	if err != nil {
		return nil, err
	}
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		cmd := strings.TrimSpace(fields[1])
		if cmd == exe || strings.HasPrefix(cmd, exe+" ") {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// Stop sends SIGTERM, waits up to three seconds, then SIGKILLs.
func Stop(appPath string) error {
	pids, _ := RunningPIDs(appPath)
	for _, pid := range pids {
		_ = exec.Command("kill", "-TERM", strconv.Itoa(pid)).Run() //nolint:gosec // G204: fixed binary, argv-only, no shell.
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		remaining, _ := RunningPIDs(appPath)
		if len(remaining) == 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	remaining, _ := RunningPIDs(appPath)
	for _, pid := range remaining {
		_ = exec.Command("kill", "-KILL", strconv.Itoa(pid)).Run() //nolint:gosec // G204: fixed binary, argv-only, no shell.
	}
	return nil
}

// Register asks LaunchServices to index the bundle. It is best-effort: open -a
// also registers the app.
func Register(appPath string) error {
	p := lsregisterPath()
	if p == "" {
		return nil
	}
	return exec.Command(p, "-f", appPath).Run() //nolint:gosec // G204: fixed binary, argv-only, no shell.
}

// Unregister removes the bundle from the LaunchServices database.
func Unregister(appPath string) error {
	p := lsregisterPath()
	if p == "" {
		return nil
	}
	return exec.Command(p, "-u", appPath).Run() //nolint:gosec // G204: fixed binary, argv-only, no shell.
}

func lsregisterPath() string {
	const p = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src) //nolint:gosec // G304: src is the user-supplied binary or home-relative bundle path.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) //nolint:gosec // G304: dst is a path inside the bundle being built.
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func renderInfoPlist(cfg Config, short string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	writeString := func(key, value string) {
		fmt.Fprintf(&b, "\t<key>%s</key>\n\t<string>%s</string>\n", esc(key), esc(value))
	}
	writeString("CFBundleDevelopmentRegion", "en")
	writeString("CFBundleExecutable", ExecutableName)
	writeString("CFBundleIdentifier", cfg.BundleID)
	writeString("CFBundleInfoDictionaryVersion", "6.0")
	writeString("CFBundleName", cfg.Name)
	writeString("CFBundleDisplayName", cfg.Name)
	writeString("CFBundlePackageType", "APPL")
	writeString("CFBundleShortVersionString", short)
	// CFBundleVersion is the bundle build, not the release version; lezz ships
	// one build per release, so it is constant.
	writeString("CFBundleVersion", "1")
	writeString("CFBundleSignature", "????")
	writeString("LSMinimumSystemVersion", "13.0")
	writeString("LSApplicationCategoryType", "public.app-category.developer-tools")
	writeString("NSLocalNetworkUsageDescription", "Lezz discovers and connects to lab cluster peers on the local network.")
	writeString("NSHumanReadableCopyright", "Copyright © James Gibson")
	b.WriteString("\t<key>NSHighResolutionCapable</key>\n\t<true/>\n")
	if cfg.Dock {
		b.WriteString("\t<key>LSUIElement</key>\n\t<false/>\n")
	} else {
		b.WriteString("\t<key>LSUIElement</key>\n\t<true/>\n")
	}
	b.WriteString("\t<key>NSBonjourServices</key>\n\t<array>\n")
	b.WriteString("\t\t<string>_lezz-demo._tcp</string>\n")
	b.WriteString("\t</array>\n")
	if cfg.URLScheme != "" {
		b.WriteString("\t<key>CFBundleURLTypes</key>\n\t<array>\n\t\t<dict>\n")
		writeString("CFBundleURLName", cfg.BundleID)
		b.WriteString("\t\t\t<key>CFBundleURLSchemes</key>\n\t\t\t<array>\n")
		fmt.Fprintf(&b, "\t\t\t\t<string>%s</string>\n", esc(cfg.URLScheme))
		b.WriteString("\t\t\t</array>\n\t\t</dict>\n\t</array>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// plistVersions returns the CFBundleShortVersionString for v. The bundle build
// is always "1" (see renderInfoPlist), so there is nothing else to return.
func plistVersions(v string) string {
	v = strings.TrimSpace(strings.TrimPrefix(v, "v"))
	if v == "" || v == "dev" {
		return "0.0.0"
	}
	var b strings.Builder
	for _, r := range v {
		if (r >= '0' && r <= '9') || r == '.' {
			b.WriteRune(r)
			continue
		}
		break
	}
	short := strings.Trim(b.String(), ".")
	if short == "" {
		short = "0.0.0"
	}
	return short
}
