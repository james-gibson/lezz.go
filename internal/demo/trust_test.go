package demo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/james-gibson/smoke-alarm/pkg/smokealarm"
)

func testTrust(t *testing.T) clusterTrust {
	t.Helper()
	trust, err := newClusterTrust("demo-4242")
	if err != nil {
		t.Fatalf("newClusterTrust: %v", err)
	}
	return trust
}

// TestClusterTrustMintsASecret: the root of the lab's trust evidence is a random
// secret per cluster, long enough that guessing is not the cheap path
// (isotope#23).
func TestClusterTrustMintsASecret(t *testing.T) {
	trust := testTrust(t)
	if trust.Cluster != "demo-4242" {
		t.Fatalf("cluster = %q", trust.Cluster)
	}
	if len(trust.Secret) != 64 {
		t.Fatalf("secret length = %d, want 64 hex chars (32 bytes)", len(trust.Secret))
	}
	if other := testTrust(t); other.Secret == trust.Secret {
		t.Fatal("two clusters minted the same secret")
	}
}

// TestClusterTrustReachesTheInProcessLinks is the demo wiring: the rendered link
// config must carry the secret the alarm verifies registrations with.
func TestClusterTrustReachesTheInProcessLinks(t *testing.T) {
	trust := testTrust(t)
	cfg, err := buildLinkConfig(linkParams{
		Port:       19101,
		ListenAddr: "127.0.0.1",
		StateDir:   t.TempDir(),
		PeerName:   "alarm-b",
		PeerPort:   19102,
		Advertise:  false,
		TrustYAML:  trust.yaml(),
	})
	if err != nil {
		t.Fatalf("buildLinkConfig: %v", err)
	}
	if got := cfg.Trust.Clusters[trust.Cluster]; got != trust.Secret {
		t.Fatalf("link config trust = %q, want the cluster secret", got)
	}
}

// TestClusterTrustReachesTheGeneratedAlarms covers auto and prod, which spawn
// the alarm as a subprocess from a rendered YAML.
func TestClusterTrustReachesTheGeneratedAlarms(t *testing.T) {
	trust := testTrust(t)

	autoCfg := baseSmokeAlarmConfig{
		Port:               19101,
		ListenAddr:         "127.0.0.1",
		StateDir:           t.TempDir(),
		TrustYAML:          trust.yaml(),
		FederationBasePort: 19110,
		FederationMaxPort:  19111,
	}
	rendered := mustRender(t, baseSmokeAlarmConfigTmpl, autoCfg)
	assertTrustBlock(t, rendered, trust)

	plan, err := buildProdPlan(ProdOptions{Isotope: "lab-42", BasePort: 19200}, "127.0.0.1")
	if err != nil {
		t.Fatalf("buildProdPlan: %v", err)
	}
	plan.Trust = trust
	prodRendered := mustRender(t, prodAlarmConfigTmpl, plan.alarmConfig(plan.Primary, "/bin/ocd-smoke-alarm", t.TempDir()))
	assertTrustBlock(t, prodRendered, trust)
}

// assertTrustBlock checks the rendered YAML both textually and by parsing it with
// the same loader the alarm uses, so a quoting mistake fails here rather than at
// cluster start.
func assertTrustBlock(t *testing.T, rendered string, trust clusterTrust) {
	t.Helper()
	if !strings.Contains(rendered, "trust:") || !strings.Contains(rendered, trust.Cluster) {
		t.Fatalf("rendered config has no trust block for %q:\n%s", trust.Cluster, rendered)
	}
	cfg, err := smokealarm.ParseConfig([]byte(rendered))
	if err != nil {
		t.Fatalf("parse rendered config: %v\n%s", err, rendered)
	}
	if got := cfg.Trust.Clusters[trust.Cluster]; got != trust.Secret {
		t.Fatalf("parsed trust = %q, want the cluster secret", got)
	}
}

// TestClusterTrustEnvReachesTheAgent: adhd signs its registrations from these
// two variables, and only when it has both.
func TestClusterTrustEnvReachesTheAgent(t *testing.T) {
	trust := testTrust(t)
	env := trust.env()
	want := map[string]bool{
		"LEZZ_CLUSTER_ID=" + trust.Cluster:    false,
		"LEZZ_CLUSTER_SECRET=" + trust.Secret: false,
	}
	for _, kv := range env {
		if _, ok := want[kv]; ok {
			want[kv] = true
		}
	}
	for kv, seen := range want {
		if !seen {
			t.Fatalf("env %v missing %q", env, kv)
		}
	}
	if got := (clusterTrust{}).env(); got != nil {
		t.Fatalf("an empty trust must export nothing, got %v", got)
	}
}

// TestClusterTrustSignsItsOwnRegistrations: prod's isotope records are made by
// lezz itself, so lezz signs them with the same secret.
func TestClusterTrustSignsItsOwnRegistrations(t *testing.T) {
	trust := testTrust(t)
	endpoint := "http://127.0.0.1:19200"
	att := trust.attestation(endpoint)
	if att == nil {
		t.Fatal("no attestation for a minted cluster")
	}
	if err := att.Verify(trust.Secret, endpoint, time.Now(), 0); err != nil {
		t.Fatalf("lezz's own attestation does not verify: %v", err)
	}
	if got := (clusterTrust{}).attestation(endpoint); got != nil {
		t.Fatalf("an empty trust signed %+v, want nil", got)
	}
}

// TestLoadOrMintClusterTrustSurvivesRotation: a rotated generation must inherit
// the parent's secret, or every handoff reads as a trust reset.
func TestLoadOrMintClusterTrustSurvivesRotation(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrMintClusterTrust(dir, "lab-42")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	second, err := loadOrMintClusterTrust(dir, "lab-42")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if first.Secret != second.Secret {
		t.Fatalf("secret changed across a reload: %q -> %q", first.Secret, second.Secret)
	}
	info, err := os.Stat(filepath.Join(dir, "cluster-secret"))
	if err != nil {
		t.Fatalf("stat secret file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("secret file mode = %o, want 600", perm)
	}
}

// TestRegistryMDNSNamesTheCluster: isotope refuses to resolve an advertisement
// that names no cluster, so the TXT record is part of the evidence chain.
func TestRegistryMDNSNamesTheCluster(t *testing.T) {
	txt := mdnsTXT("demo-4242")
	joined := strings.Join(txt, ",")
	if !strings.Contains(joined, "cluster=demo-4242") {
		t.Fatalf("TXT = %v, want a cluster field", txt)
	}
	if got := strings.Join(mdnsTXT(""), ","); strings.Contains(got, "cluster=") {
		t.Fatalf("TXT without a cluster = %q, want no empty cluster field", got)
	}
}
