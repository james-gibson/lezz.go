package demo

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/james-gibson/isotope"
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
	Secret  string
}

// newClusterTrust mints a fresh secret for a cluster that is starting now.
func newClusterTrust(cluster string) (clusterTrust, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return clusterTrust{}, fmt.Errorf("mint cluster secret: %w", err)
	}
	return clusterTrust{Cluster: cluster, Secret: hex.EncodeToString(buf)}, nil
}

// emptyTrust is a cluster that cannot attest anything: the secret is missing, so
// its agents register at the floor rather than with a receipt nobody can verify.
func (t clusterTrust) ok() bool { return t.Cluster != "" && t.Secret != "" }

// env is how the cluster hands the secret to a child process it spawns, and what
// adhd reads to sign its registrations.
func (t clusterTrust) env() []string {
	if !t.ok() {
		return nil
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
		return "trust:\n  clusters: {}\n"
	}
	return fmt.Sprintf("trust:\n  clusters:\n    %q: %q\n", t.Cluster, t.Secret)
}

// attestation signs endpoint for this cluster, for the registrations lezz makes
// itself (prod's isotope records) rather than a child's.
func (t clusterTrust) attestation(endpoint string) *isotope.Attestation {
	if !t.ok() {
		return nil
	}
	att := isotope.SignAttestation(t.Secret, t.Cluster, endpoint, time.Now())
	return &att
}

// loadOrMintClusterTrust returns the cluster's secret, minting it on first use.
//
// A cluster that rotates — `lezz prod` hands its ports to a new generation —
// must keep one secret, or the child's alarms would refuse the parent's agents
// and every rotation would read as a trust reset. The secret therefore lives in
// the cluster's state dir (0600) instead of an argv, where `ps` would show it.
func loadOrMintClusterTrust(stateDir, cluster string) (clusterTrust, error) {
	path := filepath.Join(stateDir, "cluster-secret")
	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // path is the cluster's own state dir
		if secret := string(data); secret != "" {
			return clusterTrust{Cluster: cluster, Secret: secret}, nil
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
