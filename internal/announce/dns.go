package announce

import (
	"context"
	"log"
	"net"
	"time"
)

// LookupTXT is injectable so tests can stand in for a zone without binding a
// resolver.
type LookupTXT func(string) ([]string, error)

// DNSSource polls one TXT record and offers each signed announcement it finds.
// DNS is the production transport: it crosses the WAN, respects the seed-set
// name, and needs no inbound port on the announcing client.
type DNSSource struct {
	Domain   string
	Interval time.Duration
	Lookup   LookupTXT
}

// Run polls until the context is cancelled. Parse failures are ignored: a bad
// TXT record is not an announcement, and must not stop the watcher.
func (s *DNSSource) Run(ctx context.Context, sink func(Announcement)) error {
	if s.Domain == "" {
		return nil
	}
	interval := s.Interval
	if interval <= 0 {
		interval = time.Second
	}
	lookup := s.Lookup
	if lookup == nil {
		lookup = net.LookupTXT
	}
	poll := func() {
		records, err := lookup(s.Domain)
		if err != nil {
			log.Printf("announce dns lookup %s: %v", s.Domain, err)
			return
		}
		for _, record := range records {
			a, err := ParseTXT(record)
			if err != nil {
				continue
			}
			sink(a)
		}
	}
	poll()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			poll()
		}
	}
}
