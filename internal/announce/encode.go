package announce

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
)

// BuildMDNSPacket renders a small authoritative-style mDNS response carrying a
// single TXT record. The publisher uses it to announce without a third-party
// mDNS library.
func BuildMDNSPacket(domain, txt string) ([]byte, error) {
	name := normalizeDomain(domain)
	if name == "" || txt == "" {
		return nil, fmt.Errorf("domain and txt are required")
	}
	qname, err := encodeDNSName(name)
	if err != nil {
		return nil, err
	}
	rdata, err := encodeTXTStrings(txt)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	header := make([]byte, 12)
	binary.BigEndian.PutUint16(header[2:4], 0x8400) // response + authoritative
	binary.BigEndian.PutUint16(header[4:6], 1)      // question
	binary.BigEndian.PutUint16(header[6:8], 1)      // answer
	buf.Write(header)
	buf.Write(qname)
	_ = binary.Write(&buf, binary.BigEndian, uint16(16)) // TXT
	_ = binary.Write(&buf, binary.BigEndian, uint16(1))  // IN
	buf.Write(qname)
	_ = binary.Write(&buf, binary.BigEndian, uint16(16))
	_ = binary.Write(&buf, binary.BigEndian, uint16(1))
	_ = binary.Write(&buf, binary.BigEndian, uint32(120))
	_ = binary.Write(&buf, binary.BigEndian, uint16(len(rdata)))
	buf.Write(rdata)
	return buf.Bytes(), nil
}

func encodeDNSName(name string) ([]byte, error) {
	labels := strings.Split(normalizeDomain(name), ".")
	var out []byte
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return nil, fmt.Errorf("invalid dns label %q", label)
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0), nil
}

func encodeTXTStrings(text string) ([]byte, error) {
	var out []byte
	for len(text) > 0 {
		n := len(text)
		if n > 255 {
			n = 255
		}
		out = append(out, byte(n))
		out = append(out, text[:n]...)
		text = text[n:]
	}
	if len(out) == 0 {
		out = append(out, 0)
	}
	return out, nil
}
