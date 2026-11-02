package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// testServer is a Server with just the parts the UDP, DNS and ping paths
// use: no WireGuard, packets for clients land in dev.out.
func testServer(t *testing.T, dns ...string) *Server {
	t.Helper()
	log := NewLog(200)
	log.SetEcho(func(s string) { t.Log(s) })
	s := &Server{
		log:      log,
		gw:       netip.MustParseAddr("10.66.66.1"),
		dnsUp:    parseDNS(dns),
		dnsSlots: make(chan struct{}, dnsInFlight),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.eg = newEgress(s.ctx, false, log)
	var err error
	if s.dev, err = newNetDev(1420, s.gw, true); err != nil {
		t.Fatal(err)
	}
	s.udp = newUDPNAT(s)
	s.frags = newReassembler()
	s.ping = newPinger(s)
	t.Cleanup(func() {
		s.cancel()
		s.udp.reap(0)
		s.ping.reap(0)
		s.dev.Close()
	})
	return s
}

// fromClient feeds a UDP datagram from a client through the interceptor.
func (s *Server) fromClient(t *testing.T, src, dst string, data []byte) bool {
	t.Helper()
	return s.intercept(buildUDP(netip.MustParseAddrPort(src), netip.MustParseAddrPort(dst), data))
}

// toClient waits for the next packet sent to a client.
func (s *Server) toClient(t *testing.T, wait time.Duration) (ipPacket, bool) {
	t.Helper()
	select {
	case b := <-s.dev.out:
		p, ok := parsePacket(b)
		if !ok {
			t.Fatalf("unparseable packet to client: %x", b)
		}
		return p, true
	case <-time.After(wait):
		return ipPacket{}, false
	}
}

func udpData(p ipPacket) (src, dst uint16, data []byte) {
	return binary.BigEndian.Uint16(p.payload[0:2]), binary.BigEndian.Uint16(p.payload[2:4]), p.payload[8:]
}

func TestUDPFlowLifecycle(t *testing.T) {
	s := testServer(t)
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: hostIPv4(t).AsSlice()})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 2000)
		for {
			n, from, err := echo.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			echo.WriteToUDPAddrPort(buf[:n], from)
		}
	}()
	dst := echo.LocalAddr().(*net.UDPAddr).AddrPort().String()

	if !s.fromClient(t, "10.66.66.2:40000", dst, []byte("ping")) {
		t.Fatal("UDP must be consumed by the interceptor")
	}
	p, ok := s.toClient(t, 2*time.Second)
	if !ok {
		t.Fatal("no echo")
	}
	sp, dp, data := udpData(p)
	if p.dst != netip.MustParseAddr("10.66.66.2") || dp != 40000 || string(data) != "ping" || netip.AddrPortFrom(p.src, sp).String() != dst {
		t.Fatalf("echo %v:%d -> %v:%d %q", p.src, sp, p.dst, dp, data)
	}
	if s.udp.count() != 1 || s.bytesUp.Load() != 4 || s.bytesDown.Load() != 4 {
		t.Fatalf("flows %d up %d down %d", s.udp.count(), s.bytesUp.Load(), s.bytesDown.Load())
	}
	// Same client endpoint, same flow; another endpoint, another flow.
	s.fromClient(t, "10.66.66.2:40000", dst, []byte("again"))
	s.fromClient(t, "10.66.66.2:40001", dst, []byte("other"))
	s.toClient(t, 2*time.Second)
	s.toClient(t, 2*time.Second)
	if s.udp.count() != 2 {
		t.Fatalf("flows %d", s.udp.count())
	}

	// Not flows: DNS to the gateway, other gateway ports, blocked
	// destinations, a fragment still waiting for the rest, short datagrams.
	s.fromClient(t, "10.66.66.2:5000", "10.66.66.1:1234", []byte("x"))
	s.fromClient(t, "10.66.66.2:5001", "192.168.1.255:137", []byte("x"))
	s.fromClient(t, "10.66.66.2:5002", "224.0.0.251:5353", []byte("x"))
	frag := buildUDP(netip.MustParseAddrPort("10.66.66.2:5003"), netip.MustParseAddrPort(dst), []byte("x"))
	frag[6] |= 0x20 // MF
	if !s.intercept(frag) {
		t.Fatal("fragments are consumed (held for reassembly)")
	}
	short := buildUDP(netip.MustParseAddrPort("10.66.66.2:5004"), netip.MustParseAddrPort(dst), nil)[:24]
	short[3] = 24
	s.intercept(short)
	if s.udp.count() != 2 {
		t.Fatalf("flows %d after non-flow traffic", s.udp.count())
	}
	if _, ok := s.toClient(t, 200*time.Millisecond); ok {
		t.Fatal("unexpected reply")
	}

	// Idle flows are reaped and their sockets closed.
	s.udp.reap(udpIdle)
	if s.udp.count() != 2 {
		t.Fatal("reaped active flows")
	}
	s.udp.mu.Lock()
	var f *udpFlow
	for _, fl := range s.udp.flows {
		fl.last.Store(time.Now().Add(-udpIdle).UnixNano())
		f = fl
	}
	s.udp.mu.Unlock()
	s.udp.reap(udpIdle)
	if s.udp.count() != 0 {
		t.Fatal("idle flows left")
	}
	if _, err := f.socket(false); err != errFlowClosed {
		t.Fatalf("closed flow opened a socket: %v", err)
	}
}

func TestUDPFlowLimit(t *testing.T) {
	s := testServer(t)
	s.udp.mu.Lock()
	for i := 0; i < maxUDPFlows; i++ {
		ap := netip.AddrPortFrom(netip.MustParseAddr("10.66.66.2"), uint16(i+1))
		s.udp.flows[ap] = &udpFlow{nat: s.udp, client: ap}
	}
	s.udp.mu.Unlock()
	s.fromClient(t, "10.66.66.3:9", "8.8.8.8:53", []byte("x"))
	if n := s.udp.count(); n != maxUDPFlows {
		t.Fatalf("%d flows", n)
	}
	s.fails.flush(s.log, time.Now(), true)
	if !strings.Contains(s.log.Tail(), reasonTooMany) {
		t.Fatal(s.log.Tail())
	}
}

// fakeUpstream answers DNS queries with a fixed reply; with mute it never
// answers, and with decoy another socket first sends a fake answer.
func fakeUpstream(t *testing.T, ip netip.Addr, reply []byte, mute, decoy bool) string {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip.AsSlice()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	d, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: ip.AsSlice()})
	t.Cleanup(func() { d.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := c.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if mute {
				continue
			}
			if decoy {
				// From another port, and then a forged answer from the right
				// port with the wrong ID: both must be ignored.
				d.WriteToUDPAddrPort([]byte("decoy"), from)
				forged := append([]byte(nil), buf[:n]...)
				forged[0] ^= 0xff
				forged[2] |= 0x80
				c.WriteToUDPAddrPort(append(forged, "forged"...), from)
				time.Sleep(50 * time.Millisecond)
			}
			// A real resolver echoes the ID and question and sets QR.
			answer := append([]byte(nil), buf[:n]...)
			answer[2] |= 0x80
			c.WriteToUDPAddrPort(append(answer, reply...), from)
		}
	}()
	return c.LocalAddr().String()
}

// testQuery is a DNS query for example.com/A with ID 0x1234.
var testQuery = []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0,
	7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}

func TestDNSAnswers(t *testing.T) {
	ok := append(append([]byte(nil), testQuery...), "rest"...)
	ok[2] |= 0x80
	upper := append([]byte(nil), ok...)
	upper[13] = 'E' // resolvers may change case (0x20 encoding)
	wrongID := append([]byte(nil), ok...)
	wrongID[1] ^= 1
	notResponse := append([]byte(nil), ok...)
	notResponse[2] &^= 0x80
	otherName := append([]byte(nil), ok...)
	otherName[14] = 'z'
	for name, c := range map[string]struct {
		resp []byte
		want bool
	}{"ok": {ok, true}, "case": {upper, true}, "id": {wrongID, false}, "qr": {notResponse, false},
		"name": {otherName, false}, "short": {ok[:8], false}} {
		if got := dnsAnswers(testQuery, c.resp); got != c.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
}

func TestDNSUpstreams(t *testing.T) {
	ip := hostIPv4(t)
	dead := fakeUpstream(t, ip, nil, true, false)
	good := fakeUpstream(t, ip, []byte("answer"), false, true)
	s := testServer(t, dead, good)

	start := time.Now()
	if !s.fromClient(t, "10.66.66.2:5353", "10.66.66.1:53", testQuery) {
		t.Fatal("not consumed")
	}
	p, ok := s.toClient(t, 5*time.Second)
	if !ok {
		t.Fatal("no answer")
	}
	took := time.Since(start)
	sp, dp, data := udpData(p)
	if !bytes.HasSuffix(data, []byte("answer")) || data[0] != testQuery[0] || p.src != s.gw || sp != 53 || dp != 5353 {
		t.Fatalf("answer %q from %v:%d to port %d", data, p.src, sp, dp)
	}
	if took < dnsTimeout-200*time.Millisecond || took > dnsTimeout+2*time.Second {
		t.Fatalf("fallback took %v", took)
	}
	if s.udp.count() != 0 || s.bytesUp.Load() != 0 {
		t.Fatal("DNS must not create flows or count bytes")
	}
	s.fails.flush(s.log, time.Now(), true)
	if !strings.Contains(s.log.Tail(), "DNS: 1 upstream query failed") {
		t.Fatal(s.log.Tail())
	}

	// The in-flight limit drops queries beyond it.
	for i := 0; i < dnsInFlight; i++ {
		s.dnsSlots <- struct{}{}
	}
	s.fromClient(t, "10.66.66.2:5354", "10.66.66.1:53", testQuery)
	if _, ok := s.toClient(t, 300*time.Millisecond); ok {
		t.Fatal("answered beyond the limit")
	}
	for i := 0; i < dnsInFlight; i++ {
		<-s.dnsSlots
	}
	s.fromClient(t, "10.66.66.2:5355", "10.66.66.1:53", testQuery)
	if _, ok := s.toClient(t, 5*time.Second); !ok {
		t.Fatal("no answer after the limit cleared")
	}
}

func TestPingGateway(t *testing.T) {
	s := testServer(t)
	req := []byte{8, 0, 0, 0, 0x12, 0x34, 0, 9, 'h', 'i'}
	binary.BigEndian.PutUint16(req[2:], csumFold(csumAdd(0, req)))
	pkt, hl, _ := ipHeader(netip.MustParseAddr("10.66.66.2"), s.gw, protoICMP, len(req))
	copy(pkt[hl:], req)
	if !s.intercept(pkt) {
		t.Fatal("IPv4 ICMP must be consumed")
	}
	p, ok := s.toClient(t, time.Second)
	if !ok || p.proto != protoICMP || p.src != s.gw || p.payload[0] != 0 || !bytes.Equal(p.payload[4:], req[4:]) {
		t.Fatalf("reply %+v", p)
	}
	// Not an echo request: dropped.
	pkt[hl] = 13
	s.intercept(pkt)
	if _, ok := s.toClient(t, 100*time.Millisecond); ok {
		t.Fatal("answered a non-echo")
	}
}
