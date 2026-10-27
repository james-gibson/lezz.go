//
//  hub.go
//  lezz.go
//
//  The session hub: one adhd for the whole `lezz sea` session, started before
//  the app and stopped after every cluster.
//
//  The app used to guess 127.0.0.1:9090, and each cluster knew only its own
//  endpoint. The hub is the stable endpoint: it is announced on the channel, it
//  carries the app's own light (the channel status lezz serves), and each
//  cluster's adhd joins it as a prime-plus.
//
//  It binds loopback only. adhd's proxy surface forwards to any endpoint the
//  caller names (adhd/internal/proxy), so a hub reachable from the LAN would be
//  a confused deputy. Loopback is the whole of its protection.
//

package sea

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// HubSlug is the lab-root directory the hub owns. A cluster may not take
	// the name: the hub outlives every cluster and is stopped separately.
	HubSlug = "hub"
	// HubPreferredPort is the port the app and adhd both default to.
	HubPreferredPort = 9090
	// HubChannelLight is the light the hub watches for the app/channel status.
	HubChannelLight = "lezz.sea"

	// Cluster adhds are allocated here, above the hub's preferred port.
	clusterPortLow  = 9190
	clusterPortHigh = 9289
)

// Hub is a running session hub.
type Hub struct {
	Endpoint   string
	Port       int
	ConfigPath string
	PIDPath    string
}

// hubConfig is the derived adhd config for the hub.
//
// channelEndpoint is the loopback status lezz serves for the app/channel light.
// Empty means the hub starts with no smoke_alarm entries — a hub with no lights
// yet, not a hub watching a guessed port.
func hubConfig(port int, channelEndpoint string) string {
	lines := []string{
		"mcp_server:",
		"  enabled: true",
		fmt.Sprintf("  addr: \"127.0.0.1:%d\"", port),
	}
	if channelEndpoint != "" {
		lines = append(lines,
			"smoke_alarm:",
			fmt.Sprintf("  - name: %q", HubChannelLight),
			fmt.Sprintf("    endpoint: %q", channelEndpoint),
			"    interval: 10s",
		)
	}
	return strings.Join(lines, "\n") + "\n"
}

// portAllocator hands out loopback ports and remembers what it handed out.
//
// Probing alone listens-then-closes, so two callers can be given the same port
// in the window before either child binds. The hub and every cluster adhd come
// through here, so the session cannot collide with itself.
type portAllocator struct {
	used map[int]bool
}

func newPortAllocator() *portAllocator {
	return &portAllocator{used: make(map[int]bool)}
}

// acquire prefers the given port and otherwise takes the next free cluster
// port. It never returns a port it has already handed out.
func (a *portAllocator) acquire(preferred int) (int, error) {
	low, high := clusterPortLow, clusterPortHigh
	if preferred >= 1 && preferred <= 65535 && !a.used[preferred] && portFree(preferred) {
		a.used[preferred] = true
		return preferred, nil
	}
	for port := low; port <= high; port++ {
		if a.used[port] || !portFree(port) {
			continue
		}
		a.used[port] = true
		return port, nil
	}
	return 0, fmt.Errorf("no free port in %d–%d", low, high)
}

// portFree reports whether this process can bind the port on loopback.
func portFree(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// startHub writes the hub config under the lab root and starts adhd.
func startHub(root, adhd, channelEndpoint string, ports *portAllocator) (*Hub, *exec.Cmd, error) {
	dir := filepath.Join(root, HubSlug)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, fmt.Errorf("hub: create %s: %w", dir, err)
	}
	port, err := ports.acquire(HubPreferredPort)
	if err != nil {
		return nil, nil, fmt.Errorf("hub: %w", err)
	}

	configPath := filepath.Join(dir, "adhd.yaml")
	pidPath := filepath.Join(dir, "adhd.pid")
	if writeErr := os.WriteFile(configPath, []byte(hubConfig(port, channelEndpoint)), 0o600); writeErr != nil {
		return nil, nil, fmt.Errorf("hub: write %s: %w", configPath, writeErr)
	}

	cmd, startErr := startCommand(adhd, []string{"--headless", "--config", configPath}, filepath.Join(dir, "adhd.log"))
	if startErr != nil {
		return nil, nil, fmt.Errorf("hub: %w", startErr)
	}
	if writeErr := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o600); writeErr != nil {
		terminateGroup(cmd)
		return nil, nil, fmt.Errorf("hub: write %s: %w", pidPath, writeErr)
	}
	return &Hub{
		Endpoint:   fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		Port:       port,
		ConfigPath: configPath,
		PIDPath:    pidPath,
	}, cmd, nil
}
