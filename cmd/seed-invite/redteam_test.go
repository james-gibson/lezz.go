package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/james-gibson/lezz.go/internal/announce"
)

// These are adversarial tests against the seed/rotation address plane. They
// assume the attacker can write DNS/mDNS TXT records, guess nonces, replay
// traffic and choose headers, but does not hold the seed announcement key.

func redteamSigned(t *testing.T, seed, name, slot, rotation, nonce string, tick int64) announce.Announcement {
	t.Helper()
	a, err := announce.Announcement{
		Version:  1,
		Seed:     seed,
		Name:     name,
		Slot:     slot,
		Rotation: rotation,
		Tick:     tick,
		Nonce:    nonce,
	}.Sign(testKey)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRedTeamForgedDNSRecordsCannotArmExpectation(t *testing.T) {
	store, good := testStore(t)
	cfg := testConfig("http://127.0.0.1:1")
	cfg.Slot = "slot-a"
	sink := announcementSink(cfg, store)

	forged := good
	forged.Rotation = "rot-forged"
	forged.Nonce = "nonce-forged"
	forged.Signature = "bogus-signature"

	wrongSeed := redteamSigned(t, "attacker-seed", good.Name, good.Slot, "rot-seed", "nonce-seed", good.Tick)
	wrongSlot := redteamSigned(t, good.Seed, good.Name, "other-slot", "rot-slot", "nonce-slot", good.Tick)

	domain := "_rot.seed-42.lab.example."
	source := &announce.DNSSource{
		Domain:   domain,
		Interval: time.Hour,
		Lookup: func(got string) ([]string, error) {
			if got != domain {
				t.Errorf("lookup domain = %q, want %q", got, domain)
			}
			return []string{
				"not-an-announcement",
				announce.EncodeTXT(forged),
				announce.EncodeTXT(wrongSeed),
				announce.EncodeTXT(wrongSlot),
				announce.EncodeTXT(good),
			}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = source.Run(ctx, sink)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for store.Status().Expected == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	if status := store.Status(); status.Expected != 1 {
		t.Fatalf("expected one armed expectation, got %+v", status)
	}
	for _, nonce := range []string{forged.Nonce, wrongSeed.Nonce, wrongSlot.Nonce} {
		if _, ok := store.Consume(good.Name, nonce); ok {
			t.Errorf("attacker nonce %q was accepted", nonce)
		}
	}
	if _, ok := store.Consume(good.Name, good.Nonce); !ok {
		t.Fatal("the one genuine signed record was not accepted")
	}
}

func TestRedTeamStaleLastSeenCannotKeepDeskOccupied(t *testing.T) {
	// The attacker cannot forge a new signature, but they can replay the old
	// TXT record. The desk must read the lease, not the lingering "last seen"
	// entry: a stale record must not leave le.ad looking occupied.
	now := int64(50)
	store := announce.NewStoreWithClock(testKey, "seed-42", 5*time.Second, 0,
		func() time.Time { return time.Unix(now, 0) })
	a := redteamSigned(t, "seed-42", "wan-ingress", "slot-a", "rot-stale", "nonce-stale", 10)
	if err := store.Add(a); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig("http://127.0.0.1:1")
	cfg.Slot = "slot-a"
	proxy := httptest.NewServer(newHandler(cfg, store, &http.Client{}))
	defer proxy.Close()

	now = 61 // two ticks plus slack
	status := store.Status()
	if _, ok := status.Last[a.Name+"/"+a.Slot]; !ok {
		t.Fatal("test premise failed: the old rotation is no longer in status.Last")
	}
	resp, err := proxy.Client().Get(proxy.URL + "/desk")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var desk announce.Desk
	if err := json.NewDecoder(resp.Body).Decode(&desk); err != nil {
		t.Fatal(err)
	}
	if !desk.Empty || desk.Content != 0 || desk.LeaseUntil != nil {
		t.Fatalf("stale desk = %+v, want empty with no lease", desk)
	}
}

func TestRedTeamWrongTickCannotArmExpectation(t *testing.T) {
	store, _ := testStore(t)
	for _, tick := range []int64{9, 12, 1 << 40} {
		a := redteamSigned(t, "seed-42", "wan-ingress", "slot-a", fmt.Sprintf("rot-%d", tick), fmt.Sprintf("nonce-%d", tick), tick)
		if err := store.Add(a); err == nil {
			t.Errorf("announcement at tick %d was accepted", tick)
		}
	}
	if status := store.Status(); status.Expected != 0 {
		t.Fatalf("tick skew armed expectations: %+v", status)
	}
}

func TestRedTeamCrossNameTicketAndReplay(t *testing.T) {
	var wanCalls, peerCalls atomic.Int32
	wanBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wanCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer wanBackend.Close()
	peerBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer peerBackend.Close()

	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]isotopeRecord{
			{Name: "wan-ingress", Endpoint: wanBackend.URL, RegisteredAt: time.Now().UTC()},
			{Name: "peer-egress", Endpoint: peerBackend.URL, RegisteredAt: time.Now().UTC()},
		})
	}))
	defer registry.Close()

	store, a := testStore(t)
	if err := store.Add(a); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(newHandler(testConfig(registry.URL), store, &http.Client{}))
	defer proxy.Close()

	// The nonce belongs to wan-ingress. Presenting it with another invite must
	// not forward, and must not burn the wan-ingress expectation.
	req, _ := http.NewRequest(http.MethodPost, proxy.URL+"/forward?name=42", http.NoBody)
	req.Header.Set(inviteHeader, "peer-egress")
	req.Header.Set(ticketHeader, a.Nonce)
	resp, err := proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-name status = %d, want 403", resp.StatusCode)
	}
	if got := peerCalls.Load(); got != 0 {
		t.Fatalf("cross-name attempt reached the wrong backend %d times", got)
	}

	req, _ = http.NewRequest(http.MethodPost, proxy.URL+"/forward?name=42", http.NoBody)
	req.Header.Set(ticketHeader, a.Nonce)
	resp, err = proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("legitimate status = %d, want 204", resp.StatusCode)
	}

	resp, err = proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("replay status = %d, want 403", resp.StatusCode)
	}
	if got := wanCalls.Load(); got != 1 {
		t.Fatalf("ingress backend received %d calls, want exactly one", got)
	}
}

func TestRedTeamNonceFloodHasOneWinner(t *testing.T) {
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	registry := testRegistry(t, backend.URL)
	defer registry.Close()

	store, a := testStore(t)
	if err := store.Add(a); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(newHandler(testConfig(registry.URL), store, &http.Client{}))
	defer proxy.Close()

	const attackers = 16
	var ok, refused atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < attackers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, proxy.URL+"/forward?name=42", http.NoBody)
			req.Header.Set(ticketHeader, a.Nonce)
			resp, err := proxy.Client().Do(req)
			if err != nil {
				t.Errorf("flood request: %v", err)
				return
			}
			_ = resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusNoContent:
				ok.Add(1)
			case http.StatusForbidden:
				refused.Add(1)
			default:
				t.Errorf("unexpected flood status %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || refused.Load() != attackers-1 || calls.Load() != 1 {
		t.Fatalf("flood: ok=%d refused=%d backend calls=%d", ok.Load(), refused.Load(), calls.Load())
	}
}

func TestRedTeamChallengeRequiresPositiveTick(t *testing.T) {
	registry := testRegistry(t, "http://127.0.0.1:1")
	defer registry.Close()
	store, a := testStore(t)
	if err := store.Add(a); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(newHandler(testConfig(registry.URL), store, &http.Client{}))
	defer proxy.Close()

	for _, query := range []string{"rotation=rot-1", "rotation=rot-1&tick=0", "rotation=rot-1&tick=abc"} {
		resp, err := proxy.Client().Get(proxy.URL + "/challenge?" + query)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("challenge %q status = %d, want 403", query, resp.StatusCode)
		}
	}
}

func TestRedTeamChallengeCannotBeForgedByPeers(t *testing.T) {
	registry := testRegistry(t, "http://127.0.0.1:1")
	defer registry.Close()
	store, a := testStore(t)
	cfg := testConfig(registry.URL)
	attackerPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(announce.Status{
			LocalTick: 10,
			Seen:      []announce.Announcement{a}, // forged peer view
		})
	}))
	defer attackerPeer.Close()
	cfg.Peers = []string{attackerPeer.URL}

	// No local announcement: a lying peer must not confirm the rotation.
	proxy := httptest.NewServer(newHandler(cfg, store, &http.Client{}))
	resp, err := proxy.Client().Get(proxy.URL + "/challenge?rotation=rot-1&tick=10")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("peer-only challenge status = %d, want 403", resp.StatusCode)
	}
	proxy.Close()

	// Local announcement, peer refuses to corroborate: still not confirmed.
	if err := store.Add(a); err != nil {
		t.Fatal(err)
	}
	silentPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(announce.Status{LocalTick: 10})
	}))
	defer silentPeer.Close()
	cfg.Peers = []string{silentPeer.URL}
	proxy = httptest.NewServer(newHandler(cfg, store, &http.Client{}))
	defer proxy.Close()
	resp, err = proxy.Client().Get(proxy.URL + "/challenge?rotation=rot-1&tick=10")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("local-only challenge status = %d, want 403", resp.StatusCode)
	}
	var result challengeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Local || result.Confirmed {
		t.Fatalf("result = %+v, want local true but confirmed false", result)
	}
}
