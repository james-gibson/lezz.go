package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

// DiscoveryPort is the fixed well-known port for the /cluster registry.
// Remote clients always reach it at <host>:19100/cluster.
const DiscoveryPort = 19100

const mdnsService = "_lezz-demo._tcp"
const mdnsDomain = "local."

// ClusterInfo describes a running demo cluster.
// Name uniquely identifies it within a registry (defaults to "demo-<pid>").
type ClusterInfo struct {
	Name        string   `json:"name"`
	AlarmA      string   `json:"alarm_a"`
	AlarmB      string   `json:"alarm_b"`
	AdhdMCP     string   `json:"adhd_mcp"`
	GithubRepos []string `json:"github_repos,omitempty"`
}

// registry holds the set of registered clusters, safe for concurrent use.
type registry struct {
	mu       sync.RWMutex
	clusters map[string]ClusterInfo
}

func newRegistry(seed ClusterInfo) *registry {
	r := &registry{clusters: make(map[string]ClusterInfo)}
	r.clusters[seed.Name] = seed
	return r
}

func (r *registry) add(info ClusterInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clusters[info.Name] = info
}

func (r *registry) remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.clusters, name)
}

func (r *registry) list() map[string]ClusterInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]ClusterInfo, len(r.clusters))
	for k, v := range r.clusters {
		out[k] = v
	}
	return out
}

// startDiscoveryServer binds the fixed discovery port and serves the cluster
// registry. Returns the HTTP server (caller must Shutdown) and any bind error.
//
// GET  /cluster          → map[name]ClusterInfo JSON
// POST /cluster          → register a ClusterInfo (body: ClusterInfo JSON)
// DELETE /cluster/{name} → deregister by name
func startDiscoveryServer(seed ClusterInfo) (*http.Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", DiscoveryPort))
	if err != nil {
		return nil, fmt.Errorf("bind discovery port %d: %w", DiscoveryPort, err)
	}

	reg := newRegistry(seed)
	mux := http.NewServeMux()

	mux.HandleFunc("/cluster", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(reg.list())

		case http.MethodPost:
			var info ClusterInfo
			if err := json.NewDecoder(r.Body).Decode(&info); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if info.Name == "" {
				http.Error(w, "name required", http.StatusBadRequest)
				return
			}
			reg.add(info)
			w.WriteHeader(http.StatusOK)

		default:
			// DELETE /cluster/<name>
			if r.Method == http.MethodDelete {
				name := strings.TrimPrefix(r.URL.Path, "/cluster/")
				if name == "" {
					http.Error(w, "name required", http.StatusBadRequest)
					return
				}
				reg.remove(name)
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}

// joinDiscoveryServer attempts to register info with an existing discovery
// server on DiscoveryPort. Returns a deregister function to call on shutdown,
// or an error if the server isn't a lezz registry.
func joinDiscoveryServer(info ClusterInfo) (deregFn func(), err error) {
	body, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("marshal cluster info: %w", err)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/cluster", DiscoveryPort)
	client := &http.Client{Timeout: 3 * time.Second}

	resp, err := client.Post(url, "application/json", bytes.NewReader(body)) //nolint:gosec // url is localhost:19100
	if err != nil {
		return nil, fmt.Errorf("POST to existing discovery server: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery server rejected registration (HTTP %d)", resp.StatusCode)
	}

	deregFn = func() {
		deregURL := fmt.Sprintf("http://127.0.0.1:%d/cluster/%s", DiscoveryPort, info.Name)
		req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodDelete, deregURL, http.NoBody)
		if reqErr != nil {
			return
		}
		resp, doErr := client.Do(req)
		if doErr == nil {
			_ = resp.Body.Close()
		}
	}
	return deregFn, nil
}

// registerMDNS advertises the lezz demo registry via mDNS/DNS-SD.
// Clients browse for _lezz-demo._tcp then GET /cluster for the full list.
func registerMDNS() (*zeroconf.Server, error) {
	return zeroconf.Register("lezz-demo", mdnsService, mdnsDomain, DiscoveryPort, []string{"v=1"}, nil)
}

// outboundIP returns the machine's preferred outbound IP address.
// Falls back to 127.0.0.1 if detection fails.
func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer func() { _ = conn.Close() }()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "127.0.0.1"
	}
	return addr.IP.String()
}

// BrowseDemoCluster does a short mDNS browse for a running lezz demo registry
// and returns all registered clusters, or an error if none found within timeout.
func BrowseDemoCluster(ctx context.Context, timeout time.Duration) ([]ClusterInfo, error) {
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
		if len(entry.AddrIPv4) == 0 {
			return nil, fmt.Errorf("mDNS entry has no IPv4 address")
		}
		host := entry.AddrIPv4[0].String()
		return fetchAllClusters(ctx, fmt.Sprintf("http://%s:%d/cluster", host, entry.Port))
	case <-browseCtx.Done():
		return nil, fmt.Errorf("no lezz demo cluster found on the LAN within %s", timeout)
	}
}

// fetchAllClusters retrieves the full cluster registry from a /cluster endpoint.
func fetchAllClusters(ctx context.Context, url string) ([]ClusterInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var m map[string]ClusterInfo
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode cluster registry: %w", err)
	}
	clusters := make([]ClusterInfo, 0, len(m))
	for _, v := range m {
		clusters = append(clusters, v)
	}
	return clusters, nil
}
