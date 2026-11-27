// Package announce carries signed rotational announcements over DNS TXT and
// mDNS TXT records.
//
// The transport is deliberately untrusted. A record is meaningful only when its
// seed-scoped HMAC verifies; DNS and mDNS just move the bytes. That keeps the
// seed/address plane in charge of naming while the signed border remains the
// enforcement boundary.
package announce

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Announcement is one rotation of one slot of one seed-addressed isotope.
type Announcement struct {
	Version   int    `json:"v"`
	Seed      string `json:"seed"`
	Name      string `json:"name"`
	Slot      string `json:"slot"`
	Rotation  string `json:"rotation"`
	Tick      int64  `json:"tick"`
	Prev      string `json:"prev"`
	Nonce     string `json:"nonce"`
	Signature string `json:"sig"`
}

// ParseTXT parses a TXT record body of key=value fields. Unknown fields are
// refused: a typo in a rotation record must not be silently ignored.
func ParseTXT(text string) (Announcement, error) {
	var a Announcement
	seen := map[string]bool{}
	for _, field := range strings.Fields(text) {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" || value == "" {
			return Announcement{}, fmt.Errorf("malformed field %q", field)
		}
		if seen[key] {
			return Announcement{}, fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = true
		switch key {
		case "v":
			n, err := strconv.Atoi(value)
			if err != nil || n != 1 {
				return Announcement{}, fmt.Errorf("unsupported version %q", value)
			}
			a.Version = n
		case "seed":
			a.Seed = value
		case "name":
			a.Name = value
		case "slot":
			a.Slot = value
		case "rotation":
			a.Rotation = value
		case "tick":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n <= 0 {
				return Announcement{}, fmt.Errorf("invalid tick %q", value)
			}
			a.Tick = n
		case "prev":
			a.Prev = value
		case "nonce":
			a.Nonce = value
		case "sig":
			a.Signature = value
		default:
			return Announcement{}, fmt.Errorf("unknown field %q", key)
		}
	}
	if a.Version != 1 || a.Seed == "" || a.Name == "" || a.Slot == "" ||
		a.Rotation == "" || a.Tick <= 0 || a.Nonce == "" || a.Signature == "" {
		return Announcement{}, fmt.Errorf("missing required field")
	}
	if err := a.validate(); err != nil {
		return Announcement{}, err
	}
	return a, nil
}

func (a Announcement) validate() error {
	for label, value := range map[string]string{
		"seed": a.Seed, "name": a.Name, "slot": a.Slot,
		"rotation": a.Rotation, "prev": a.Prev, "nonce": a.Nonce,
	} {
		if len(value) > 128 {
			return fmt.Errorf("%s too long", label)
		}
		if strings.ContainsAny(value, "|\r\n\t ") {
			return fmt.Errorf("%s contains a separator", label)
		}
	}
	if len(a.Signature) > 256 {
		return fmt.Errorf("sig too long")
	}
	return nil
}

// Canonical is the exact byte string covered by the signature. The field order
// is fixed so DNS and mDNS carry the same identity.
func (a Announcement) Canonical() string {
	return fmt.Sprintf("v=1|seed=%s|name=%s|slot=%s|rotation=%s|tick=%d|prev=%s|nonce=%s",
		a.Seed, a.Name, a.Slot, a.Rotation, a.Tick, a.Prev, a.Nonce)
}

func mac(key []byte, message string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(message))
	return h.Sum(nil)
}

// Sign fills in the HMAC over the canonical announcement.
func (a Announcement) Sign(key []byte) (Announcement, error) {
	if len(key) == 0 {
		return Announcement{}, fmt.Errorf("announcement key is empty")
	}
	if err := a.validate(); err != nil {
		return Announcement{}, err
	}
	a.Signature = base64.RawURLEncoding.EncodeToString(mac(key, a.Canonical()))
	return a, nil
}

// Verify checks the HMAC in constant time.
func (a Announcement) Verify(key []byte) error {
	if len(key) == 0 {
		return fmt.Errorf("announcement key is empty")
	}
	if err := a.validate(); err != nil {
		return err
	}
	got, err := base64.RawURLEncoding.DecodeString(a.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !hmac.Equal(got, mac(key, a.Canonical())) {
		return fmt.Errorf("bad announcement signature")
	}
	return nil
}

// EncodeTXT renders the record in the canonical field order, signature last.
// prev is omitted when empty so a first rotation still parses cleanly.
func EncodeTXT(a Announcement) string {
	fields := []string{
		"v=1",
		"seed=" + a.Seed,
		"name=" + a.Name,
		"slot=" + a.Slot,
		"rotation=" + a.Rotation,
		"tick=" + strconv.FormatInt(a.Tick, 10),
	}
	if a.Prev != "" {
		fields = append(fields, "prev="+a.Prev)
	}
	fields = append(fields, "nonce="+a.Nonce, "sig="+a.Signature)
	return strings.Join(fields, " ")
}

// DecodeKey accepts both raw URL and standard base64 so an operator can paste
// a key from either a DNS zone file or a shell.
func DecodeKey(text string) ([]byte, error) {
	if text == "" {
		return nil, fmt.Errorf("key is empty")
	}
	if key, err := base64.RawURLEncoding.DecodeString(text); err == nil && len(key) >= 16 {
		return key, nil
	}
	key, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	if len(key) < 16 {
		return nil, fmt.Errorf("key must be at least 16 bytes")
	}
	return key, nil
}
