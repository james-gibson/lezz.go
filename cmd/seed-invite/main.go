// Command seed-invite is the production-capable address plane in front of the
// WAN ingress. It stands in for a peer cluster's egress and alarms: it resolves
// an invite (an isotope name) to the current binding, and — when configured to
// do so — requires a signed rotational announcement to have armed a short-lived
// expectation before it forwards the call.
//
// The shipped wan-ingress binary remains unaware of seed, DNS or mDNS. This
// process owns addressing and expectation; the ingress still owns admission and
// the signed border.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/james-gibson/lezz.go/internal/announce"
)

const (
	inviteHeader   = "X-Invite"
	ticketHeader   = "X-Invite-Ticket"
	rotationHeader = "X-Invite-Rotation"
)

// Config is operator-owned. Defaults fail closed: a call without a live signed
// expectation is refused unless an operator explicitly disables that check.
type Config struct {
	Addr                string   `json:"addr,omitempty"`
	Registry            string   `json:"registry,omitempty"`
	Name                string   `json:"name,omitempty"`
	Token               string   `json:"token,omitempty"`
	Seed                string   `json:"seed,omitempty"`
	Slot                string   `json:"slot,omitempty"`
	AnnounceKey         string   `json:"announce_key,omitempty"`
	DNSDomain           string   `json:"dns_domain,omitempty"`
	MDNSDomain          string   `json:"mdns_domain,omitempty"`
	MDNSInterface       string   `json:"mdns_interface,omitempty"`
	MDNS                bool     `json:"mdns,omitempty"`
	TickSeconds         int      `json:"tick_seconds,omitempty"`
	TickOffset          int64    `json:"tick_offset,omitempty"`
	RequireAnnouncement bool     `json:"require_announcement,omitempty"`
	Peers               []string `json:"peers,omitempty"`
}

func defaultConfig() Config {
	return Config{
		Addr:                "127.0.0.1:0",
		Name:                "wan-ingress",
		MDNSDomain:          "_lezz-rot._tcp.local.",
		TickSeconds:         5,
		RequireAnnouncement: true,
	}
}

func loadConfigFile(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer func() { _ = file.Close() }()

	cfg := defaultConfig()
	dec := json.NewDecoder(io.LimitReader(file, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	return cfg, nil
}

// applyFlags lets explicit flags override a config field without a bool flag
// accidentally turning an operator's true back to false.
func applyFlags(cfg *Config, set map[string]bool, flags map[string]func()) {
	for name, apply := range flags {
		if set[name] {
			apply()
		}
	}
}

type isotopeRecord struct {
	Name         string    `json:"name"`
	Endpoint     string    `json:"endpoint"`
	RegisteredAt time.Time `json:"registered_at"`
}

// resolve turns an invite into the current binding of that isotope name. A
// restarted ingress is found by registration time, not by a caller-held URL.
func resolve(ctx context.Context, registryURL, name string, client *http.Client) (string, error) {
	endpoint := strings.TrimRight(registryURL, "/") + "/isotope/list"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("build registry request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("query registry: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry returned %s", resp.Status)
	}
	var records []isotopeRecord
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return "", fmt.Errorf("decode registry: %w", err)
	}
	var best isotopeRecord
	for _, rec := range records {
		if rec.Name == name && rec.Endpoint != "" && rec.RegisteredAt.After(best.RegisteredAt) {
			best = rec
		}
	}
	if best.Endpoint == "" {
		return "", fmt.Errorf("no isotope named %q in %s", name, registryURL)
	}
	return strings.TrimRight(best.Endpoint, "/"), nil
}

func singleJoining(base, path string) string {
	base = strings.TrimRight(base, "/")
	path = strings.TrimLeft(path, "/")
	if path == "" {
		return base
	}
	return base + "/" + path
}

type peerResult struct {
	URL   string                  `json:"url"`
	OK    bool                    `json:"ok"`
	Error string                  `json:"error,omitempty"`
	Tick  int64                   `json:"tick,omitempty"`
	Seen  []announce.Announcement `json:"seen,omitempty"`
}

type challengeResult struct {
	Confirmed bool         `json:"confirmed"`
	Local     bool         `json:"local"`
	Name      string       `json:"name"`
	Rotation  string       `json:"rotation"`
	Tick      int64        `json:"tick"`
	Peers     []peerResult `json:"peers"`
}

func fetchStatus(ctx context.Context, peer string, client *http.Client) (announce.Status, error) {
	endpoint := strings.TrimRight(peer, "/") + "/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return announce.Status{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return announce.Status{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return announce.Status{}, fmt.Errorf("status returned %s", resp.Status)
	}
	var status announce.Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return announce.Status{}, err
	}
	return status, nil
}

func statusHasRotation(status announce.Status, name, rotation string, tick int64) bool {
	for _, a := range status.Seen {
		if a.Name == name && a.Rotation == rotation && a.Tick >= tick {
			return true
		}
	}
	return false
}

// announcementSink is the only path from a DNS/mDNS record into the store. It
// pins the configured slot as well as the seed so a signed record for a different
// slot cannot arm this process.
func announcementSink(cfg Config, store *announce.Store) func(announce.Announcement) {
	return func(a announce.Announcement) {
		if cfg.Slot != "" && a.Slot != cfg.Slot {
			log.Printf("ignore announcement %q: slot %q is not configured %q", a.Rotation, a.Slot, cfg.Slot)
			return
		}
		if err := store.Add(a); err != nil {
			log.Printf("ignore announcement %q: %v", a.Rotation, err)
		}
	}
}

func newHandler(cfg Config, store *announce.Store, client *http.Client) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(store.Status())
	})

	// le.ad: the empty desk. It is a connection point, never a content store.
	mux.HandleFunc("/desk", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(store.Desk(cfg.Seed, cfg.Name, cfg.Slot))
	})

	// The inversion challenge: a monitor asks the upstream address plane to
	// prove that the announced rotation is present on the cluster tick, not just
	// on the node that made the claim.
	mux.HandleFunc("/challenge", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			name = cfg.Name
		}
		rotation := r.URL.Query().Get("rotation")
		tick, err := strconv.ParseInt(r.URL.Query().Get("tick"), 10, 64)
		if err != nil || tick <= 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(challengeResult{Name: name, Rotation: rotation})
			return
		}
		local := store.Confirm(name, rotation, tick)
		result := challengeResult{Confirmed: local, Local: local, Name: name, Rotation: rotation, Tick: tick}
		for _, peer := range cfg.Peers {
			item := peerResult{URL: peer}
			status, err := fetchStatus(r.Context(), peer, client)
			if err != nil {
				item.Error = err.Error()
			} else {
				item.OK = statusHasRotation(status, name, rotation, tick)
				item.Tick = status.LocalTick
				item.Seen = status.Seen
			}
			result.Confirmed = result.Confirmed && item.OK
			result.Peers = append(result.Peers, item)
		}
		w.Header().Set("Content-Type", "application/json")
		if !result.Confirmed {
			w.WriteHeader(http.StatusForbidden)
		}
		_ = json.NewEncoder(w).Encode(result)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		invite := r.Header.Get(inviteHeader)
		if invite == "" {
			invite = cfg.Name
		}

		var rotation announce.Announcement
		if cfg.RequireAnnouncement {
			ticket := r.Header.Get(ticketHeader)
			if ticket == "" {
				ticket = r.URL.Query().Get("ticket")
			}
			var ok bool
			rotation, ok = store.Consume(invite, ticket)
			if !ok {
				log.Printf("refuse invite %q: no live rotational expectation", invite)
				http.Error(w, "no live rotational expectation", http.StatusForbidden)
				return
			}
		}

		endpoint, err := resolve(r.Context(), cfg.Registry, invite, client)
		if err != nil {
			log.Printf("resolve invite %q: %v", invite, err)
			http.Error(w, "invite not resolvable", http.StatusBadGateway)
			return
		}
		target, err := url.Parse(endpoint)
		if err != nil || target.Host == "" {
			log.Printf("invalid endpoint %q for invite %q: %v", endpoint, invite, err)
			http.Error(w, "invite not resolvable", http.StatusBadGateway)
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Director = func(out *http.Request) {
			out.URL.Scheme = target.Scheme
			out.URL.Host = target.Host
			out.URL.Path = singleJoining(target.Path, out.URL.Path)
			out.Host = target.Host
			out.Header = out.Header.Clone()
			if out.Header.Get("Authorization") == "" && cfg.Token != "" {
				out.Header.Set("Authorization", "Bearer "+cfg.Token)
			}
			out.Header.Del(inviteHeader)
			out.Header.Del(ticketHeader)
			if rotation.Rotation != "" {
				out.Header.Set(rotationHeader, rotation.Rotation)
			}
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("forward invite %q: %v", invite, err)
			http.Error(w, "egress unavailable", http.StatusBadGateway)
		}
		proxy.ServeHTTP(w, r)
	})
	return mux
}

func main() {
	configPath := flag.String("config", "", "JSON config; see configs/seed-invite.example.json")
	addr := flag.String("addr", "", "listen address")
	registry := flag.String("registry", "", "isotope registry URL (smoke-alarm)")
	name := flag.String("name", "", "default invite to resolve when X-Invite is absent")
	token := flag.String("token", "", "bearer token injected when the caller supplies none")
	seed := flag.String("seed", "", "seed this address plane serves")
	slot := flag.String("slot", "", "slot this address plane watches")
	key := flag.String("announce-key", "", "base64 announcement key")
	dnsDomain := flag.String("dns-domain", "", "TXT record polled for signed rotations")
	mdnsDomain := flag.String("mdns-domain", "", "mDNS TXT service domain")
	mdnsIface := flag.String("mdns-interface", "", "mDNS interface (default: system multicast interface)")
	mdns := flag.Bool("mdns", false, "listen for mDNS TXT rotations")
	tickSeconds := flag.Int("tick-seconds", 0, "cluster tick length in seconds")
	tickOffset := flag.Int64("tick-offset", 0, "local tick offset; use 1 for the node one tick ahead")
	require := flag.Bool("require-announcement", false, "require a signed expectation before forwarding")
	peers := flag.String("peers", "", "comma-separated peer status URLs for inversion challenges")
	flag.Parse()

	cfg, err := loadConfigFile(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if *configPath == "" {
		cfg = defaultConfig()
	}
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	applyFlags(&cfg, set, map[string]func(){
		"addr":                 func() { cfg.Addr = *addr },
		"registry":             func() { cfg.Registry = *registry },
		"name":                 func() { cfg.Name = *name },
		"token":                func() { cfg.Token = *token },
		"seed":                 func() { cfg.Seed = *seed },
		"slot":                 func() { cfg.Slot = *slot },
		"announce-key":         func() { cfg.AnnounceKey = *key },
		"dns-domain":           func() { cfg.DNSDomain = *dnsDomain },
		"mdns-domain":          func() { cfg.MDNSDomain = *mdnsDomain },
		"mdns-interface":       func() { cfg.MDNSInterface = *mdnsIface },
		"mdns":                 func() { cfg.MDNS = *mdns },
		"tick-seconds":         func() { cfg.TickSeconds = *tickSeconds },
		"tick-offset":          func() { cfg.TickOffset = *tickOffset },
		"require-announcement": func() { cfg.RequireAnnouncement = *require },
		"peers": func() {
			if *peers == "" {
				cfg.Peers = nil
			} else {
				cfg.Peers = strings.Split(*peers, ",")
			}
		},
	})
	if cfg.Addr == "" {
		cfg.Addr = defaultConfig().Addr
	}
	if cfg.Name == "" {
		cfg.Name = defaultConfig().Name
	}
	if cfg.MDNSDomain == "" {
		cfg.MDNSDomain = defaultConfig().MDNSDomain
	}
	if cfg.TickSeconds <= 0 {
		cfg.TickSeconds = defaultConfig().TickSeconds
	}
	if cfg.Registry == "" {
		log.Fatal("-registry is required")
	}

	var keyBytes []byte
	if cfg.RequireAnnouncement {
		if cfg.Seed == "" {
			log.Fatal("seed is required when announcements are enforced")
		}
		if cfg.DNSDomain == "" && !cfg.MDNS {
			log.Fatal("configure dns-domain or mdns so a signed expectation can arrive")
		}
		keyBytes, err = announce.DecodeKey(cfg.AnnounceKey)
		if err != nil {
			log.Fatalf("announce key: %v", err)
		}
	}

	interval := time.Duration(cfg.TickSeconds) * time.Second
	store := announce.NewStore(keyBytes, cfg.Seed, interval, cfg.TickOffset)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := announcementSink(cfg, store)
	if cfg.DNSDomain != "" {
		source := &announce.DNSSource{Domain: cfg.DNSDomain, Interval: interval, Lookup: nil}
		go func() {
			if err := source.Run(ctx, sink); err != nil {
				log.Printf("dns announcement source: %v", err)
			}
		}()
	}
	if cfg.MDNS {
		source := &announce.MDNSSource{Domain: cfg.MDNSDomain, Interface: cfg.MDNSInterface}
		go func() {
			if err := source.Run(ctx, sink); err != nil {
				log.Printf("mdns announcement source: %v", err)
			}
		}()
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.Addr, err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	log.Printf("seed-invite listening addr=%s registry=%s name=%s seed=%s tick=%d offset=%d",
		ln.Addr(), cfg.Registry, cfg.Name, cfg.Seed, cfg.TickSeconds, cfg.TickOffset)
	log.Fatal(http.Serve(ln, newHandler(cfg, store, client)))
}
