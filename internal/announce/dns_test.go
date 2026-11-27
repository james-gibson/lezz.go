package announce

import (
	"context"
	"testing"
	"time"
)

func TestDNSSourceOffersSignedRecords(t *testing.T) {
	a := signedAnnouncement(t, 10)
	domain := "_rot.seed-42.lab.example."
	source := &DNSSource{
		Domain:   domain,
		Interval: time.Hour,
		Lookup: func(got string) ([]string, error) {
			if got != domain {
				t.Errorf("lookup domain = %q, want %q", got, domain)
			}
			return []string{"junk", EncodeTXT(a)}, nil
		},
	}
	got := make(chan Announcement, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = source.Run(ctx, func(a Announcement) { got <- a }) }()
	select {
	case announced := <-got:
		if announced != a {
			t.Fatalf("announcement = %+v, want %+v", announced, a)
		}
	case <-time.After(time.Second):
		t.Fatal("DNS source did not offer the signed record")
	}
}
