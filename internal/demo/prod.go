// Package demo also hosts "lezz prod": a production-shaped cluster that
// presents two logical alarms to its clients, backed by three physical nodes.
//
//	client view          physical
//	-----------          --------
//	alarm_a        ->    alarm-a-a   (primary, owns the registry on 19100)
//	                     alarm-a-b   (mirror, federated with a-a)
//	alarm_b        ->    alarm-c     (never named "c" to a client)
//
// A dashboard arms against alarm_a and falls back to alarm_b. It is not told
// that alarm_a is mirrored, or that alarm_b is physically a third node. That
// invisibility is the design, so neither the registry nor the join payload
// ever advertises a third alarm name.
package demo

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/james-gibson/isotope"
	"github.com/james-gibson/lezz.go/internal/service"
	"github.com/james-gibson/lezz.go/internal/tools"
)

const (
	// ProdRegistryPort is the anchor port the primary A node claims.
	ProdRegistryPort = 19100

	// ProdDefaultBasePort is the first alarm port: A_a, A_b, C take base,
	// base+1, base+2; their adhd MCP servers take base+10..base+12. Fixed
	// ports are deliberate: a rotated child must be reachable at the same
	// endpoints as the generation it replaces.
	ProdDefaultBasePort = 19200

	prodADHDPortOffset   = 10
	prodFederationOffset = 100

	// maxProdBasePort leaves room for the highest derived port (the last
	// federation slot, at base+prodFederationOffset+1).
	maxProdBasePort = 65535 - prodFederationOffset - 1
)

var prodIsotopePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ProdOptions configures RunProd.
type ProdOptions struct {
	// Isotope is the cluster identity. It is stable across rotations: the
	// child registers under the same name, so peers do not see a trust reset.
	Isotope string
	// BasePort is the first alarm port (A_a). A_b and C follow.
	BasePort int
	// Child marks a process installed by a previous generation. It waits for
	// the fixed ports to be released instead of failing on them.
	Child bool
	// Generation is the launchd service generation (g0 manual, g1 first child).
	Generation int
	// Rotate installs the next generation as a service and hands off after
	// the cluster is up. SIGHUP does the same thing at runtime.
	Rotate bool
	// RegistryBind is the host the anchor registry listens on. Empty is
	// loopback (RegistryBindDefault): prod's nodes are all local, and the
	// registry is an unauthenticated directory, so publishing it to the LAN is
	// an operator decision (lezz.go#59). It is carried into a rotated child, so
	// a handoff does not quietly change who can reach the registry.
	RegistryBind string
	// Trust selects the evidence shape the cluster hands its children, and with
	// it where the key material comes from (isotope#26):
	//   secret       a launch secret minted for this cluster (default)
	//   derived      a subkey derived from the parent named by
	//                LEZZ_TRUST_PARENT / LEZZ_TRUST_PARENT_SECRET
	//   certificate  a certificate an operator provisioned, pinned to a root
	// The shape is an init flag because it cannot be changed by editing a
	// rendered config: a rotation must present the same identity, not a new one.
	Trust string
	// RotateTimeout bounds the wait for the child's registry after handoff.
	RotateTimeout time.Duration
}

func (o ProdOptions) normalized() (ProdOptions, error) {
	if o.Isotope == "" {
		o.Isotope = "prod"
	}
	if !prodIsotopePattern.MatchString(o.Isotope) {
		return o, fmt.Errorf("isotope %q must match %s", o.Isotope, prodIsotopePattern)
	}
	if o.BasePort == 0 {
		o.BasePort = ProdDefaultBasePort
	}
	if o.BasePort < 1024 || o.BasePort > maxProdBasePort {
		return o, fmt.Errorf("base port %d out of range (1024..%d)", o.BasePort, maxProdBasePort)
	}
	if err := prodPortLayout(o.BasePort); err != nil {
		return o, err
	}
	if o.RotateTimeout == 0 {
		o.RotateTimeout = 45 * time.Second
	}
	if o.Generation < 0 {
		return o, fmt.Errorf("generation %d must be >= 0", o.Generation)
	}
	switch o.Trust {
	case "":
		o.Trust = TrustSecret
	case TrustSecret, TrustDerived, TrustCertificate:
	default:
		return o, fmt.Errorf("trust %q must be one of: %s, %s, %s",
			o.Trust, TrustSecret, TrustDerived, TrustCertificate)
	}
	if _, _, err := registryHost(o.RegistryBind, ""); err != nil {
		return o, err
	}
	return o, nil
}

// prodNode is one physical node. Role is the logical alarm it presents: "a"
// (the mirrored primary) or "b" (the fallback, physically node C).
type prodNode struct {
	Name      string
	Role      string
	AlarmPort int
	ADHDPort  int
	Federate  bool // A_a and A_b mirror each other through federation
}

// prodNodes is the whole topology in one place. C appears only as role "b".
func prodNodes(base int) []prodNode {
	return []prodNode{
		{
			Name:      "alarm-a-a",
			Role:      "a",
			AlarmPort: base,
			ADHDPort:  base + prodADHDPortOffset,
			Federate:  true,
		},
		{
			Name:      "alarm-a-b",
			Role:      "a",
			AlarmPort: base + 1,
			ADHDPort:  base + prodADHDPortOffset + 1,
			Federate:  true,
		},
		{
			Name:      "alarm-c",
			Role:      "b",
			AlarmPort: base + 2,
			ADHDPort:  base + prodADHDPortOffset + 2,
		},
	}
}

func prodPorts(base int) []int {
	ports := []int{ProdRegistryPort}
	for _, n := range prodNodes(base) {
		ports = append(ports, n.AlarmPort, n.ADHDPort)
	}
	// Federation slots for the mirrored pair.
	for p := base + prodFederationOffset; p <= base+prodFederationOffset+1; p++ {
		ports = append(ports, p)
	}
	return ports
}

// prodPortLayout rejects a base whose derived block would collide with itself
// or with the registry anchor. Without it, base=19000 puts a federation slot
// exactly on 19100 and the cluster cannot start.
func prodPortLayout(base int) error {
	seen := map[int]bool{ProdRegistryPort: true}
	for _, n := range prodNodes(base) {
		for _, p := range []int{n.AlarmPort, n.ADHDPort} {
			if seen[p] {
				return fmt.Errorf("base port %d collides on %d (the registry reserves %d)", base, p, ProdRegistryPort)
			}
			seen[p] = true
		}
	}
	for p := base + prodFederationOffset; p <= base+prodFederationOffset+1; p++ {
		if seen[p] {
			return fmt.Errorf("base port %d collides on %d (the registry reserves %d)", base, p, ProdRegistryPort)
		}
		seen[p] = true
	}
	return nil
}

func prodServiceProfile(generation int) string {
	return fmt.Sprintf("prod-g%d", generation)
}

// prodServiceArgs is the argv a rotated child is installed with. The isotope
// and base port are inherited so the child is the same identity at the same
// endpoints; only the generation increments.
// prodSkill is a transient capability applied to one generation's flow. The
// simplest skill is an environment binding: it lasts for the lifetime of the
// child processes and is replaced with the generation, so nothing is baked into
// the permanent config or the launchd plist.
type prodSkill struct {
	Name string
	Env  map[string]string
}

// prodSkillsFor returns the transient skills a node's flow carries.
//
// tear-ledger seeds adhd's single-use proxy-hop tickets. The file sits under the
// stable per-isotope state dir, so the child of a rotation opens the ledger its
// parent wrote without the path being threaded through launchd.
func prodSkillsFor(root string, n prodNode) []prodSkill {
	return []prodSkill{{
		Name: "tear-ledger",
		Env: map[string]string{
			"ADHD_TEAR_LEDGER_PATH": filepath.Join(root, "tear-ledger-"+n.Name+".json"),
		},
	}}
}

// skillEnviron renders skills as KEY=VALUE entries in a stable order.
func skillEnviron(skills []prodSkill) []string {
	var env []string
	for _, s := range skills {
		for k, v := range s.Env {
			env = append(env, k+"="+v)
		}
	}
	sort.Strings(env)
	return env
}

func prodServiceArgs(o ProdOptions, generation int) []string {
	args := []string{
		"prod",
		"--isotope", o.Isotope,
		"--base-port", strconv.Itoa(o.BasePort),
		"--child",
		"--generation", strconv.Itoa(generation),
	}
	// A rotated child must serve who the parent served: carrying an explicit
	// bind stops a handoff from quietly pulling the registry back to loopback.
	if o.RegistryBind != "" {
		args = append(args, "--registry-bind", o.RegistryBind)
	}
	return args
}

func prodStateDir(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".lezz", "prod", name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create state dir %s: %w", dir, err)
	}
	return dir, nil
}

// prodPlan is the pure description of a prod cluster: everything that can be
// decided without touching the filesystem, starting a process, or loading
// launchd. RunProd turns a plan into processes; fuzz tests assert the plan's
// invariants (the invisibility rules) without side effects.
type prodPlan struct {
	Options            ProdOptions
	Nodes              []prodNode
	Primary            prodNode
	Fallback           prodNode
	Cluster            ClusterInfo
	Isotopes           []isotope.Registration
	FederationBasePort int
	// Trust is the cluster's launch secret. The zero value attests nothing, which
	// is what a plan built by a fuzz target or a test gets.
	Trust clusterTrust
}

func buildProdPlan(opts ProdOptions, host string) (prodPlan, error) {
	opts, err := opts.normalized()
	if err != nil {
		return prodPlan{}, err
	}
	nodes := prodNodes(opts.BasePort)
	primary, fallback := nodes[0], nodes[2]
	// The registry record must name the address the registry actually listens
	// on: with the default loopback bind, publishing the outbound IP would
	// advertise an endpoint nothing answers on (lezz.go#59).
	registryName, _, err := registryHost(opts.RegistryBind, host)
	if err != nil {
		return prodPlan{}, err
	}
	return prodPlan{
		Options:  opts,
		Nodes:    nodes,
		Primary:  primary,
		Fallback: fallback,
		Cluster: ClusterInfo{
			Name:    opts.Isotope,
			AlarmA:  fmt.Sprintf("http://%s:%d", host, primary.AlarmPort),
			AlarmB:  fmt.Sprintf("http://%s:%d", host, fallback.AlarmPort),
			AdhdMCP: fmt.Sprintf("http://%s:%d/mcp", host, primary.ADHDPort),
		},
		Isotopes:           prodIsotopeRecords(opts.Isotope, primary, fallback, host, registryName),
		FederationBasePort: opts.BasePort + prodFederationOffset,
	}, nil
}

// ParseProdArgs parses the argv after the "prod" command. It is exported so
// the command wrapper stays a thin shell and the parser can be fuzzed: every
// accepted argument set must normalize cleanly.
func ParseProdArgs(args []string, output io.Writer) (ProdOptions, error) {
	fs := flag.NewFlagSet("prod", flag.ContinueOnError)
	if output != nil {
		fs.SetOutput(output)
	} else {
		fs.SetOutput(io.Discard)
	}

	isotopeName := fs.String("isotope", "prod", "cluster isotope identity, stable across rotations")
	basePort := fs.Int("base-port", ProdDefaultBasePort, "first alarm port; A_a, A_b, C take base, base+1, base+2")
	rotate := fs.Bool("rotate", false, "install the next generation as a launchd service and hand off")
	child := fs.Bool("child", false, "internal: wait for the previous generation to release its ports")
	generation := fs.Int("generation", 0, "internal: launchd service generation")
	registryBind := fs.String("registry-bind", "", "host for the anchor /cluster registry (default: 127.0.0.1; 0.0.0.0 publishes it to the LAN)")
	trust := fs.String("trust", TrustSecret, "evidence shape: secret (mint), derived (subkey of LEZZ_TRUST_PARENT_SECRET) or certificate (operator-provisioned)")

	if err := fs.Parse(args); err != nil {
		return ProdOptions{}, err
	}
	if fs.NArg() > 0 {
		return ProdOptions{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	// Validate here, not only in RunProd: an accepted argument set must be a
	// usable one. The fuzz target asserts exactly this.
	return ProdOptions{
		Isotope:      *isotopeName,
		BasePort:     *basePort,
		Rotate:       *rotate,
		Child:        *child,
		Generation:   *generation,
		RegistryBind: *registryBind,
		Trust:        *trust,
	}.normalized()
}

// alarmConfig is the template data for one physical alarm. Paths are supplied
// by the caller because they are environment-specific.
func (p prodPlan) alarmConfig(n prodNode, binary, root string) prodAlarmConfig {
	return prodAlarmConfig{
		Port:               n.AlarmPort,
		ListenAddr:         healthListenAddr,
		StateDir:           filepath.Join(root, "state-"+n.Name),
		Binary:             binary,
		ConfigPath:         filepath.Join(root, n.Name+".yaml"),
		LogPath:            filepath.Join(root, n.Name+".log"),
		Federate:           n.Federate,
		ClusterID:          p.Options.Isotope + "-a",
		FederationBasePort: p.FederationBasePort,
		FederationMaxPort:  p.FederationBasePort + 1,
		TrustYAML:          p.Trust.yaml(),
	}
}

// adhdConfig is the client view for one node: alarm_a and alarm_b only.
func (p prodPlan) adhdConfig(n prodNode) prodADHDConfig {
	return prodADHDConfig{
		ADHDPort:   n.ADHDPort,
		AlarmAPort: p.Primary.AlarmPort,
		AlarmBPort: p.Fallback.AlarmPort,
	}
}

// renderTemplate executes a config template into a string. Config templates
// are pure string transforms, so fuzzing them needs no temp directory.
func renderTemplate(tmpl *template.Template, data any) (string, error) {
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// ---- config templates ------------------------------------------------------

// prodAlarmConfigTmpl configures one physical alarm. Targets are the peers it
// watches; the A pair additionally federates so the two share a state view.
var prodAlarmConfigTmpl = template.Must(template.New("prod-smoke-alarm").Parse(`version: "1"
service:
  name: "ocd-smoke-alarm"
  environment: "lezz-prod"
  mode: "background"
  log_level: "warn"
  poll_interval: "5s"
  timeout: "3s"
  max_workers: 4

health:
  enabled: true
  listen_addr: "{{.ListenAddr}}:{{.Port}}"
  self_check: true
  endpoints:
    healthz: "/healthz"
    readyz: "/readyz"
    status: "/status"

runtime:
  lock_file: "{{.StateDir}}/lock"
  state_dir: "{{.StateDir}}"
  baseline_file: "{{.StateDir}}/known-good.json"
  event_history_size: 100
  graceful_shutdown_timeout: "5s"

discovery:
  enabled: true
  interval: "30s"
  include_local_paths:
    - "~/.config/mcp"
    - "~/.config/acp"
  local_proxy_scan:
    enabled: false
  cloud_catalog:
    enabled: false
    urls: []
  llms_txt:
    enabled: false

tuner:
  enabled: true
  advertise: true

alerts:
  aggressive: false
  notify_on_regression_immediately: true
  sinks:
    log:
      enabled: true

{{.TrustYAML}}
# Targets arrive at runtime, by discovery or remote management. Prod does not
# peer the nodes: a dashboard must not learn that alarm_a is
# mirrored or that alarm_b is a separate physical node.
targets: []

remote_agent:
  managed_updates: true
  update:
    strategy: "rolling-single"
    stop_command: "{{.Binary}} ops stop -pid-file {{.StateDir}}/ocd-smoke-alarm.pid"
    start_command: "nohup {{.Binary}} serve -config {{.ConfigPath}} >> {{.LogPath}} 2>&1 & sleep 1"
    verify_command: "{{.Binary}} check -config {{.ConfigPath}}"
    rollback_on_failure: false
    max_wait_for_healthy: "30s"
  safety:
    require_lock: false
{{if .Federate}}
# The two A nodes are one logical alarm: same cluster_id, same slot range.
# A_a claims the introducer slot; A_b follows it and mirrors the state view.
federation:
  enabled: true
  cluster_id: "{{.ClusterID}}"
  base_port: {{.FederationBasePort}}
  max_port: {{.FederationMaxPort}}
  poll_interval: "5s"
  announce_interval: "10s"
  heartbeat_interval: "15s"
  heartbeat_timeout: "45s"
  upstream: ""
  downstream: []
  rank: 0
{{end}}`))

// prodADHDConfigTmpl is the client view: exactly two names. The dashboard is
// not told that alarm_a is mirrored, or that alarm_b is physically node C.
var prodADHDConfigTmpl = template.Must(template.New("prod-adhd").Parse(`mcp_server:
  enabled: true
  addr: ":{{.ADHDPort}}"
smoke_alarm:
  - name: "alarm_a"
    endpoint: "http://127.0.0.1:{{.AlarmAPort}}"
    interval: "5s"
    use_sse: false
  - name: "alarm_b"
    endpoint: "http://127.0.0.1:{{.AlarmBPort}}"
    interval: "5s"
    use_sse: false
`))

type prodAlarmConfig struct {
	Port               int
	ListenAddr         string
	StateDir           string
	Binary             string
	ConfigPath         string
	LogPath            string
	Federate           bool
	ClusterID          string
	FederationBasePort int
	FederationMaxPort  int
	TrustYAML          string // trust block: the cluster secret this alarm verifies with
}

type prodADHDConfig struct {
	ADHDPort   int
	AlarmAPort int
	AlarmBPort int
}

// ---- run -------------------------------------------------------------------

// RunProd launches the three-node prod cluster and blocks until Ctrl+C, SIGHUP
// or context cancellation. SIGHUP (or Rotate) installs the next generation as
// a launchd service and hands the fixed ports over to it.
func RunProd(ctx context.Context, opts ProdOptions) error {
	opts, err := opts.normalized()
	if err != nil {
		return err
	}

	if opts.Child {
		fmt.Println("child generation waiting for the previous cluster to release its ports...")
		if waitErr := waitPortsFree(ctx, prodPorts(opts.BasePort), 90*time.Second); waitErr != nil {
			return waitErr
		}
		removeOlderProdServices(opts.Generation)
	}

	root, err := prodStateDir(opts.Isotope)
	if err != nil {
		return err
	}

	host := outboundIP()
	plan, err := buildProdPlan(opts, host)
	if err != nil {
		return err
	}
	opts = plan.Options
	nodes := plan.Nodes
	primary := plan.Primary

	// The cluster's trust material. It lives in the state dir rather than the
	// argv so a rotated generation inherits the same identity instead of
	// resetting every agent's rung on handoff, and so `ps` cannot read it. The
	// shape was chosen by `--trust` at init; what that produces is loaded here.
	trust, err := loadOrMintTrust(root, opts.Isotope, opts.Trust)
	if err != nil {
		return err
	}
	plan.Trust = trust
	for i := range plan.Isotopes {
		plan.Isotopes[i].Attestation = trust.attestation(plan.Isotopes[i].Endpoint)
	}

	if dirErr := ensureProdDirs(root, nodes); dirErr != nil {
		return dirErr
	}
	if toolErr := ensureTools(ctx, "adhd", "ocd-smoke-alarm"); toolErr != nil {
		return toolErr
	}

	alarmBin, err := tools.Find("ocd-smoke-alarm")
	if err != nil {
		return fmt.Errorf("resolve ocd-smoke-alarm: %w", err)
	}
	if _, findErr := tools.Find("adhd"); findErr != nil {
		return fmt.Errorf("resolve adhd: %w", findErr)
	}

	// --- Write configs ------------------------------------------------------
	configs := make(map[string]string, len(nodes))

	for _, n := range nodes {
		cfg, cfgErr := writeTempConfig(root, n.Name, prodAlarmConfigTmpl, plan.alarmConfig(n, alarmBin, root))
		if cfgErr != nil {
			return cfgErr
		}
		configs[n.Name] = cfg

		if _, cfgErr := writeTempConfig(root, "adhd-"+n.Name, prodADHDConfigTmpl, plan.adhdConfig(n)); cfgErr != nil {
			return cfgErr
		}
	}

	// --- Anchor: the primary A node owns 19100 -----------------------------
	// Only alarm_a and alarm_b are advertised. C has no name of its own. The
	// registry binds loopback unless the operator published it: it is an
	// unauthenticated directory, and prod's nodes are all local (lezz.go#59).
	clusterInfo := plan.Cluster
	registry, err := resolveRegistryListener(plan.Options.RegistryBind)
	if err != nil {
		return err
	}
	discoverySrv, err := startDiscoveryServer(clusterInfo, registry)
	if err != nil {
		return fmt.Errorf("claim registry port %d: %w (another prod cluster or lezz demo owns it; use --rotate to hand off)", ProdRegistryPort, err)
	}

	var mdnsStop func()
	mdnsSrv, mdnsErr := registerRegistryMDNS(registry, opts.Isotope)
	switch {
	case mdnsErr != nil:
		fmt.Fprintf(os.Stderr, "warning: mDNS registration failed: %v\n", mdnsErr)
	case mdnsSrv != nil:
		mdnsStop = func() { mdnsSrv.Shutdown() }
	}

	// --- Start alarms -------------------------------------------------------
	var procs []*exec.Cmd
	stopAll := func() {
		for _, cmd := range procs {
			killProcess(cmd)
		}
		procs = nil
	}
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			stopAll()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = discoverySrv.Shutdown(shutdownCtx)
			cancel()
			if mdnsStop != nil {
				mdnsStop()
			}
		})
	}
	defer release()

	for _, n := range nodes {
		cmd, startErr := startProcess("ocd-smoke-alarm",
			[]string{"serve", "-config", configs[n.Name]},
			filepath.Join(root, n.Name+".log"))
		if startErr != nil {
			return fmt.Errorf("start %s: %w", n.Name, startErr)
		}
		procs = append(procs, cmd)
	}
	for _, n := range nodes {
		fmt.Printf("waiting for %s (role %s) on :%d ...\n", n.Name, n.Role, n.AlarmPort)
		if err := waitReady(ctx, fmt.Sprintf("http://127.0.0.1:%d/healthz", n.AlarmPort)); err != nil {
			return fmt.Errorf("%s readiness: %w", n.Name, err)
		}
	}

	// --- Start adhd per node ------------------------------------------------
	// Each dashboard gets the same two logical names. The one on node C arms
	// against A as well; it is not told that its fallback is itself.
	for _, n := range nodes {
		adhdCfg := filepath.Join(root, "adhd-"+n.Name+".yaml")
		cmd, startErr := startProcessEnv("adhd", []string{
			"--headless",
			"--config", adhdCfg,
			"--log", filepath.Join(root, "adhd-"+n.Name+".jsonl"),
			"--mcp-addr", fmt.Sprintf(":%d", n.ADHDPort),
			"--smoke-alarm", fmt.Sprintf("http://127.0.0.1:%d", primary.AlarmPort),
			// Every node binds all interfaces so the LAN can reach it; the
			// advertise guard requires this explicit statement (adhd#15).
			"--force",
		}, filepath.Join(root, "adhd-"+n.Name+".log"), append(skillEnviron(prodSkillsFor(root, n)), trust.env()...))
		if startErr != nil {
			return fmt.Errorf("start adhd-%s: %w", n.Name, startErr)
		}
		procs = append(procs, cmd)
	}
	for _, n := range nodes {
		if err := waitServiceOpen(ctx, fmt.Sprintf("0.0.0.0:%d", n.ADHDPort), nil); err != nil {
			return fmt.Errorf("adhd-%s readiness: %w", n.Name, err)
		}
	}

	// --- Register the isotope identity -------------------------------------
	trustSummary := registerProdIsotopes(ctx, plan)

	fmt.Printf(`
lezz prod cluster ready

isotope      %s  (%s)
alarm_a      %s/status   <- %s (mirrored by %s, federation :%d)
alarm_b      %s/status   <- fallback node (no separate name is advertised)
adhd-a       %s
registry     %s
remote mgmt  enabled on all three alarms (remote_agent.managed_updates)
generation   g%d
state        %s

rotate       kill -HUP %d   (installs generation g%d as a launchd service)
Ctrl+C to stop
`, opts.Isotope, trustSummary,
		clusterInfo.AlarmA, primary.Name, nodes[1].Name, plan.FederationBasePort,
		clusterInfo.AlarmB,
		clusterInfo.AdhdMCP,
		registry.registryURL(),
		opts.Generation,
		root,
		os.Getpid(), opts.Generation+1)

	if opts.Rotate {
		fmt.Println("rotate requested: installing the next generation...")
		if err := rotateProd(ctx, opts, release); err != nil {
			return err
		}
		return nil
	}

	// --- Wait for shutdown or rotation -------------------------------------
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	for {
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGHUP {
				fmt.Println("\nSIGHUP: rotating the cluster...")
				if err := rotateProd(ctx, opts, release); err != nil {
					fmt.Fprintf(os.Stderr, "rotation failed, cluster still running: %v\n", err)
					continue
				}
				return nil
			}
			fmt.Println("\nshutting down prod cluster...")
			release()
			return nil
		case <-ctx.Done():
			fmt.Println("\ncontext canceled, shutting down prod cluster...")
			release()
			return nil
		}
	}
}

// rotateProd installs the next generation as a launchd service, releases the
// fixed ports, and waits for the child's registry to answer under the same
// isotope identity.
func rotateProd(ctx context.Context, opts ProdOptions, stop func()) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve lezz binary: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(exe); resolveErr == nil {
		exe = resolved
	}

	next := opts.Generation + 1
	profile := prodServiceProfile(next)
	if err := service.InstallArgs("lezz", profile, exe, prodServiceArgs(opts, next)); err != nil {
		return fmt.Errorf("install generation g%d: %w", next, err)
	}
	fmt.Printf("installed generation g%d as launchd service co.james-gibson.lab.lezz.%s\n", next, profile)

	// Release the fixed ports before waiting, or the child can never bind.
	stop()

	if err := waitForRegistry(ctx, opts.Isotope, opts.RotateTimeout); err != nil {
		return err
	}
	fmt.Printf("handoff complete: %s is serving generation g%d\n", opts.Isotope, next)

	if opts.Generation > 0 {
		_ = service.RemoveArgs("lezz", prodServiceProfile(opts.Generation))
	}
	return nil
}

func removeOlderProdServices(generation int) {
	for g := 0; g < generation; g++ {
		_ = service.RemoveArgs("lezz", prodServiceProfile(g))
	}
}

// prodIsotopeRecords is the public identity alone: cluster, alarm_a, alarm_b.
// The mirror and the physical third node are never named here. registryName is
// the host the anchor registry answers on, which is loopback unless the
// operator published it.
func prodIsotopeRecords(name string, primary, fallback prodNode, host, registryName string) []isotope.Registration {
	return []isotope.Registration{
		{
			Name:     name,
			Role:     "cluster",
			Endpoint: fmt.Sprintf("http://%s:%d/cluster", registryName, ProdRegistryPort),
			Protocol: "http",
		},
		{
			Name:     name + "-a",
			Role:     "alarm-a",
			Endpoint: fmt.Sprintf("http://%s:%d", host, primary.AlarmPort),
			Protocol: "http",
		},
		{
			Name:     name + "-b",
			Role:     "alarm-b",
			Endpoint: fmt.Sprintf("http://%s:%d", host, fallback.AlarmPort),
			Protocol: "http",
		},
	}
}

// registerProdIsotopes announces that identity on every alarm, so any of the
// three can answer /isotope/list with the logical view.
func registerProdIsotopes(ctx context.Context, plan prodPlan) string {
	clusterRung := -1
	records := plan.Isotopes

	for _, n := range plan.Nodes {
		client := isotope.NewClient(fmt.Sprintf("http://127.0.0.1:%d", n.AlarmPort))
		for _, reg := range records {
			regCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			rec, err := client.Register(regCtx, reg)
			cancel()
			if err == nil && reg.Role == "cluster" {
				clusterRung = max(clusterRung, rec.TrustRung)
			}
		}
	}
	if clusterRung < 0 {
		return "none registered"
	}
	return fmt.Sprintf("cluster rung %d", clusterRung)
}

// ---- helpers ---------------------------------------------------------------

func ensureProdDirs(root string, nodes []prodNode) error {
	for _, n := range nodes {
		if err := os.MkdirAll(filepath.Join(root, "state-"+n.Name), 0o700); err != nil {
			return fmt.Errorf("create state dir for %s: %w", n.Name, err)
		}
	}
	return nil
}

// waitPortsFree waits until every port can be bound on all interfaces.
func waitPortsFree(ctx context.Context, ports []int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		busy := make([]int, 0, len(ports))
		for _, p := range ports {
			ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
			if err != nil {
				busy = append(busy, p)
				continue
			}
			_ = ln.Close()
		}
		if len(busy) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ports still busy after %v: %v", timeout, busy)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// waitForRegistry waits for a cluster with this isotope name to answer on the
// anchor port — the evidence a rotated child has taken over.
func waitForRegistry(ctx context.Context, isotopeName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/cluster", ProdRegistryPort)
	client := &http.Client{Timeout: time.Second}
	for {
		resp, err := client.Get(url)
		if err == nil {
			var clusters map[string]ClusterInfo
			decodeErr := json.NewDecoder(resp.Body).Decode(&clusters)
			_ = resp.Body.Close()
			if decodeErr == nil {
				if c, ok := clusters[isotopeName]; ok {
					// The registry entry appears before the nodes are healthy.
					// Treat the handoff as complete only when alarm_a answers.
					if c.AlarmA == "" || healthProbe(c.AlarmA+"/healthz") {
						return nil
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("child registry %q did not answer on :%d within %v", isotopeName, ProdRegistryPort, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func healthProbe(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url) //nolint:gosec // url comes from the registry we just read
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}
