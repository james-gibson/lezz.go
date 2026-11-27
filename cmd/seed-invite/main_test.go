package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/james-gibson/lezz.go/internal/announce"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func testConfig(registry string) Config {
	cfg := defaultConfig()
	cfg.Registry = registry
	cfg.Seed = "seed-42"
	cfg.Token = "invite-token"
	cfg.RequireAnnouncement = true
	return cfg
}

func testStore(t *testing.T) (*announce.Store, announce.Announcement) {
	t.Helper()
	store := announce.NewStoreWithClock(testKey, "seed-42", 5*time.Second, 0,
		func() time.Time { return time.Unix(50, 0) })
	a, err := announce.Announcement{
		Version:  1,
		Seed:     "seed-42",
		Name:     "wan-ingress",
		Slot:     "slot-a",
		Rotation: "rot-1",
		Tick:     10,
		Nonce:    "nonce-1",
	}.Sign(testKey)
	if err != nil {
		t.Fatal(err)
	}
	return store, a
}

func TestDeskEndpoint(t *testing.T) {
	now := int64(50)
	store := announce.NewStoreWithClock(testKey, "seed-42", 5*time.Second, 0,
		func() time.Time { return time.Unix(now, 0) })
	a, err := announce.Announcement{
		Version:  1,
		Seed:     "seed-42",
		Name:     "wan-ingress",
		Slot:     "slot-a",
		Rotation: "rot-desk",
		Tick:     10,
		Nonce:    "nonce-desk",
	}.Sign(testKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig("http://127.0.0.1:1")
	cfg.Slot = "slot-a"
	proxy := httptest.NewServer(newHandler(cfg, store, &http.Client{}))
	defer proxy.Close()

	getDesk := func() announce.Desk {
		t.Helper()
		resp, err := proxy.Client().Get(proxy.URL + "/desk")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("desk status = %d, want 200", resp.StatusCode)
		}
		var desk announce.Desk
		if err := json.NewDecoder(resp.Body).Decode(&desk); err != nil {
			t.Fatal(err)
		}
		return desk
	}

	if desk := getDesk(); !desk.Empty || desk.Content != 0 || desk.LeaseUntil != nil {
		t.Fatalf("boot desk = %+v, want empty", desk)
	}
	if err := store.Add(a); err != nil {
		t.Fatal(err)
	}
	if desk := getDesk(); desk.Empty || desk.Content != 0 || desk.Rotation != a.Rotation || desk.LeaseUntil == nil {
		t.Fatalf("armed desk = %+v, want occupied with no content", desk)
	}
	now = 61
	if desk := getDesk(); !desk.Empty || desk.Content != 0 || desk.LeaseUntil != nil {
		t.Fatalf("expired desk = %+v, want empty", desk)
	}
}

func testRegistry(t *testing.T, endpoint string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]isotopeRecord{{
			Name:         "wan-ingress",
			Endpoint:     endpoint,
			RegisteredAt: time.Now().UTC(),
		}})
	}))
}

func TestSeedInviteRequiresAndConsumesAnnouncement(t *testing.T) {
	var gotRotation, gotAuth string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRotation = r.Header.Get(rotationHeader)
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	registry := testRegistry(t, backend.URL)
	defer registry.Close()

	store, a := testStore(t)
	proxy := httptest.NewServer(newHandler(testConfig(registry.URL), store, &http.Client{}))
	defer proxy.Close()

	resp, err := proxy.Client().Post(proxy.URL+"/mcp", "application/json", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status without ticket = %d, want 403", resp.StatusCode)
	}

	if err := store.Add(a); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/mcp", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(ticketHeader, a.Nonce)
	resp, err = proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status with ticket = %d, want 204", resp.StatusCode)
	}
	if gotRotation != a.Rotation {
		t.Errorf("rotation header = %q, want %q", gotRotation, a.Rotation)
	}
	if gotAuth != "Bearer invite-token" {
		t.Errorf("Authorization = %q, want the invite grant", gotAuth)
	}

	resp, err = proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("replayed ticket status = %d, want 403", resp.StatusCode)
	}
}

func TestSeedInviteStatusReportsExpectation(t *testing.T) {
	registry := testRegistry(t, "http://127.0.0.1:1")
	defer registry.Close()
	store, a := testStore(t)
	if err := store.Add(a); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(newHandler(testConfig(registry.URL), store, &http.Client{}))
	defer proxy.Close()

	resp, err := proxy.Client().Get(proxy.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var status announce.Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Expected != 1 || status.LocalTick != 10 {
		t.Fatalf("status = %+v, want one expectation at tick 10", status)
	}
}

func TestInversionChallengeChecksPeers(t *testing.T) {
	registry := testRegistry(t, "http://127.0.0.1:1")
	defer registry.Close()
	store, a := testStore(t)
	if err := store.Add(a); err != nil {
		t.Fatal(err)
	}

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(announce.Status{
			LocalTick: 10,
			Seen:      []announce.Announcement{a},
		})
	}))
	defer peer.Close()

	cfg := testConfig(registry.URL)
	cfg.Peers = []string{peer.URL}
	proxy := httptest.NewServer(newHandler(cfg, store, &http.Client{}))
	defer proxy.Close()

	resp, err := proxy.Client().Get(proxy.URL + "/challenge?rotation=rot-1&tick=10")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("challenge status = %d, want 200", resp.StatusCode)
	}
	var result challengeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Confirmed || !result.Local || len(result.Peers) != 1 || !result.Peers[0].OK {
		t.Fatalf("challenge result = %+v, want local + peer confirmation", result)
	}
}
