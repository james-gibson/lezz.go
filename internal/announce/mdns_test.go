package announce

import "testing"

func TestBuildAndParseMDNSAnnouncement(t *testing.T) {
	a := signedAnnouncement(t, 10)
	txt := EncodeTXT(a)
	packet, err := BuildMDNSPacket("seed-42._lezz-rot._tcp.local.", txt)
	if err != nil {
		t.Fatal(err)
	}
	got := parseTXTAnswers(packet, "_lezz-rot._tcp.local.")
	if len(got) != 1 {
		t.Fatalf("got %d TXT records, want 1", len(got))
	}
	parsed, err := ParseTXT(got[0])
	if err != nil {
		t.Fatalf("parse announced TXT: %v", err)
	}
	if parsed != a {
		t.Fatalf("announcement = %+v, want %+v", parsed, a)
	}
	if others := parseTXTAnswers(packet, "_other._tcp.local."); len(others) != 0 {
		t.Fatalf("domain filter leaked %v", others)
	}
}
