package demo

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

// TestRegistryBindDefaultsToLoopback is the core of lezz.go#59: the /cluster
// registry is an unauthenticated read-and-write directory, so the bind that
// publishes it to the LAN must be asked for, never defaulted into.
func TestRegistryBindDefaultsToLoopback(t *testing.T) {
	rl, err := resolveRegistryListener("")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if want := net.JoinHostPort(RegistryBindDefault, strconv.Itoa(DiscoveryPort)); rl.Addr != want {
		t.Fatalf("default bind = %q, want %q", rl.Addr, want)
	}
	if rl.LAN {
		t.Fatalf("the default registry bind must not be LAN-reachable: %+v", rl)
	}
	if want := "http://127.0.0.1:" + strconv.Itoa(DiscoveryPort) + "/cluster"; rl.registryURL() != want {
		t.Fatalf("registry URL = %q, want %q", rl.registryURL(), want)
	}
}

func TestRegistryBindMatrix(t *testing.T) {
	outbound := outboundIP()
	for _, tc := range []struct {
		bind    string
		wantLAN bool
		wantURL string
	}{
		{"127.0.0.1", false, "http://127.0.0.1:19100/cluster"},
		{"::1", false, "http://[::1]:19100/cluster"},
		{"0.0.0.0", true, "http://" + outbound + ":19100/cluster"},
		{"::", true, "http://" + outbound + ":19100/cluster"},
		{"192.168.1.9", true, "http://192.168.1.9:19100/cluster"},
	} {
		rl, err := resolveRegistryListener(tc.bind)
		if err != nil {
			t.Fatalf("resolve %q: %v", tc.bind, err)
		}
		if rl.LAN != tc.wantLAN {
			t.Errorf("%q: LAN = %v, want %v", tc.bind, rl.LAN, tc.wantLAN)
		}
		if got := rl.registryURL(); got != tc.wantURL {
			t.Errorf("%q: URL = %q, want %q", tc.bind, got, tc.wantURL)
		}
	}
}

// TestRegistryBindRejectsAHostname fails before anything is spawned: a bind is
// an address, not a name to resolve.
func TestRegistryBindRejectsAHostname(t *testing.T) {
	for _, bind := range []string{"localhost", "example.com", "127.0.0.1:19100"} {
		if _, err := resolveRegistryListener(bind); err == nil {
			t.Fatalf("accept %q as a registry bind", bind)
		} else if !strings.Contains(err.Error(), "--registry-bind") {
			t.Errorf("error %q does not name the flag", err)
		}
	}
}

// TestRegistryMDNSIsSuppressedOnLoopback: a loopback directory advertised over
// DNS-SD would name a service no peer can dial. The gate is asserted without
// touching multicast, so it cannot go green on a host with no mDNS.
func TestRegistryMDNSIsSuppressedOnLoopback(t *testing.T) {
	loopback, err := resolveRegistryListener("")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	srv, err := registerRegistryMDNS(loopback)
	if err != nil {
		t.Fatalf("registerRegistryMDNS(loopback) = %v, want a silent no-op", err)
	}
	if srv != nil {
		srv.Shutdown()
		t.Fatal("a loopback registry was advertised over mDNS")
	}
}

// TestRegistryBindPublishesOnlyWhenAsked covers the deliberate LAN case: the
// operator names the interface, and the resolved listener says so.
func TestRegistryBindPublishesOnlyWhenAsked(t *testing.T) {
	if _, lan, _ := registryHost("0.0.0.0", "10.0.0.5"); !lan {
		t.Fatal("a wildcard bind must be reported as LAN-reachable")
	}
	if host, _, _ := registryHost("0.0.0.0", "10.0.0.5"); host != "10.0.0.5" {
		t.Fatalf("wildcard bind named %q, want the outbound IP", host)
	}
	if _, lan, _ := registryHost("127.0.0.1", "10.0.0.5"); lan {
		t.Fatal("loopback reported as LAN-reachable")
	}
}

// TestProdRegistryBindRoundTrip: a rotated child serves who the parent served.
// A handoff must not pull a published registry back to loopback.
func TestProdRegistryBindRoundTrip(t *testing.T) {
	opts, err := (ProdOptions{Isotope: "lab-42", BasePort: 21000, RegistryBind: "0.0.0.0"}).normalized()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	args := prodServiceArgs(opts, 2)
	if !strings.Contains(strings.Join(args, " "), "--registry-bind 0.0.0.0") {
		t.Fatalf("child argv drops the registry bind: %v", args)
	}
	got, err := ParseProdArgs(args[1:], nil)
	if err != nil {
		t.Fatalf("parse child argv: %v", err)
	}
	if got.RegistryBind != opts.RegistryBind {
		t.Fatalf("registry bind across rotation: %q -> %q", opts.RegistryBind, got.RegistryBind)
	}

	// The default is not spelled out in the argv, and still normalizes to itself.
	plain := prodServiceArgs(ProdOptions{Isotope: "lab-42", RegistryBind: ""}, 2)
	if strings.Contains(strings.Join(plain, " "), "--registry-bind") {
		t.Fatalf("default bind written into the child argv: %v", plain)
	}
}

// TestProdRejectsAHostnameRegistryBind: an accepted argument set must be
// usable, which is the same property FuzzParseProdArgs asserts.
func TestProdRejectsAHostnameRegistryBind(t *testing.T) {
	if _, err := ParseProdArgs([]string{"--registry-bind", "localhost"}, nil); err == nil {
		t.Fatal("accepted a hostname registry bind")
	} else if !strings.Contains(err.Error(), "--registry-bind") {
		t.Fatalf("error %q does not name the flag", err)
	}
}

// TestProdRegistryRecordNamesTheBind: the identity prod registers with its
// alarms must name the registry address that answers. With the loopback default
// that is loopback, not the outbound IP nothing is listening on.
func TestProdRegistryRecordNamesTheBind(t *testing.T) {
	const outbound = "192.168.1.5"
	for _, tc := range []struct{ bind, want string }{
		{"", "http://127.0.0.1:19100/cluster"},
		{"0.0.0.0", "http://" + outbound + ":19100/cluster"},
		{"10.1.2.3", "http://10.1.2.3:19100/cluster"},
	} {
		plan, err := buildProdPlan(ProdOptions{Isotope: "lab-42", RegistryBind: tc.bind}, outbound)
		if err != nil {
			t.Fatalf("build plan for %q: %v", tc.bind, err)
		}
		var got string
		for _, rec := range plan.Isotopes {
			if rec.Role == "cluster" {
				got = rec.Endpoint
			}
		}
		if got != tc.want {
			t.Errorf("bind %q: registry record = %q, want %q", tc.bind, got, tc.want)
		}
	}
}
