package demo

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/james-gibson/isotope"
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

// TestDerivedChildCannotMintAsParent: the child gets a subkey labeled with its
// own name. Its receipts verify as the child, and it cannot produce one that
// verifies as the parent — which is what makes a derived key safe to hand down.
func TestDerivedChildCannotMintAsParent(t *testing.T) {
	parent := testTrust(t) // "demo-4242"
	child, err := deriveClusterTrust(parent, "demo-4242-kb")
	if err != nil {
		t.Fatalf("deriveClusterTrust: %v", err)
	}
	if child.Secret == parent.Secret {
		t.Fatal("the child was handed the parent's secret")
	}
	if child.Kind != TrustDerived || child.Parent != parent.Cluster {
		t.Fatalf("child = %+v, want a derived child of %q", child, parent.Cluster)
	}

	endpoint := "127.0.0.1:19400"
	att := child.attestation(endpoint)
	if att == nil {
		t.Fatal("derived child signed no receipt")
	}
	childKeyring := isotope.Keyring{Secrets: map[string]string{child.Cluster: child.Secret}}
	if verifyErr := att.VerifyWith(childKeyring, endpoint, time.Now(), 0); verifyErr != nil {
		t.Fatalf("the child's own receipt does not verify: %v", verifyErr)
	}

	// The same MAC checked against the parent's secret, for the parent's name:
	// a child cannot speak as its parent.
	forged := isotope.SignAttestation(child.Secret, parent.Cluster, endpoint, time.Now())
	parentKeyring := isotope.Keyring{Secrets: map[string]string{parent.Cluster: parent.Secret}}
	if verifyErr := forged.VerifyWith(parentKeyring, endpoint, time.Now(), 0); verifyErr == nil {
		t.Fatal("the child minted a receipt that verifies as the parent")
	}

	// Derivation is deterministic: a parent that lost the copy recomputes it.
	again, err := deriveClusterTrust(parent, "demo-4242-kb")
	if err != nil {
		t.Fatalf("re-derive: %v", err)
	}
	if again.Secret != child.Secret {
		t.Fatal("derivation is not deterministic, so a lost child key cannot be recovered")
	}
}

// certificateTrustFixture provisions the certificate shape the way an operator
// does: a root signs a certificate for the cluster, and lezz is handed the
// certificate, the key it names, and the root it pins.
func certificateTrustFixture(t *testing.T, cluster string) (certB64, keyB64, rootName, rootKeyB64 string) {
	t.Helper()
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate root: %v", err)
	}
	clusterPub, clusterPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate cluster key: %v", err)
	}
	cert, err := isotope.SignCertificate(isotope.Certificate{
		Cluster:   cluster,
		PublicKey: base64.StdEncoding.EncodeToString(clusterPub),
		Issuer:    isotope.PublicSeedCluster,
		Scope:     []string{isotope.ScopeAttest},
	}, rootPriv)
	if err != nil {
		t.Fatalf("sign certificate: %v", err)
	}
	body, err := json.Marshal(cert)
	if err != nil {
		t.Fatalf("marshal certificate: %v", err)
	}
	return base64.StdEncoding.EncodeToString(body),
		base64.StdEncoding.EncodeToString(clusterPriv),
		isotope.PublicSeedCluster,
		base64.StdEncoding.EncodeToString(rootPub)
}

// TestCertificateTrustIsProvisionedAndSurvivesRotation: the certificate shape
// takes its material from the operator, writes it to the state dir, and presents
// the same identity on the next generation. Nothing is minted: a cluster cannot
// certify itself to a root it does not hold.
func TestCertificateTrustIsProvisionedAndSurvivesRotation(t *testing.T) {
	dir := t.TempDir()
	certB64, keyB64, root, rootKey := certificateTrustFixture(t, "lab-42")
	t.Setenv(envTrustCert, certB64)
	t.Setenv(envTrustKey, keyB64)
	t.Setenv(envTrustRoot, root)
	t.Setenv(envTrustRootKey, rootKey)

	trust, err := loadOrMintTrust(dir, "lab-42", TrustCertificate)
	if err != nil {
		t.Fatalf("loadOrMintTrust: %v", err)
	}
	if trust.kind() != TrustCertificate || !trust.ok() {
		t.Fatalf("trust = %+v, want a usable certificate shape", trust)
	}

	// The alarm gets the root to pin, never the cluster's key.
	if rendered := trust.yaml(); !strings.Contains(rendered, "roots:") || !strings.Contains(rendered, rootKey) {
		t.Fatalf("alarm config does not pin the root:\n%s", rendered)
	} else if cfg, parseErr := smokealarm.ParseConfig([]byte("version: \"1\"\n" + rendered)); parseErr != nil {
		t.Fatalf("parse alarm config: %v\n%s", parseErr, rendered)
	} else if got := cfg.Trust.Roots[root]; got != rootKey {
		t.Fatalf("parsed roots[%q] = %q, want the pinned key", root, got)
	}

	// The child gets the certificate and its key, in separate variables.
	env := strings.Join(trust.env(), "\n")
	if !strings.Contains(env, "LEZZ_CLUSTER_CERT=") || !strings.Contains(env, "LEZZ_CLUSTER_KEY=") {
		t.Fatalf("agent env does not carry the certificate shape:\n%s", env)
	}
	if strings.Contains(env, "LEZZ_CLUSTER_SECRET=") {
		t.Fatal("the certificate shape leaked a launch secret into the agent env")
	}

	// The receipts are ed25519 and verify against the pinned root.
	endpoint := "127.0.0.1:19400"
	att := trust.attestation(endpoint)
	if att == nil || att.EffectiveScheme() != isotope.SchemeEd25519 {
		t.Fatalf("attestation = %+v, want ed25519", att)
	}
	rootPub, err := base64.StdEncoding.DecodeString(rootKey)
	if err != nil {
		t.Fatalf("decode root key: %v", err)
	}
	keyring := isotope.Keyring{Roots: map[string]ed25519.PublicKey{root: rootPub}}
	if verifyErr := att.VerifyWith(keyring, endpoint, time.Now(), 0); verifyErr != nil {
		t.Fatalf("certificate receipt does not verify against the pinned root: %v", verifyErr)
	}

	// Rotation: a new generation has no env and a fresh process. The same
	// identity comes back from the state dir, so no agent sees a trust reset.
	t.Setenv(envTrustCert, "")
	t.Setenv(envTrustKey, "")
	t.Setenv(envTrustRoot, "")
	t.Setenv(envTrustRootKey, "")
	rotated, err := loadOrMintTrust(dir, "lab-42", TrustCertificate)
	if err != nil {
		t.Fatalf("rotation: %v", err)
	}
	if !rotated.Key.Equal(trust.Key) {
		t.Fatal("rotation minted a new signing key, which would invalidate the certificate")
	}
	if got := rotated.attestation(endpoint); got == nil {
		t.Fatal("rotation produced no attestation")
	} else if err := got.VerifyWith(keyring, endpoint, time.Now(), 0); err != nil {
		t.Fatalf("rotated receipt does not verify: %v", err)
	}
}

// TestCertificateTrustNeedsProvisioning: with no certificate anywhere, the shape
// is a provisioning error. Minting a secret instead would silently downgrade the
// cluster to the shape the operator asked it to leave.
func TestCertificateTrustNeedsProvisioning(t *testing.T) {
	t.Setenv(envTrustCert, "")
	t.Setenv(envTrustKey, "")
	t.Setenv(envTrustRoot, "")
	t.Setenv(envTrustRootKey, "")
	_, err := loadOrMintTrust(t.TempDir(), "lab-42", TrustCertificate)
	if err == nil {
		t.Fatal("expected a provisioning error without a certificate")
	}
	for _, want := range []string{envTrustCert, envTrustKey, envTrustRoot, envTrustRootKey} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %s", err, want)
		}
	}
}

// TestCertificateTrustRejectsAMismatchedKey: the certificate names the key that
// may sign for the cluster. A key file that is not that pair is a configuration
// error, not a receipt the alarm rejects one round trip later.
func TestCertificateTrustRejectsAMismatchedKey(t *testing.T) {
	dir := t.TempDir()
	certB64, _, root, rootKey := certificateTrustFixture(t, "lab-42")
	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(envTrustCert, certB64)
	t.Setenv(envTrustKey, base64.StdEncoding.EncodeToString(otherKey))
	t.Setenv(envTrustRoot, root)
	t.Setenv(envTrustRootKey, rootKey)
	if _, err := loadOrMintTrust(dir, "lab-42", TrustCertificate); err == nil {
		t.Fatal("a key that is not the certificate's pair was accepted")
	}
}

// TestProdTrustFlagSelectsTheShape: the shape is an init flag, and an unknown
// one is refused rather than defaulting to whatever compiles.
func TestProdTrustFlagSelectsTheShape(t *testing.T) {
	opts, err := ParseProdArgs([]string{"--trust", TrustDerived}, io.Discard)
	if err != nil {
		t.Fatalf("ParseProdArgs: %v", err)
	}
	if opts.Trust != TrustDerived {
		t.Fatalf("trust = %q, want %q", opts.Trust, TrustDerived)
	}
	if opts, err = ParseProdArgs(nil, io.Discard); err != nil {
		t.Fatalf("ParseProdArgs default: %v", err)
	} else if opts.Trust != TrustSecret {
		t.Fatalf("default trust = %q, want %q", opts.Trust, TrustSecret)
	}
	if _, err := ParseProdArgs([]string{"--trust", "whatever"}, io.Discard); err == nil {
		t.Fatal("an unknown trust shape was accepted")
	}
}
