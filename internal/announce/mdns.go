package announce

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
)

// MDNSSource listens for unsolicited mDNS TXT announcements on the local link.
// It is the localhost/LAN twin of DNSSource and uses only the standard library:
// a raw multicast UDP socket plus a small DNS TXT decoder.
type MDNSSource struct {
	Domain    string
	Interface string
	Port      int
}

// Run joins 224.0.0.251:5353 and hands signed TXT records to sink until the
// context is cancelled.
func (s *MDNSSource) Run(ctx context.Context, sink func(Announcement)) error {
	domain := normalizeDomain(s.Domain)
	if domain == "" {
		return nil
	}
	port := s.Port
	if port == 0 {
		port = 5353
	}
	var iface *net.Interface
	if s.Interface != "" {
		found, err := net.InterfaceByName(s.Interface)
		if err != nil {
			return fmt.Errorf("mdns interface %q: %w", s.Interface, err)
		}
		iface = found
	}
	addr := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: port}
	conn, err := net.ListenMulticastUDP("udp4", iface, addr)
	if err != nil {
		return fmt.Errorf("listen mdns %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	buf := make([]byte, 9000)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		for _, txt := range parseTXTAnswers(buf[:n], domain) {
			a, err := ParseTXT(txt)
			if err == nil {
				sink(a)
			}
		}
	}
}

func normalizeDomain(domain string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
}

func matchesDomain(name, domain string) bool {
	name = normalizeDomain(name)
	domain = normalizeDomain(domain)
	return name != "" && domain != "" && (name == domain || strings.HasSuffix(name, "."+domain))
}

func decodeName(msg []byte, off, depth int) (string, int, error) {
	if depth > 10 {
		return "", 0, fmt.Errorf("dns name too many pointers")
	}
	var labels []string
	for {
		if off >= len(msg) {
			return "", 0, io.ErrUnexpectedEOF
		}
		n := int(msg[off])
		switch {
		case n&0xc0 == 0xc0:
			if off+1 >= len(msg) {
				return "", 0, io.ErrUnexpectedEOF
			}
			ptr := ((n & 0x3f) << 8) | int(msg[off+1])
			if ptr >= len(msg) {
				return "", 0, fmt.Errorf("dns name pointer out of range")
			}
			suffix, _, err := decodeName(msg, ptr, depth+1)
			if err != nil {
				return "", 0, err
			}
			name := strings.Join(labels, ".")
			if suffix != "" {
				if name != "" {
					name += "."
				}
				name += suffix
			}
			return normalizeDomain(name), off + 2, nil
		case n == 0:
			return normalizeDomain(strings.Join(labels, ".")), off + 1, nil
		default:
			off++
			if off+n > len(msg) {
				return "", 0, io.ErrUnexpectedEOF
			}
			labels = append(labels, string(msg[off:off+n]))
			off += n
		}
	}
}

// parseTXTAnswers extracts TXT record bodies for one mDNS domain. mDNS answers
// commonly point their owner name into the question section, so compression is
// handled rather than assumed away.
func parseTXTAnswers(msg []byte, domain string) []string {
	if len(msg) < 12 {
		return nil
	}
	questions := int(msg[4])<<8 | int(msg[5])
	answers := int(msg[6])<<8 | int(msg[7])
	off := 12
	for i := 0; i < questions; i++ {
		name, next, err := decodeName(msg, off, 0)
		if err != nil {
			return nil
		}
		off = next
		if off+4 > len(msg) {
			return nil
		}
		off += 4
		_ = name
	}
	var out []string
	for i := 0; i < answers; i++ {
		name, next, err := decodeName(msg, off, 0)
		if err != nil {
			return out
		}
		off = next
		if off+10 > len(msg) {
			return out
		}
		recordType := int(msg[off])<<8 | int(msg[off+1])
		rdLength := int(msg[off+8])<<8 | int(msg[off+9])
		off += 10
		if off+rdLength > len(msg) {
			return out
		}
		if recordType == 16 && matchesDomain(name, domain) {
			if txt := decodeTXTRData(msg[off : off+rdLength]); txt != "" {
				out = append(out, txt)
			}
		}
		off += rdLength
	}
	return out
}

func decodeTXTRData(rdata []byte) string {
	var parts []string
	for len(rdata) > 0 {
		n := int(rdata[0])
		rdata = rdata[1:]
		if n > len(rdata) {
			break
		}
		parts = append(parts, string(rdata[:n]))
		rdata = rdata[n:]
	}
	return strings.Join(parts, " ")
}
