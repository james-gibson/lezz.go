package announce

import (
	"strings"
	"testing"
	"time"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func signedAnnouncement(t *testing.T, tick int64) Announcement {
	t.Helper()
	a, err := Announcement{
		Version:  1,
		Seed:     "seed-42",
		Name:     "wan-ingress",
		Slot:     "slot-a",
		Rotation: "rot-1",
		Tick:     tick,
		Nonce:    "nonce-1",
	}.Sign(testKey)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestTXTRoundTripAndTamperDetection(t *testing.T) {
	a := signedAnnouncement(t, 10)
	text := EncodeTXT(a)
	got, err := ParseTXT(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != a {
		t.Fatalf("round trip = %+v, want %+v", got, a)
	}
	if err := got.Verify(testKey); err != nil {
		t.Fatalf("verify: %v", err)
	}
	tampered := strings.Replace(text, "rotation=rot-1", "rotation=rot-2", 1)
	bad, err := ParseTXT(tampered)
	if err != nil {
		t.Fatalf("parse tampered: %v", err)
	}
	if err := bad.Verify(testKey); err == nil {
		t.Fatal("tampered announcement verified")
	}
}

func TestParseTXTRefusesUnknownFields(t *testing.T) {
	if _, err := ParseTXT("v=1 seed=s name=n slot=x rotation=r tick=1 nonce=n sig=s extra=1"); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestStoreArmsOneShotExpectation(t *testing.T) {
	a := signedAnnouncement(t, 10)
	store := NewStoreWithClock(testKey, "seed-42", 5*time.Second, 0,
		func() time.Time { return time.Unix(50, 0) })
	if err := store.Add(a); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, ok := store.Consume(a.Name, a.Nonce)
	if !ok || got.Rotation != a.Rotation {
		t.Fatalf("consume = %+v, %v", got, ok)
	}
	if _, ok := store.Consume(a.Name, a.Nonce); ok {
		t.Fatal("nonce was reusable")
	}
}

func TestStoreAcceptsOneTickAheadAndRefusesReplay(t *testing.T) {
	a := signedAnnouncement(t, 11)
	store := NewStoreWithClock(testKey, "seed-42", 5*time.Second, 0,
		func() time.Time { return time.Unix(50, 0) })
	if err := store.Add(a); err != nil {
		t.Fatalf("ahead announcement refused: %v", err)
	}
	old := signedAnnouncement(t, 9)
	if err := store.Add(old); err == nil {
		t.Fatal("old tick accepted")
	}
	if err := store.Add(a); err == nil {
		t.Fatal("replayed rotation accepted")
	}
	if tick := store.LocalTick(); tick != 10 {
		t.Fatalf("local tick = %d, want 10", tick)
	}
}

func TestStoreExpiresExpectation(t *testing.T) {
	now := int64(50)
	a := signedAnnouncement(t, 10)
	store := NewStoreWithClock(testKey, "seed-42", 5*time.Second, 0,
		func() time.Time { return time.Unix(now, 0) })
	if err := store.Add(a); err != nil {
		t.Fatal(err)
	}
	now = 105 // two ticks later
	if _, ok := store.Consume(a.Name, a.Nonce); ok {
		t.Fatal("expired expectation was accepted")
	}
}

func TestDeskReturnsToEmptyWhenLeaseRunsOut(t *testing.T) {
	now := int64(50)
	a := signedAnnouncement(t, 10)
	store := NewStoreWithClock(testKey, "seed-42", 5*time.Second, 0,
		func() time.Time { return time.Unix(now, 0) })
	if err := store.Add(a); err != nil {
		t.Fatal(err)
	}

	desk := store.Desk("seed-42", a.Name, a.Slot)
	if desk.Empty || desk.Content != 0 || desk.Rotation != a.Rotation || desk.LeaseUntil == nil {
		t.Fatalf("live desk = %+v, want occupied with no content", desk)
	}
	if got := desk.LeaseUntil.Unix(); got != 60 {
		t.Fatalf("lease until = %d, want 60", got)
	}

	now = 61 // past the two-tick lease
	desk = store.Desk("seed-42", a.Name, a.Slot)
	if !desk.Empty || desk.Content != 0 || desk.LeaseUntil != nil {
		t.Fatalf("expired desk = %+v, want empty with no lease", desk)
	}
}
