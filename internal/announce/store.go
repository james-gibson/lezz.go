package announce

import (
	"fmt"
	"sync"
	"time"
)

// Expectation is a signed rotation armed for a short, tick-scoped window. It is
// one-shot: seeing the announcement lets a call be expected, not trusted.
type Expectation struct {
	Announcement Announcement `json:"announcement"`
	ExpiresAt    time.Time    `json:"expires_at"`
}

// Status is the monitor-facing view of the address plane.
type Status struct {
	LocalTick   int64                   `json:"local_tick"`
	TickSeconds int64                   `json:"tick_seconds"`
	TickOffset  int64                   `json:"tick_offset"`
	Expected    int                     `json:"expected"`
	Last        map[string]Announcement `json:"last"`
	Seen        []Announcement          `json:"seen"`
}

// Desk is the le.ad view: a connection point that holds no content. It is
// non-empty only while a signed expectation is inside its lease window, and it
// returns to empty when that lease runs out or the ticket is consumed.
type Desk struct {
	Seed       string     `json:"seed"`
	Name       string     `json:"name"`
	Slot       string     `json:"slot"`
	Content    int        `json:"content"`
	Empty      bool       `json:"empty"`
	Rotation   string     `json:"rotation,omitempty"`
	Tick       int64      `json:"tick,omitempty"`
	LeaseUntil *time.Time `json:"lease_until,omitempty"`
}

// Store verifies announcements and holds the expectations they arm.
type Store struct {
	mu       sync.Mutex
	key      []byte
	seed     string
	interval time.Duration
	offset   int64
	now      func() time.Time

	expected map[string]Expectation
	last     map[string]Announcement
	seen     []Announcement
}

// NewStore builds a store. An empty seed accepts any seed; a production config
// pins one so a key for another seed cannot arm this process.
func NewStore(key []byte, seed string, interval time.Duration, offset int64) *Store {
	return NewStoreWithClock(key, seed, interval, offset, time.Now)
}

// NewStoreWithClock is the injectable-clock form used by tests and by a mock
// cluster tick.
func NewStoreWithClock(key []byte, seed string, interval time.Duration, offset int64, now func() time.Time) *Store {
	if interval < time.Second {
		interval = time.Second
	}
	if now == nil {
		now = time.Now
	}
	return &Store{
		key:      key,
		seed:     seed,
		interval: interval,
		offset:   offset,
		now:      now,
		expected: map[string]Expectation{},
		last:     map[string]Announcement{},
	}
}

func slotKey(name, slot string) string    { return name + "/" + slot }
func ticketKey(name, nonce string) string { return name + "\x00" + nonce }

// LocalTick is the node's cluster tick. A node configured one tick ahead
// announces N+1 while its peers are still on N; that phase skew is the evidence
// the upstream monitor challenges.
func (s *Store) LocalTick() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.localTick()
}

func (s *Store) localTick() int64 {
	return s.now().Unix()/int64(s.interval/time.Second) + s.offset
}

// Add verifies an announcement and arms its one-shot expectation. Old ticks are
// refused, and a slot may not move backwards — replaying a DNS record cannot
// manufacture a new rotation.
func (s *Store) Add(a Announcement) error {
	if err := a.Verify(s.key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.seed != "" && a.Seed != s.seed {
		return fmt.Errorf("announcement seed %q does not match configured seed %q", a.Seed, s.seed)
	}
	local := s.localTick()
	if a.Tick < local || a.Tick > local+1 {
		return fmt.Errorf("tick %d outside window [%d,%d]", a.Tick, local, local+1)
	}
	sk := slotKey(a.Name, a.Slot)
	if last, ok := s.last[sk]; ok && a.Tick <= last.Tick {
		return fmt.Errorf("stale rotation %q at tick %d (have tick %d)", a.Rotation, a.Tick, last.Tick)
	}

	expires := s.now().Add(2 * s.interval)
	s.last[sk] = a
	s.expected[ticketKey(a.Name, a.Nonce)] = Expectation{Announcement: a, ExpiresAt: expires}
	s.seen = append(s.seen, a)
	if len(s.seen) > 32 {
		s.seen = append([]Announcement(nil), s.seen[len(s.seen)-32:]...)
	}
	return nil
}

// Consume discharges an expectation. The entry is removed either way so a
// wrong or expired ticket cannot be retried forever.
func (s *Store) Consume(name, nonce string) (Announcement, bool) {
	if name == "" || nonce == "" {
		return Announcement{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key := ticketKey(name, nonce)
	exp, ok := s.expected[key]
	delete(s.expected, key)
	if !ok || s.now().After(exp.ExpiresAt) {
		return Announcement{}, false
	}
	return exp.Announcement, true
}

// Confirm reports whether this node has seen the rotation the monitor is
// challenging. Confirmation is local; the proxy's challenge endpoint adds the
// peer views that make it a cluster fact.
func (s *Store) Confirm(name, rotation string, tick int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.seen {
		if a.Name == name && a.Rotation == rotation && a.Tick >= tick {
			return true
		}
	}
	return false
}

// Status prunes expired expectations and returns a copy for the monitor.
func (s *Store) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for key, exp := range s.expected {
		if now.After(exp.ExpiresAt) {
			delete(s.expected, key)
		}
	}
	last := make(map[string]Announcement, len(s.last))
	for key, a := range s.last {
		last[key] = a
	}
	seen := append([]Announcement(nil), s.seen...)
	return Status{
		LocalTick:   s.localTick(),
		TickSeconds: int64(s.interval / time.Second),
		TickOffset:  s.offset,
		Expected:    len(s.expected),
		Last:        last,
		Seen:        seen,
	}
}

// Desk reports the live lease for one slot. It prunes expired expectations on
// the way out, so a desk whose lease has run out is reported as empty.
func (s *Store) Desk(seed, name, slot string) Desk {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for key, exp := range s.expected {
		if now.After(exp.ExpiresAt) {
			delete(s.expected, key)
		}
	}

	desk := Desk{Seed: seed, Name: name, Slot: slot, Content: 0, Empty: true}
	var best Expectation
	found := false
	for _, exp := range s.expected {
		a := exp.Announcement
		if seed != "" && a.Seed != seed {
			continue
		}
		if a.Name != name || a.Slot != slot {
			continue
		}
		if !found || a.Tick > best.Announcement.Tick {
			best = exp
			found = true
		}
	}
	if found {
		until := best.ExpiresAt
		desk.Empty = false
		desk.Rotation = best.Announcement.Rotation
		desk.Tick = best.Announcement.Tick
		desk.LeaseUntil = &until
	}
	return desk
}
