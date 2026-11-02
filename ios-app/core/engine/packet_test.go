package engine

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/checksum"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

func v4Packet(proto uint8, total, ihl int, flags uint16, buflen int) []byte {
	b := make([]byte, buflen)
	b[0] = 0x40 | byte(ihl)
	binary.BigEndian.PutUint16(b[2:4], uint16(total))
	binary.BigEndian.PutUint16(b[6:8], flags)
	b[9] = proto
	copy(b[12:16], []byte{10, 66, 66, 2})
	copy(b[16:20], []byte{1, 1, 1, 1})
	return b
}

func TestParsePacket(t *testing.T) {
	v6 := func(plen, buflen int) []byte {
		b := make([]byte, buflen)
		b[0] = 0x60
		binary.BigEndian.PutUint16(b[4:6], uint16(plen))
		b[6] = protoUDP
		b[23] = 2
		b[39] = 1
		return b
	}
	for _, tc := range []struct {
		name    string
		pkt     []byte
		ok      bool
		frag    bool
		payload int
	}{
		{"empty", nil, false, false, 0},
		{"short v4", make([]byte, 19), false, false, 0},
		{"v4 ok", v4Packet(protoUDP, 28, 5, 0, 28), true, false, 8},
		{"v4 trailing padding ignored", v4Packet(protoUDP, 28, 5, 0, 40), true, false, 8},
		{"v4 options", v4Packet(protoTCP, 44, 6, 0, 44), true, false, 20},
		{"ihl < 5", v4Packet(protoUDP, 28, 4, 0, 28), false, false, 0},
		{"total < ihl", v4Packet(protoUDP, 10, 5, 0, 28), false, false, 0},
		{"total > buffer", v4Packet(protoUDP, 60, 5, 0, 28), false, false, 0},
		{"MF flag", v4Packet(protoUDP, 28, 5, 0x2000, 28), true, true, 8},
		{"fragment offset", v4Packet(protoUDP, 28, 5, 0x0010, 28), true, true, 8},
		{"DF only", v4Packet(protoUDP, 28, 5, 0x4000, 28), true, false, 8},
		{"short v6", make([]byte, 39), false, false, 0},
		{"v6 ok", v6(8, 48), true, false, 8},
		{"v6 trailing ignored", v6(8, 60), true, false, 8},
		{"v6 payload past buffer", v6(20, 48), false, false, 0},
		{"version 5", append([]byte{0x50}, make([]byte, 40)...), false, false, 0},
	} {
		p, ok := parsePacket(tc.pkt)
		if ok != tc.ok {
			t.Errorf("%s: ok=%v", tc.name, ok)
			continue
		}
		if !ok {
			continue
		}
		if p.frag != tc.frag || len(p.payload) != tc.payload {
			t.Errorf("%s: frag=%v payload=%d", tc.name, p.frag, len(p.payload))
		}
	}
	p, _ := parsePacket(v4Packet(protoUDP, 28, 5, 0, 28))
	if p.version != 4 || p.proto != protoUDP || p.src != netip.MustParseAddr("10.66.66.2") || p.dst != netip.MustParseAddr("1.1.1.1") {
		t.Fatalf("%+v", p)
	}
	p, _ = parsePacket(v6(8, 48))
	if p.version != 6 || p.src != netip.MustParseAddr("::2") || p.dst != netip.MustParseAddr("::1") {
		t.Fatalf("%+v", p)
	}
}

func TestBuildUDPChecksums(t *testing.T) {
	for _, tc := range []struct{ src, dst string }{
		{"1.2.3.4:53", "10.66.66.2:40000"},
		{"[2606:4700::1111]:443", "[fd0b:a75::a42:4202]:5000"},
	} {
		src, dst := netip.MustParseAddrPort(tc.src), netip.MustParseAddrPort(tc.dst)
		for _, data := range [][]byte{nil, []byte("x"), []byte("hello world"), bytes.Repeat([]byte{0xab}, 1301)} {
			pkt := buildUDP(src, dst, data)
			p, ok := parsePacket(pkt)
			if !ok || p.proto != protoUDP || p.src != src.Addr() || p.dst != dst.Addr() {
				t.Fatalf("%s: parse %+v %v", tc.src, p, ok)
			}
			if src.Addr().Is4() {
				h := header.IPv4(pkt)
				if !h.IsValid(len(pkt)) || !h.IsChecksumValid() || h.TTL() != 64 {
					t.Fatalf("bad IPv4 header %x", pkt[:20])
				}
			} else if header.IPv6(pkt).HopLimit() != 64 {
				t.Fatal("hop limit")
			}
			u := header.UDP(p.payload)
			if u.Checksum() == 0 {
				t.Fatal("UDP checksum must never be 0")
			}
			s, d := tcpip.AddrFromSlice(src.Addr().AsSlice()), tcpip.AddrFromSlice(dst.Addr().AsSlice())
			if !u.IsChecksumValid(s, d, checksum.Checksum(u.Payload(), 0)) {
				t.Fatalf("bad UDP checksum for %s, %d bytes", tc.src, len(data))
			}
			if u.SourcePort() != src.Port() || u.DestinationPort() != dst.Port() || !bytes.Equal(u.Payload(), data) {
				t.Fatal("UDP fields")
			}
		}
	}
	if buildUDP(netip.MustParseAddrPort("1.2.3.4:1"), netip.MustParseAddrPort("5.6.7.8:2"), make([]byte, 65508)) != nil {
		t.Fatal("oversized datagram must be refused")
	}
}

func TestBuildEchoReply(t *testing.T) {
	pkt := buildEchoReply(netip.MustParseAddr("10.66.66.1"), netip.MustParseAddr("10.66.66.2"), 0x1234, 7, []byte("hi there"))
	if !header.IPv4(pkt).IsChecksumValid() {
		t.Fatal("IPv4 checksum")
	}
	p, _ := parsePacket(pkt)
	if checksum.Checksum(p.payload, 0) != 0xffff {
		t.Fatal("ICMP checksum")
	}
	m, err := icmp.ParseMessage(1, p.payload)
	if err != nil || m.Type != ipv4.ICMPTypeEchoReply || m.Code != 0 {
		t.Fatalf("%+v %v", m, err)
	}
	e := m.Body.(*icmp.Echo)
	if e.ID != 0x1234 || e.Seq != 7 || string(e.Data) != "hi there" {
		t.Fatalf("%+v", e)
	}
}

func TestDropDestination(t *testing.T) {
	for addr, drop := range map[string]bool{
		"10.66.66.255":    true,
		"192.168.1.255":   true,
		"172.16.0.255":    true,
		"255.255.255.255": true,
		"fe80::1":         true,
		"ff02::1":         true,
		"224.0.0.251":     true,
		"169.254.1.1":     true,
		"::1":             true,
		"127.0.0.1":       true,
		"0.0.0.0":         true,
		"::":              true,
		"8.8.8.8":         false,
		"172.217.3.255":   false, // public hosts in 172/8 and 192/8 used to be blocked
		"192.0.43.255":    false,
		"10.66.66.3":      false, // other clients: dialed out, not blocked
		"11.1.1.255":      false,
		"2606:4700::1111": false,
	} {
		if got := dropDestination(netip.MustParseAddr(addr)); got != drop {
			t.Errorf("%s: %v", addr, got)
		}
	}
}

func TestParseIPv6ExtensionHeaders(t *testing.T) {
	udp := buildUDP(netip.MustParseAddrPort("[fd0b:a75::a42:4202]:5000"), netip.MustParseAddrPort("[2001:db8::1]:53"), []byte("q"))
	// Insert an 8-byte hop-by-hop header between the IPv6 header and UDP.
	pkt := append(append(append([]byte(nil), udp[:40]...), protoUDP, 0, 1, 4, 0, 0, 0, 0), udp[40:]...)
	pkt[6] = 0 // next header: hop-by-hop
	binary.BigEndian.PutUint16(pkt[4:6], uint16(len(pkt)-40))
	p, ok := parsePacket(pkt)
	if !ok || p.proto != protoUDP || p.frag || len(p.payload) != len(udp)-40 {
		t.Fatalf("%+v %v", p, ok)
	}
}

func TestFragmentAndReassemble(t *testing.T) {
	data := make([]byte, 3000)
	for i := range data {
		data[i] = byte(i * 7)
	}
	for _, pair := range [][2]string{
		{"10.66.66.1:53", "10.66.66.2:5353"},
		{"[fd0b:a75::a42:4201]:53", "[fd0b:a75::a42:4202]:5353"},
	} {
		whole := buildUDP(netip.MustParseAddrPort(pair[0]), netip.MustParseAddrPort(pair[1]), data)
		frags := fragment(whole, 1280)
		if len(frags) < 3 {
			t.Fatalf("%s: %d fragments", pair[0], len(frags))
		}
		r := newReassembler()
		var got ipPacket
		done := false
		// Out of order, the way networks deliver them sometimes.
		for _, i := range []int{2, 0, 1} {
			if len(frags[i]) > 1280 {
				t.Fatalf("fragment of %d bytes", len(frags[i]))
			}
			p, ok := parsePacket(frags[i])
			if !ok || !p.frag || p.proto != protoUDP {
				t.Fatalf("fragment %d: %+v %v", i, p, ok)
			}
			got, done = r.add(&p)
		}
		for i := 3; i < len(frags) && !done; i++ {
			p, _ := parsePacket(frags[i])
			got, done = r.add(&p)
		}
		want, _ := parsePacket(whole)
		if !done || !bytes.Equal(got.payload, want.payload) || got.src != want.src || got.dst != want.dst {
			t.Fatalf("%s: reassembled %d bytes, done %v", pair[0], len(got.payload), done)
		}
	}
	if small := buildUDP(netip.MustParseAddrPort("10.66.66.1:53"), netip.MustParseAddrPort("10.66.66.2:1"), []byte("x")); len(fragment(small, 1280)) != 1 {
		t.Fatal("small packets must not be split")
	}
}

func TestIPv6PingNotFaked(t *testing.T) {
	s := testServer(t)
	client := netip.MustParseAddr("fd0b:a75::a42:4202")
	echo := func(dst netip.Addr) []byte {
		pkt, hl, _ := ipHeader(client, dst, protoICMPv6, 12)
		m := pkt[hl:]
		m[0] = icmpv6EchoRequest
		binary.BigEndian.PutUint16(m[4:6], 0x77)
		binary.BigEndian.PutUint16(m[6:8], 1)
		copy(m[8:], "ping")
		binary.BigEndian.PutUint16(m[2:4], csumFold(csumAdd(pseudoSum(client, dst, protoICMPv6, len(m)), m)))
		return pkt
	}
	// The phone's own tunnel address answers right away, with a valid checksum.
	gw6, _ := peerAddr6(s.gw)
	if !s.intercept(echo(gw6)) {
		t.Fatal("echo to the gateway not intercepted")
	}
	p, ok := s.toClient(t, time.Second)
	if !ok || p.proto != protoICMPv6 || p.payload[0] != icmpv6EchoReply || p.src != gw6 || string(p.payload[8:]) != "ping" {
		t.Fatalf("reply %+v %v", p, ok)
	}
	if csumFold(csumAdd(pseudoSum(p.src, p.dst, protoICMPv6, len(p.payload)), p.payload)) != 0 {
		t.Fatal("bad ICMPv6 checksum")
	}
	// Anything else is forwarded for real, never answered by the stack: an
	// unreachable documentation address gets no reply.
	if !s.intercept(echo(netip.MustParseAddr("2001:db8::1"))) {
		t.Fatal("echo not intercepted")
	}
	if p, ok := s.toClient(t, 700*time.Millisecond); ok {
		t.Fatalf("fake reply %+v", p)
	}
}
