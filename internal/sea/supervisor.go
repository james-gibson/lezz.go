//
//  supervisor.go
//  lezz.go
//
//  `lezz sea run` starts lezz.sea as a child with a private stdin/stdout pipe.
//  The app asks for isotopes; this supervisor writes the derived configs under
//  its own lab root, starts the alarms (and adhd), and stops everything when
//  the app exits.
//
//  The app never sends a command line. A request carries the derived YAML and a
//  cluster slug; the supervisor builds the argv, and it owns the directory
//  those configs and pid files live in. The app's sandbox container is
//  deliberately not shared: TCC keeps other processes out of it.
//

package sea

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/james-gibson/lezz.go/internal/appbundle"
	"github.com/james-gibson/lezz.go/internal/tools"
)

// LabRootEnv tells the app where lezz.go will write the lab. The app derives
// the configs with runtime paths under this root.
const LabRootEnv = "LEZZ_SEA_LAB_ROOT"

// RunOptions configures one supervisor run.
type RunOptions struct {
	// AppPath is the installed (or locally built) lezz.sea.app.
	AppPath string
	// ClustersRoot is the directory lezz.go owns for configs, state and pid
	// files. Empty means ~/.lezz/sea/Clusters.
	ClustersRoot string
	// LogPath receives the app's stderr and its non-protocol stdout.
	LogPath string
	// Out and Err receive the supervisor's own progress and errors.
	Out io.Writer
	Err io.Writer
	// FindTool is injectable for tests; nil means tools.Find.
	FindTool func(string) (string, error)
}

type child struct {
	cmd     *exec.Cmd
	cluster string
	pidPath string
	kind    string
}

type supervisor struct {
	opts     RunOptions
	binder   *Binder
	stdin    io.WriteCloser
	writeMu  sync.Mutex
	children map[string]*child // keyed by pid path
	lost     bool
}

// Run supervises one lezz.sea process until it exits or ctx is canceled.
func Run(ctx context.Context, opts RunOptions) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("lezz sea run is only supported on macOS (got %s)", runtime.GOOS)
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Err == nil {
		opts.Err = os.Stderr
	}
	if opts.FindTool == nil {
		opts.FindTool = tools.Find
	}

	root := strings.TrimSpace(opts.ClustersRoot)
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home directory: %w", err)
		}
		root = filepath.Join(home, ".lezz", "sea", "Clusters")
	}
	if mkErr := os.MkdirAll(root, 0o750); mkErr != nil {
		return fmt.Errorf("create lab root %s: %w", root, mkErr)
	}
	binder, err := NewBinder(root)
	if err != nil {
		return err
	}

	exe, err := appbundle.ExecutablePath(opts.AppPath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", opts.AppPath, err)
	}
	if _, statErr := os.Stat(exe); statErr != nil {
		return fmt.Errorf("%s: %w (run: lezz sea install)", exe, statErr)
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}

	appLog, logErr := openLog(opts.LogPath)
	if logErr != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return logErr
	}
	defer func() { _ = appLog.Close() }()

	cmd := exec.Command(exe) //nolint:gosec // executable is the app bundle's own binary, resolved from the app path
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = appLog
	cmd.Env = append(childEnv(), ChannelEnv+"=1", LabRootEnv+"="+binder.Root)
	setProcGroup(cmd)

	if startErr := cmd.Start(); startErr != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return fmt.Errorf("start %s: %w", exe, startErr)
	}
	_ = stdinR.Close()
	_ = stdoutW.Close()

	s := &supervisor{
		opts:     opts,
		binder:   binder,
		stdin:    stdinW,
		children: make(map[string]*child),
	}
	defer func() {
		_ = stdinW.Close()
		_ = stdoutR.Close()
	}()

	messages := make(chan Message, 16)
	go s.readLoop(stdoutR, messages)

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	s.progressf("lezz.sea channel: %s", exe)
	s.progressf("lab root: %s", binder.Root)
	s.progressf("lezz.sea is a viewer until it asks for an isotope; Ctrl-C stops the lab")

	for {
		select {
		case <-ctx.Done():
			s.cleanup()
			stopApp(cmd)
			waitForExit(cmd, waitCh)
			return ctx.Err()

		case err := <-waitCh:
			s.cleanup()
			return err

		case msg, ok := <-messages:
			if !ok {
				s.cleanup()
				stopApp(cmd)
				waitForExit(cmd, waitCh)
				return nil
			}
			s.handle(msg)
		}
	}
}

func (s *supervisor) readLoop(stdout io.Reader, out chan<- Message) {
	defer close(out)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		msg, err := Decode([]byte(line))
		if err != nil {
			s.logLine("ignoring non-protocol output: " + line)
			continue
		}
		out <- msg
	}
}

func (s *supervisor) handle(msg Message) {
	switch msg.Type {
	case "hello":
		s.progressf("connected: %s", msg.Version)
	case "request":
		s.reply(s.dispatch(msg))
	default:
		s.logLine(fmt.Sprintf("ignoring message type %q", msg.Type))
	}
}

func (s *supervisor) dispatch(msg Message) Message {
	if msg.V != 0 && msg.V != ProtocolVersion {
		return ReplyError(msg.ID, fmt.Sprintf("unsupported channel version %d", msg.V))
	}

	var err error
	switch msg.Want {
	case WantStart:
		err = s.start(msg)
	case WantStop:
		err = s.stop(msg)
	case WantADHD:
		return s.startADHD(msg)
	case WantReveal:
		return s.reveal(msg)
	default:
		err = fmt.Errorf("unknown request %q", msg.Want)
	}
	if err != nil {
		return ReplyError(msg.ID, err.Error())
	}
	return ReplyOK(msg.ID)
}

func (s *supervisor) start(msg Message) error {
	if !safeName(msg.Cluster) {
		return fmt.Errorf("start: cluster name %q is not a slug", msg.Cluster)
	}
	if len(msg.Members) == 0 {
		return fmt.Errorf("start: no members")
	}
	if len(msg.Members) > 8 {
		return fmt.Errorf("start: at most 8 members")
	}

	alarm, err := s.opts.FindTool("ocd-smoke-alarm")
	if err != nil {
		return fmt.Errorf("%w — install it with lezz install ocd-smoke-alarm", err)
	}

	base := filepath.Join(s.binder.Root, msg.Cluster)
	if mkErr := os.MkdirAll(base, 0o750); mkErr != nil {
		return fmt.Errorf("create %s: %w", base, mkErr)
	}

	started := make([]*child, 0, len(msg.Members))
	for index, member := range msg.Members {
		name := member.Name
		if name == "" {
			name = fmt.Sprintf("member-%d", index+1)
		}
		if !safeName(name) {
			s.stopChildren(started)
			return fmt.Errorf("start: member name %q is not a slug", name)
		}
		if member.Config == "" || len(member.Config) > 1<<20 {
			s.stopChildren(started)
			return fmt.Errorf("start: %s has no config (or one larger than 1 MiB)", name)
		}

		dir := filepath.Join(base, name)
		if mkErr := os.MkdirAll(filepath.Join(dir, "state"), 0o750); mkErr != nil {
			s.stopChildren(started)
			return fmt.Errorf("create %s: %w", dir, mkErr)
		}
		configPath := filepath.Join(dir, "config.yaml")
		if writeErr := os.WriteFile(configPath, []byte(member.Config), 0o600); writeErr != nil {
			s.stopChildren(started)
			return fmt.Errorf("write %s: %w", configPath, writeErr)
		}
		if _, validErr := s.binder.Config(configPath); validErr != nil {
			_ = os.Remove(configPath)
			s.stopChildren(started)
			return validErr
		}

		logPath := filepath.Join(dir, "alarm.log")
		pidPath := filepath.Join(dir, "ocd-smoke-alarm.pid")
		cmd, startErr := startCommand(alarm, []string{"serve", "--config=" + configPath, "--mode=headless"}, logPath)
		if startErr != nil {
			s.stopChildren(started)
			return startErr
		}
		entry := &child{cmd: cmd, cluster: msg.Cluster, pidPath: pidPath, kind: "alarm"}
		s.children[pidPath] = entry
		started = append(started, entry)
	}

	s.progressf("started %s (%d member(s))", msg.Cluster, len(started))
	return nil
}

func (s *supervisor) startADHD(msg Message) Message {
	if msg.Slug == "" || !safeName(msg.Slug) {
		return ReplyError(msg.ID, "adhd: slug is required")
	}
	if len(msg.HealthPorts) == 0 || len(msg.HealthPorts) > 16 {
		return ReplyError(msg.ID, "adhd: 1–16 health ports are required")
	}
	for _, port := range msg.HealthPorts {
		if port < 1 || port > 65535 {
			return ReplyError(msg.ID, fmt.Sprintf("adhd: invalid health port %d", port))
		}
	}
	if msg.GherkinDirectory != "" && !filepath.IsAbs(msg.GherkinDirectory) {
		return ReplyError(msg.ID, "adhd: gherkinDirectory must be absolute")
	}

	adhd, err := s.opts.FindTool("adhd")
	if err != nil {
		return ReplyError(msg.ID, fmt.Sprintf("%v — install it with lezz install adhd", err))
	}

	port, err := freePort(9190, 9289)
	if err != nil {
		return ReplyError(msg.ID, "adhd: "+err.Error())
	}

	dir := filepath.Join(s.binder.Root, msg.Slug)
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return ReplyError(msg.ID, fmt.Sprintf("adhd: create %s: %v", dir, mkErr))
	}
	configPath := filepath.Join(dir, "adhd.yaml")
	logPath := filepath.Join(dir, "adhd.log")
	pidPath := filepath.Join(dir, "adhd.pid")

	config := adhdConfig(msg.Slug, msg.HealthPorts, port, msg.GherkinDirectory)
	if writeErr := os.WriteFile(configPath, []byte(config), 0o600); writeErr != nil {
		return ReplyError(msg.ID, fmt.Sprintf("adhd: write %s: %v", configPath, writeErr))
	}

	cmd, startErr := startCommand(adhd, []string{"--headless", "--config", configPath}, logPath)
	if startErr != nil {
		return ReplyError(msg.ID, "adhd: "+startErr.Error())
	}
	if writeErr := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o600); writeErr != nil {
		terminateGroup(cmd)
		return ReplyError(msg.ID, fmt.Sprintf("adhd: write %s: %v", pidPath, writeErr))
	}
	s.children[pidPath] = &child{cmd: cmd, cluster: msg.Slug, pidPath: pidPath, kind: "adhd"}
	s.progressf("paired adhd on 127.0.0.1:%d", port)

	reply := ReplyOK(msg.ID)
	reply.ADHD = &ADHD{
		Endpoint:   fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		Port:       port,
		ConfigPath: configPath,
		PIDPath:    pidPath,
	}
	return reply
}

func (s *supervisor) stop(msg Message) error {
	if !safeName(msg.Cluster) {
		return fmt.Errorf("stop: cluster name %q is not a slug", msg.Cluster)
	}
	clusterDir := filepath.Join(s.binder.Root, msg.Cluster)
	var errs []string

	// Anything this supervisor started is stopped by handle first: a pid file
	// read later could already name a recycled process.
	for path, entry := range s.children {
		if entry.cluster != msg.Cluster {
			continue
		}
		terminateGroup(entry.cmd)
		delete(s.children, path)
		_ = os.Remove(path)
	}

	pidFiles, _ := filepath.Glob(filepath.Join(clusterDir, "*", "ocd-smoke-alarm.pid"))
	for _, pidFile := range pidFiles {
		if err := s.stopAlarmPIDFile(pidFile); err != nil {
			errs = append(errs, err.Error())
		}
	}
	adhdPID := filepath.Join(clusterDir, "adhd.pid")
	if _, err := os.Stat(adhdPID); err == nil {
		if err := s.stopPIDFile(adhdPID, "adhd"); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("stop: %s", strings.Join(errs, "; "))
	}
	s.progressf("stopped %s", msg.Cluster)
	return nil
}

func (s *supervisor) reveal(msg Message) Message {
	if !safeName(msg.Cluster) {
		return ReplyError(msg.ID, "reveal: cluster name is not a slug")
	}
	dir := filepath.Join(s.binder.Root, msg.Cluster)
	if _, err := os.Stat(dir); err != nil {
		return ReplyError(msg.ID, fmt.Sprintf("reveal: %v", err))
	}
	if out, err := exec.Command("open", "-R", dir).CombinedOutput(); err != nil { //nolint:gosec // fixed binary; dir is confined to the lab root
		return ReplyError(msg.ID, fmt.Sprintf("reveal: %v %s", err, strings.TrimSpace(string(out))))
	}
	return ReplyOK(msg.ID)
}

func (s *supervisor) stopAlarmPIDFile(pidPath string) error {
	if entry, ok := s.children[pidPath]; ok {
		terminateGroup(entry.cmd)
		delete(s.children, pidPath)
		_ = os.Remove(pidPath)
		return nil
	}

	alarm, err := s.opts.FindTool("ocd-smoke-alarm")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, alarm, "ops", "stop", "-pid-file", pidPath) //nolint:gosec // argv is fixed; pidPath is confined to the lab root
	cmd.Env = childEnv()
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return fmt.Errorf("ops stop %s: %w %s", filepath.Base(pidPath), runErr, strings.TrimSpace(string(out)))
	}
	_ = os.Remove(pidPath)
	return nil
}

func (s *supervisor) stopPIDFile(pidPath, want string) error {
	if entry, ok := s.children[pidPath]; ok {
		terminateGroup(entry.cmd)
		delete(s.children, pidPath)
		_ = os.Remove(pidPath)
		return nil
	}
	pid, err := readPID(pidPath)
	if err != nil {
		_ = os.Remove(pidPath)
		return nil //nolint:nilerr // an unreadable pid file names nothing left to stop
	}
	if name := processName(pid); name != "" && !strings.Contains(name, want) {
		return fmt.Errorf("%s names %s, not %s; not signaling it", filepath.Base(pidPath), name, want)
	}
	if err := terminatePID(pid); err != nil {
		return fmt.Errorf("stop pid %d: %w", pid, err)
	}
	_ = os.Remove(pidPath)
	return nil
}

func (s *supervisor) stopChildren(children []*child) {
	for _, entry := range children {
		terminateGroup(entry.cmd)
		delete(s.children, entry.pidPath)
	}
}

func (s *supervisor) cleanup() {
	for path, entry := range s.children {
		terminateGroup(entry.cmd)
		_ = os.Remove(path)
		delete(s.children, path)
	}
}

func (s *supervisor) reply(msg Message) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.lost {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		s.logLine("encode reply: " + err.Error())
		return
	}
	if _, err := s.stdin.Write(append(data, '\n')); err != nil {
		s.lost = true
		s.logLine("channel lost: " + err.Error())
	}
}

func (s *supervisor) progressf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.opts.Out, format+"\n", args...)
}

func (s *supervisor) logLine(line string) {
	_, _ = fmt.Fprintln(s.opts.Err, line)
}

func openLog(path string) (*os.File, error) {
	if path == "" {
		return os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // --log is an operator choice; the default lives under ~/.lezz
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", path, err)
	}
	return f, nil
}

func childEnv() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PATH=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "PATH=/opt/homebrew/bin:/usr/local/bin:"+path)
}

func startCommand(binary string, args []string, logPath string) (*exec.Cmd, error) {
	cmd := exec.Command(binary, args...) //nolint:gosec // argv is fixed by the supervisor, never by the app
	cmd.Env = childEnv()
	setProcGroup(cmd)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // logPath is under the lab root
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", logPath, err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start %s: %w", filepath.Base(binary), err)
	}
	_ = logFile.Close()
	return cmd, nil
}

func stopApp(cmd *exec.Cmd) {
	terminateGroup(cmd)
}

func waitForExit(cmd *exec.Cmd, waitCh <-chan error) {
	select {
	case err := <-waitCh:
		_ = err
	case <-time.After(3 * time.Second):
		killProcessGroup(cmd)
		_ = <-waitCh
	}
}

func freePort(low, high int) (int, error) {
	for port := low; port <= high; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = listener.Close()
		return port, nil
	}
	return 0, fmt.Errorf("no free port in %d–%d", low, high)
}

func adhdConfig(slug string, healthPorts []int, mcpPort int, gherkin string) string {
	lines := []string{
		"mcp_server:",
		"  enabled: true",
		fmt.Sprintf("  addr: \"127.0.0.1:%d\"", mcpPort),
		"smoke_alarm:",
	}
	for index, port := range healthPorts {
		name := slug
		if len(healthPorts) > 1 {
			name = fmt.Sprintf("%s-%d", slug, index+1)
		}
		lines = append(lines,
			fmt.Sprintf("  - name: %q", name),
			fmt.Sprintf("    endpoint: \"http://127.0.0.1:%d\"", port),
			"    interval: 10s",
		)
	}
	if gherkin != "" {
		lines = append(lines,
			"features:",
			"  binaries:",
			"    - name: ocd-smoke-alarm",
			fmt.Sprintf("      endpoint: \"http://127.0.0.1:%d/status\"", healthPorts[0]),
			"      gherkin_files:",
			fmt.Sprintf("        - %q", filepath.Join(gherkin, "*.feature")),
		)
	}
	return strings.Join(lines, "\n") + "\n"
}

func safeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is confined to the lab root by the caller
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil {
		return 0, fmt.Errorf("parse pid %s: %w", path, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid in %s", path)
	}
	return pid, nil
}
