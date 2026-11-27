// Command seed-announce signs a rotational announcement for a seed-addressed
// isotope. It can print the DNS TXT body for a zone, or multicast the signed
// record on the local link for the mDNS POC. No third-party mDNS library is
// needed.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/james-gibson/lezz.go/internal/announce"
)

func token() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("random token: %v", err)
	}
	return hex.EncodeToString(buf)
}

func main() {
	keyText := flag.String("key", "", "base64 announcement key")
	seed := flag.String("seed", "", "seed this rotation belongs to")
	name := flag.String("name", "wan-ingress", "isotope name whose slot rotates")
	slot := flag.String("slot", "", "slot being rotated")
	rotation := flag.String("rotation", "", "rotation id (random when omitted)")
	prev := flag.String("prev", "", "previous rotation id")
	nonce := flag.String("nonce", "", "one-shot ticket (random when omitted)")
	tick := flag.Int64("tick", 0, "absolute cluster tick (derived from the clock when omitted)")
	tickSeconds := flag.Int("tick-seconds", 5, "cluster tick length in seconds")
	offset := flag.Int64("tick-offset", 0, "local tick offset; use 1 for the node one tick ahead")
	dnsDomain := flag.String("dns-domain", "", "DNS owner name to print with the TXT body")
	mdnsDomain := flag.String("mdns-domain", "_lezz-rot._tcp.local.", "mDNS service domain")
	mdns := flag.Bool("mdns", false, "multicast the signed TXT record")
	flag.Parse()

	if *keyText == "" || *seed == "" || *slot == "" {
		log.Fatal("-key, -seed and -slot are required")
	}
	key, err := announce.DecodeKey(*keyText)
	if err != nil {
		log.Fatalf("announce key: %v", err)
	}
	rot := *rotation
	if rot == "" {
		rot = token()
	}
	oneTime := *nonce
	if oneTime == "" {
		oneTime = token()
	}
	current := *tick
	if current == 0 {
		interval := time.Duration(*tickSeconds) * time.Second
		if interval <= 0 {
			interval = time.Second
		}
		current = time.Now().Unix()/int64(interval/time.Second) + *offset
	}
	signed, err := announce.Announcement{
		Version:  1,
		Seed:     *seed,
		Name:     *name,
		Slot:     *slot,
		Rotation: rot,
		Tick:     current,
		Prev:     *prev,
		Nonce:    oneTime,
	}.Sign(key)
	if err != nil {
		log.Fatalf("sign announcement: %v", err)
	}
	txt := announce.EncodeTXT(signed)
	if *dnsDomain != "" {
		fmt.Printf("%s. TXT %q\n", strings.TrimSuffix(*dnsDomain, "."), txt)
	} else {
		fmt.Println(txt)
	}

	if *mdns {
		owner := *seed + "." + strings.TrimSuffix(*mdnsDomain, ".") + "."
		packet, err := announce.BuildMDNSPacket(owner, txt)
		if err != nil {
			log.Fatalf("build mdns packet: %v", err)
		}
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
		if err != nil {
			log.Fatalf("open mdns socket: %v", err)
		}
		addr := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
		_, sendErr := conn.WriteToUDP(packet, addr)
		_ = conn.Close()
		if sendErr != nil {
			log.Fatalf("send mdns announcement: %v", sendErr)
		}
		log.Printf("announced rotation=%s tick=%d nonce=%s", rot, current, oneTime)
	}
}
