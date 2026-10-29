package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

// DiscoveryPort is the fixed well-known port for the /cluster registry.
// Local clients always reach it at <host>:19100/cluster.
const DiscoveryPort = 19100

// RegistryBindDefault is the host the /cluster registry listens on unless the
// operator opts into LAN discovery.
//
// The registry is an unauthenticated directory: GET /cluster names every
// cluster's alarms and MCP endpoints, and POST /cluster accepts a record from
// whoever can reach it. Binding it to every interface published that directory
// to the LAN, which named the raw adhd MCP URLs the per-project gates exist to
// keep off it, and let a third party enumerate or overwrite records
// (lezz.go#59). Loopback makes the directory mean "this host"; exposing it to a
// LAN is a decision the operator takes out loud, with --registry-bind.
const RegistryBindDefault = "127.0.0.1"

const mdnsService = "_lezz-demo._tcp"
const mdnsDomain = "local."

// ProjectEntry describes a project within a cluster that exposes an MCP server.
type ProjectEntry struct {
	Route  string `json:"route,omitempty"` // routing id a dependent resolves by (ADR 0003)
	ID     string `json:"id,omitempty"`    // legacy alias for Route; namespaces the tool
	Name   string `json:"name"`            // short name, e.g. "tuner", "adhd"
	Repo   string `json:"repo"`            // GitHub slug, e.g. "james-gibson/tuner"
	MCPURL string `json:"mcp_url"`         // base URL of the project's MCP server
}

// ClusterInfo describes a running demo cluster.
// Name uniquely identifies it within a registry (defaults to "demo-<pid>").
type ClusterInfo struct {
	Name        string         `json:"name"`
	AlarmA      string         `json:"alarm_a"`
	AlarmB      string         `json:"alarm_b"`
	AdhdMCP     string         `json:"adhd_mcp"`
	GithubRepos []string       `json:"github_repos,omitempty"`
	Projects    []ProjectEntry `json:"projects,omitempty"`
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

// registryListener is where the /cluster registry listens and who can reach it.
// Addr is the bind address; Host is the host a URL or a state file should name;
// LAN says the bind is reachable beyond loopback, which is the only case where
// advertising it over mDNS is meaningful.
type registryListener struct {
	Addr string
	Host string
	LAN  bool
}

// registryHost maps a bind onto the host a URL should name, and reports whether
// that bind is reachable from the LAN. outbound is the address to name for a
// wildcard bind; it is passed in so the prod plan stays free of I/O.
func registryHost(bind, outbound string) (host string, lan bool, err error) {
	if bind == "" {
		bind = RegistryBindDefault
	}
	ip := net.ParseIP(bind)
	if ip == nil {
		return "", false, fmt.Errorf("invalid --registry-bind %q: give an IP address (for example 127.0.0.1 to keep the registry local, or 0.0.0.0 to publish it to the LAN)", bind)
	}
	switch {
	case ip.IsUnspecified():
		return outbound, true, nil
	case ip.IsLoopback():
		return ip.String(), false, nil
	default:
		return ip.String(), true, nil
	}
}

// resolveRegistryListener maps the --registry-bind host onto the registry
// listener.
//
//   - empty: loopback. The registry is a read-and-write directory with no
//     authentication, so the default is the bind that cannot be reached from
//     the LAN (lezz.go#59). Publishing it is opt-in.
//   - loopback: bind it and advertise nothing; local clients reach it exactly
//     as before, through localhost:19100.
//   - an unspecified address (0.0.0.0, ::): bind every interface and name the
//     outbound IP — the deliberate LAN directory.
//   - any other IP: bind it and name it; it is LAN-reachable, so it is
//     advertised.
//
// An invalid value fails before any listener or child exists, so `lezz demo
// start` reports the flag in the parent rather than in a detached child's log.
func resolveRegistryListener(bind string) (registryListener, error) {
	host, lan, err := registryHost(bind, outboundIP())
	if err != nil {
		return registryListener{}, err
	}
	if bind == "" {
		bind = RegistryBindDefault
	}
	return registryListener{
		Addr: net.JoinHostPort(bind, strconv.Itoa(DiscoveryPort)),
		Host: host,
		LAN:  lan,
	}, nil
}

// registryURL is the /cluster endpoint this listener serves. The host is
// joined with the port so an IPv6 bind is bracketed.
func (l registryListener) registryURL() string {
	return fmt.Sprintf("http://%s/cluster", net.JoinHostPort(l.Host, strconv.Itoa(DiscoveryPort)))
}

// startDiscoveryServer binds the resolved discovery listener and serves the
// cluster registry. Returns the HTTP server (caller must Shutdown) and any bind
// error.
//
// GET    /cluster          → map[name]ClusterInfo JSON
// POST   /cluster          → register a ClusterInfo (body: ClusterInfo JSON)
// DELETE /cluster/{name}   → deregister by name
func startDiscoveryServer(seed ClusterInfo, rl registryListener) (*http.Server, error) {
	ln, err := net.Listen("tcp", rl.Addr)
	if err != nil {
		return nil, fmt.Errorf("bind discovery registry on %s: %w", rl.Addr, err)
	}

	srv := &http.Server{
		Handler:      newDiscoveryMux(newRegistry(seed)),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}

// newDiscoveryMux routes the registry. Both the collection path and its
// subtree are registered: ServeMux does not match /cluster/{name} to the exact
// "/cluster" pattern, so a DELETE never reached the handler while only
// "/cluster" was registered (lezz.go#57).
func newDiscoveryMux(reg *registry) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/cluster", clusterHandler(reg))
	mux.HandleFunc("/cluster/", clusterHandler(reg))
	return mux
}

// clusterHandler serves the registry. The collection path carries GET and POST;
// the subtree carries DELETE /cluster/{name}.
//
// Registration is deliberately anonymous, and that is a design constraint, not
// an omission: the lab's agents must be able to register an alarm nobody has
// told this host about, and the first agent to bind 19100 becomes the anchor
// while later ones yield to it. Do not add a token without changing how a new
// agent discovers and registers — it would turn "unknown alarms are accepted"
// into "only pre-shared ones are". The guard against a third party enumerating
// or poisoning this directory is therefore the bind, which resolves to loopback
// unless the operator asks for the LAN (RegistryBindDefault, lezz.go#59).
func clusterHandler(reg *registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := clusterNameFromPath(r.URL.Path)

		switch r.Method {
		case http.MethodGet:
			if name != "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(reg.list())

		case http.MethodPost:
			if name != "" {
				http.NotFound(w, r)
				return
			}
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

		case http.MethodDelete:
			if name == "" {
				http.Error(w, "name required", http.StatusBadRequest)
				return
			}
			reg.remove(name)
			w.WriteHeader(http.StatusOK)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// clusterNameFromPath returns the {name} segment of /cluster/{name}, or "" for
// the collection path itself.
func clusterNameFromPath(path string) string {
	name := strings.TrimPrefix(path, "/cluster")
	return strings.TrimPrefix(name, "/")
}

// joinDiscoveryServer attempts to register info with an existing discovery
// server on DiscoveryPort. Returns a deregister function to call on shutdown,
// or an error if the server isn't a lezz registry.
func joinDiscoveryServer(info ClusterInfo) (deregFn func(), err error) {
	body, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("marshal cluster info: %w", err)
	}

	url := fmt.Sprintf("http://localhost:%d/cluster", DiscoveryPort)
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
//
// The cluster identity rides in the TXT record because a verifier may not accept
// "a demo is running" as evidence: isotope resolves an advertisement to the
// cluster it is checking, and a record that names no cluster proves nothing
// (isotope#23).
func registerMDNS(cluster string) (*zeroconf.Server, error) {
	return zeroconf.Register("lezz-demo", mdnsService, mdnsDomain, DiscoveryPort, mdnsTXT(cluster), nil)
}

// mdnsTXT is the advertisement's TXT set. Split out so a test can assert the
// cluster identity is published without depending on multicast.
func mdnsTXT(cluster string) []string {
	txt := []string{"v=1"}
	if cluster != "" {
		txt = append(txt, "cluster="+cluster)
	}
	return txt
}

// registerRegistryMDNS advertises the registry over DNS-SD, but only when the
// bind is reachable from the LAN. A loopback registry advertised over mDNS would
// name a service no peer can dial — and announce a directory that is not there
// to every browser that lists it. It returns (nil, nil) when no advertisement is
// published, so a caller never has to Shutdown a nil server.
func registerRegistryMDNS(rl registryListener, cluster string) (*zeroconf.Server, error) {
	if !rl.LAN {
		return nil, nil
	}
	return registerMDNS(cluster)
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

// FetchRegistry retrieves the full cluster registry from a /cluster endpoint.
// It is the exported read path used by `lezz see`.
func FetchRegistry(ctx context.Context, url string) ([]ClusterInfo, error) {
	return fetchAllClusters(ctx, url)
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
