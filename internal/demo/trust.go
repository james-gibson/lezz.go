package demo

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/james-gibson/isotope"
)

// The evidence shapes a cluster can hand its children. The shape is chosen when
// the cluster is initialized, because it decides where the key material comes
// from — a minted secret, a subkey derived from a parent, or a certificate an
// operator issued — and it cannot be changed by editing a rendered config.
const (
	// TrustSecret is the launch secret a cluster mints for itself and hands to
	// the alarm and its agents (isotope#23).
	TrustSecret = "secret"
	// TrustDerived is a subkey of a parent cluster's secret, handed to a child
	// so the child registers as itself and cannot mint as the parent.
	TrustDerived = "derived"
	// TrustCertificate is an ed25519 certificate a root issued for the cluster,
	// plus the private key it was issued for. Any verifier that pins the root
	// can check the receipts without holding a secret (isotope#25).
	TrustCertificate = "certificate"
)

// Environment the operator uses to provision the material a shape needs. lezz
// hands separate variables to adhd (LEZZ_CLUSTER_CERT / LEZZ_CLUSTER_KEY), but
// what it *reads* is its own: an operator may run lezz with the same material
// under a different name.
const (
	envTrustParent       = "LEZZ_TRUST_PARENT"
	envTrustParentSecret = "LEZZ_TRUST_PARENT_SECRET" //nolint:gosec // an environment variable name, not a credential
	envTrustCert         = "LEZZ_CLUSTER_CERT"
	envTrustKey          = "LEZZ_CLUSTER_KEY"
	envTrustRoot         = "LEZZ_TRUST_ROOT"
	envTrustRootKey      = "LEZZ_TRUST_ROOT_KEY"
)

// clusterTrust is the launch secret a cluster mints when it starts and hands to
// the processes it trusts: the smoke-alarm instances that verify registrations
// and the agents (adhd) that register with them.
//
// It is the root of the lab's trust evidence (isotope#23). A registration is a
// claim about an endpoint; without a secret, the alarm cannot tell a cluster
// member from a local process that bound the discovery port, so it grants the
// floor. With it, the agent can present a receipt the alarm re-computes from the
// secret it was started with. The secret itself never crosses a socket.
type clusterTrust struct {
	Cluster string
	// Kind is TrustSecret, TrustDerived or TrustCertificate. The zero value is
	// TrustSecret so a trust built by an older caller keeps the shape it had.
	Kind string
	// Secret is the HMAC secret for this cluster: minted for TrustSecret,
	// derived from the parent for TrustDerived.
	Secret string
	// Parent names the cluster whose secret a derived child's key came from.
	Parent string
	// Cert and Key are the certificate shape: the certificate an operator
	// issued, and the private key it names. Key never leaves this process.
	Cert *isotope.Certificate
	Key  ed25519.PrivateKey
	// Root and RootKey are the pinned issuer: the contract test in #26 —
	// naming a root is not trusting it, so the key travels with it.
	Root    string
	RootKey string
}

// newClusterTrust mints a fresh secret for a cluster that is starting now.
func newClusterTrust(cluster string) (clusterTrust, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return clusterTrust{}, fmt.Errorf("mint cluster secret: %w", err)
	}
	return clusterTrust{Cluster: cluster, Kind: TrustSecret, Secret: hex.EncodeToString(buf)}, nil
}

// deriveClusterTrust hands a child cluster a subkey of its parent's secret.
//
// The child registers receipts as itself and cannot mint as the parent, and
// revoking it is dropping one configured entry rather than rotating the parent.
// Derivation is deterministic, so a parent that lost the child's copy recomputes
// it; nothing is persisted for a derived child.
func deriveClusterTrust(parent clusterTrust, child string) (clusterTrust, error) {
	if child == "" {
		return clusterTrust{}, fmt.Errorf("derived trust needs a child cluster")
	}
	if parent.Cluster == "" || parent.Secret == "" || parent.Kind == TrustCertificate {
		return clusterTrust{}, fmt.Errorf("derived trust needs a parent with a secret, got %q (%s)", parent.Cluster, parent.Kind)
	}
	return clusterTrust{
		Cluster: child,
		Kind:    TrustDerived,
		Secret:  isotope.DeriveClusterSecret(parent.Secret, child),
		Parent:  parent.Cluster,
	}, nil
}

// kind is the shape, with the zero value read as a minted secret.
func (t clusterTrust) kind() string {
	if t.Kind == "" {
		return TrustSecret
	}
	return t.Kind
}

// ok reports whether the cluster can attest anything. An incomplete trust is not
// an error at every call site: a cluster with no evidence registers at the
// floor, which is the honest answer rather than a receipt nobody can verify.
func (t clusterTrust) ok() bool {
	if t.Cluster == "" {
		return false
	}
	if t.kind() == TrustCertificate {
		return t.Cert != nil && len(t.Key) == ed25519.PrivateKeySize && t.Root != "" && t.RootKey != ""
	}
	return t.Secret != ""
}

// env is how the cluster hands the secret to a child process it spawns, and what
// adhd reads to sign its registrations.
func (t clusterTrust) env() []string {
	if !t.ok() {
		return nil
	}
	if t.kind() == TrustCertificate {
		// The certificate is public; the key is not. They travel apart so that
		// printing one never prints the other.
		body, err := json.Marshal(t.Cert)
		if err != nil {
			return nil
		}
		return []string{
			"LEZZ_CLUSTER_CERT=" + base64.StdEncoding.EncodeToString(body),
			"LEZZ_CLUSTER_KEY=" + base64.StdEncoding.EncodeToString(t.Key),
		}
	}
	return []string{
		"LEZZ_CLUSTER_ID=" + t.Cluster,
		"LEZZ_CLUSTER_SECRET=" + t.Secret,
	}
}

// yaml is the trust block for a generated alarm config. It is rendered here
// rather than in the template so the secret is quoted once, in one place.
func (t clusterTrust) yaml() string {
	if !t.ok() {
		return "trust:\n  clusters: {}\n  roots: {}\n"
	}
	if t.kind() == TrustCertificate {
		// The alarm pins the root the certificate chains to; it never holds the
		// cluster's key, which is what lets a third party verify the receipt.
		return fmt.Sprintf("trust:\n  clusters: {}\n  roots:\n    %q: %q\n", t.Root, t.RootKey)
	}
	return fmt.Sprintf("trust:\n  clusters:\n    %q: %q\n  roots: {}\n", t.Cluster, t.Secret)
}

// attestation signs endpoint for this cluster, for the registrations lezz makes
// itself (prod's isotope records) rather than a child's.
func (t clusterTrust) attestation(endpoint string) *isotope.Attestation {
	if !t.ok() {
		return nil
	}
	if t.kind() == TrustCertificate {
		att := isotope.SignReceiptEd25519(t.Key, *t.Cert, endpoint, time.Now())
		return &att
	}
	att := isotope.SignAttestation(t.Secret, t.Cluster, endpoint, time.Now())
	return &att
}

// loadOrMintClusterTrust returns the cluster's secret, minting it on first use.
func loadOrMintClusterTrust(stateDir, cluster string) (clusterTrust, error) {
	return loadOrMintTrust(stateDir, cluster, TrustSecret)
}

// loadOrMintTrust returns the evidence a cluster hands its children, in the
// shape chosen when it was initialized (`lezz prod --trust`).
//
// The shape decides where the material comes from, so it is a flag rather than a
// guess: a secret is minted, a derived child's key is computed from its parent's
// secret, and a certificate is provisioned by the operator (only a root can
// issue one).
//
// Rotation shares the same argument as the launch secret: whatever is loaded on
// the first generation is written to the state dir (0600) and reused, because
// re-minting on handoff would reset every agent's rung — a rotation is a change
// of ports, not of trust.
func loadOrMintTrust(stateDir, cluster, kind string) (clusterTrust, error) {
	switch kind {
	case "", TrustSecret:
		return loadOrMintSecret(stateDir, cluster)
	case TrustDerived:
		parentID := strings.TrimSpace(os.Getenv(envTrustParent))
		parentSecret := strings.TrimSpace(os.Getenv(envTrustParentSecret))
		if parentID == "" || parentSecret == "" {
			return clusterTrust{}, fmt.Errorf("--trust %s needs %s and %s", TrustDerived, envTrustParent, envTrustParentSecret)
		}
		return deriveClusterTrust(clusterTrust{Cluster: parentID, Kind: TrustSecret, Secret: parentSecret}, cluster)
	case TrustCertificate:
		return loadOrMintCertificate(stateDir, cluster)
	default:
		return clusterTrust{}, fmt.Errorf("unknown trust shape %q: want %s, %s or %s",
			kind, TrustSecret, TrustDerived, TrustCertificate)
	}
}

// loadOrMintSecret is the minted-secret shape.
func loadOrMintSecret(stateDir, cluster string) (clusterTrust, error) {
	path := filepath.Join(stateDir, "cluster-secret")
	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // path is the cluster's own state dir
		if secret := strings.TrimSpace(string(data)); secret != "" {
			return clusterTrust{Cluster: cluster, Kind: TrustSecret, Secret: secret}, nil
		}
	}
	trust, err := newClusterTrust(cluster)
	if err != nil {
		return clusterTrust{}, err
	}
	if err := os.WriteFile(path, []byte(trust.Secret), 0o600); err != nil {
		return clusterTrust{}, fmt.Errorf("write cluster secret: %w", err)
	}
	return trust, nil
}

// loadOrMintCertificate is the certificate shape: the operator provisions a
// certificate a root issued and the private key it names, and lezz keeps both in
// the state dir so a rotation presents the same identity.
//
// Nothing is generated here. A cluster cannot certify itself to a root it does
// not hold, so a missing certificate is a provisioning error, not something to
// paper over with a minted secret.
func loadOrMintCertificate(stateDir, cluster string) (clusterTrust, error) {
	certPath := filepath.Join(stateDir, "cluster-cert.json")
	keyPath := filepath.Join(stateDir, "cluster-key")
	rootPath := filepath.Join(stateDir, "cluster-root.json")

	certRaw, certErr := os.ReadFile(certPath) //nolint:gosec // path is the cluster's own state dir
	keyRaw, keyErr := os.ReadFile(keyPath)    //nolint:gosec // path is the cluster's own state dir
	rootRaw, rootErr := os.ReadFile(rootPath) //nolint:gosec // path is the cluster's own state dir

	if certErr != nil || keyErr != nil || rootErr != nil {
		// First initialisation: the material is provisioned, not minted.
		certRaw = []byte(strings.TrimSpace(os.Getenv(envTrustCert)))
		keyRaw = []byte(strings.TrimSpace(os.Getenv(envTrustKey)))
		rootName := strings.TrimSpace(os.Getenv(envTrustRoot))
		rootKey := strings.TrimSpace(os.Getenv(envTrustRootKey))
		if len(certRaw) == 0 || len(keyRaw) == 0 || rootName == "" || rootKey == "" {
			return clusterTrust{}, fmt.Errorf("--trust %s needs a certificate and its key: provision %s, %s, %s and %s",
				TrustCertificate, envTrustCert, envTrustKey, envTrustRoot, envTrustRootKey)
		}
		encodedRoot, err := json.Marshal(map[string]string{"root": rootName, "public_key": rootKey})
		if err != nil {
			return clusterTrust{}, err
		}
		rootRaw = encodedRoot
		for path, body := range map[string][]byte{certPath: certRaw, keyPath: keyRaw, rootPath: rootRaw} {
			if err := os.WriteFile(path, body, 0o600); err != nil {
				return clusterTrust{}, fmt.Errorf("write %s: %w", path, err)
			}
		}
	}

	cert, err := decodeClusterCertificate(strings.TrimSpace(string(certRaw)))
	if err != nil {
		return clusterTrust{}, err
	}
	key, err := decodeClusterKey(strings.TrimSpace(string(keyRaw)))
	if err != nil {
		return clusterTrust{}, err
	}
	var root struct {
		Root      string `json:"root"`
		PublicKey string `json:"public_key"`
	}
	if unmarshalErr := json.Unmarshal(rootRaw, &root); unmarshalErr != nil {
		return clusterTrust{}, fmt.Errorf("cluster-root.json: %w", unmarshalErr)
	}
	if cert.Cluster != cluster {
		return clusterTrust{}, fmt.Errorf("certificate is for %q, but this cluster is %q", cert.Cluster, cluster)
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cert.PublicKey))
	if err != nil {
		return clusterTrust{}, fmt.Errorf("certificate public_key is not base64: %w", err)
	}
	if !ed25519.PublicKey(pub).Equal(key.Public()) {
		return clusterTrust{}, fmt.Errorf("%s does not match the certificate's public_key", keyPath)
	}
	return clusterTrust{
		Cluster: cluster,
		Kind:    TrustCertificate,
		Cert:    &cert,
		Key:     key,
		Root:    root.Root,
		RootKey: root.PublicKey,
	}, nil
}

// decodeClusterCertificate accepts the certificate as `seed42 certify` prints it
// (a base64 blob) or as its JSON.
func decodeClusterCertificate(raw string) (isotope.Certificate, error) {
	body := []byte(raw)
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
		body = decoded
	}
	var cert isotope.Certificate
	if err := json.Unmarshal(body, &cert); err != nil {
		return isotope.Certificate{}, fmt.Errorf("certificate is neither base64 JSON nor JSON: %w", err)
	}
	return cert, nil
}

// decodeClusterKey reads an ed25519 private key as base64, accepting the 64-byte
// private key or its 32-byte seed.
func decodeClusterKey(raw string) (ed25519.PrivateKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("cluster key is not base64: %w", err)
	}
	switch len(decoded) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(decoded), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(decoded), nil
	default:
		return nil, fmt.Errorf("cluster key is %d bytes, want %d or %d",
			len(decoded), ed25519.PrivateKeySize, ed25519.SeedSize)
	}
}
