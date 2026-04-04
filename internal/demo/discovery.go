package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/grandcat/zeroconf"
)

// DiscoveryPort is the fixed well-known port for the /cluster endpoint.
// Remote clients can always reach it at <host>:19100/cluster.
const DiscoveryPort = 19100

const mdnsService = "_lezz-demo._tcp"
const mdnsDomain = "local."

// ClusterInfo describes a running demo cluster.
// It is served as JSON at /cluster and embedded in mDNS TXT records.
type ClusterInfo struct {
	AlarmA  string `json:"alarm_a"`
	AlarmB  string `json:"alarm_b"`
	AdhdMCP string `json:"adhd_mcp"`
}

// startDiscoveryServer binds the fixed discovery port and serves /cluster.
// Call srv.Shutdown to stop it.
func startDiscoveryServer(info ClusterInfo) (*http.Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", DiscoveryPort))
	if err != nil {
		return nil, fmt.Errorf("bind discovery port %d: %w", DiscoveryPort, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info)
	})

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}

// registerMDNS advertises the demo cluster via mDNS/DNS-SD on the LAN.
// TXT records carry the cluster endpoints so browsers get full info without
// a follow-up HTTP call. The returned server must be Shutdown to deregister.
func registerMDNS(info ClusterInfo) (*zeroconf.Server, error) {
	txt := []string{
		"v=1",
		"alarm_a=" + info.AlarmA,
		"alarm_b=" + info.AlarmB,
		"adhd_mcp=" + info.AdhdMCP,
	}
	return zeroconf.Register("lezz-demo", mdnsService, mdnsDomain, DiscoveryPort, txt, nil)
}

// outboundIP returns the machine's preferred outbound IP address so remote
// clients can reach the cluster. Falls back to 127.0.0.1 if detection fails.
func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer func() { _ = conn.Close() }()
	return conn.LocalAddr().(*net.UDPAddr).IP.String() //nolint:forcetypeassert // Dial("udp") always returns *UDPAddr
}

// BrowseDemoCluster does a short mDNS browse for a running lezz demo and
// returns the first ClusterInfo it finds, or an error if none is found within
// the timeout. Useful for the dashboard to auto-discover on the LAN.
func BrowseDemoCluster(ctx context.Context, timeout time.Duration) (*ClusterInfo, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("create mDNS resolver: %w", err)
	}

	entries := make(chan *zeroconf.ServiceEntry, 4)
	browseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := resolver.Browse(browseCtx, mdnsService, mdnsDomain, entries); err != nil {
		return nil, fmt.Errorf("mDNS browse: %w", err)
	}

	select {
	case entry := <-entries:
		info := clusterInfoFromTXT(entry)
		if info != nil {
			return info, nil
		}
		// TXT missing — fall back to fetching the /cluster endpoint.
		host := entry.AddrIPv4[0].String()
		return fetchClusterInfo(ctx, fmt.Sprintf("http://%s:%d/cluster", host, entry.Port))
	case <-browseCtx.Done():
		return nil, fmt.Errorf("no lezz demo cluster found on the LAN within %s", timeout)
	}
}

// clusterInfoFromTXT parses TXT records into a ClusterInfo, returning nil if
// the required keys are absent.
func clusterInfoFromTXT(entry *zeroconf.ServiceEntry) *ClusterInfo {
	info := &ClusterInfo{}
	for _, kv := range entry.Text {
		switch {
		case len(kv) > len("alarm_a=") && kv[:len("alarm_a=")] == "alarm_a=":
			info.AlarmA = kv[len("alarm_a="):]
		case len(kv) > len("alarm_b=") && kv[:len("alarm_b=")] == "alarm_b=":
			info.AlarmB = kv[len("alarm_b="):]
		case len(kv) > len("adhd_mcp=") && kv[:len("adhd_mcp=")] == "adhd_mcp=":
			info.AdhdMCP = kv[len("adhd_mcp="):]
		}
	}
	if info.AlarmA == "" || info.AlarmB == "" || info.AdhdMCP == "" {
		return nil
	}
	return info
}

// fetchClusterInfo retrieves ClusterInfo from a /cluster HTTP endpoint.
func fetchClusterInfo(ctx context.Context, url string) (*ClusterInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var info ClusterInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode cluster info: %w", err)
	}
	return &info, nil
}
