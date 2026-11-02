package engine

import (
	"encoding/binary"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// Pings from devices. Echo requests to the phone itself are answered at
// once; others are sent for real from unprivileged ICMP datagram sockets
// (iOS allows them without root), pinned to the same interface as all other
// egress, so round-trip times reflect the phone's real path. IPv4 pings
// fall back to NAT64 on IPv6-only networks, and IPv6 pings are forwarded
// too (otherwise the userspace stack would answer them itself).

const (
	pingIdle          = time.Minute
	maxPingFlows      = 256
	pingReadBuf       = 2048
	icmpv6EchoRequest = 128
	icmpv6EchoReply   = 129
)

type pingKey struct {
	client, dst netip.Addr // as the device sees them
	id          uint16     // the device's echo identifier
}

type pingFlow struct {
	key  pingKey
	myID uint16     // identifier on the wire
	wire netip.Addr // where requests really go: dst, or its NAT64 address
	conn net.PacketConn
	last atomic.Int64 // unix nanoseconds
}

type pinger struct {
	s      *Server
	mu     sync.Mutex
	flows  map[pingKey]*pingFlow
	nextID atomic.Uint32
	warned [2]atomic.Bool // ICMP / ICMPv6 sockets unavailable (logged once)
}

func newPinger(s *Server) *pinger {
	return &pinger{s: s, flows: map[pingKey]*pingFlow{}}
}

// handle takes one IPv4 ICMP packet from a device; only unfragmented echo
// requests are used.
func (pg *pinger) handle(p *ipPacket) {
	m := p.payload
	if p.frag || len(m) < 8 || m[0] != 8 || m[1] != 0 {
		return
	}
	id, seq, data := binary.BigEndian.Uint16(m[4:6]), binary.BigEndian.Uint16(m[6:8]), m[8:]
	s := pg.s
	if p.dst == s.gw {
		s.dev.sendIP(buildEchoReply(s.gw, p.src, id, seq, data))
		return
	}
	if dropDestination(p.dst) {
		return
	}
	pg.forward(pingKey{p.src, p.dst, id}, seq, data)
}

// handle6 takes one ICMPv6 echo request from a device.
func (pg *pinger) handle6(p *ipPacket) {
	m := p.payload
	if len(m) < 8 || m[0] != icmpv6EchoRequest || m[1] != 0 {
		return
	}
	id, seq, data := binary.BigEndian.Uint16(m[4:6]), binary.BigEndian.Uint16(m[6:8]), m[8:]
	s := pg.s
	if gw6, ok := peerAddr6(s.gw); ok && p.dst == gw6 {
		s.dev.sendIP(buildEchoReply6(gw6, p.src, id, seq, data))
		return
	}
	if dropDestination(p.dst) {
		return
	}
	pg.forward(pingKey{p.src, p.dst, id}, seq, data)
}

// forward sends an echo request for a device, over NAT64 when an IPv4
// destination isn't reachable directly.
func (pg *pinger) forward(k pingKey, seq uint16, data []byte) {
	s := pg.s
	f := pg.flow(k, k.dst)
	if f == nil {
		return
	}
	f.last.Store(time.Now().UnixNano())
	err := f.send(seq, data)
	if err != nil && k.dst.Is4() && f.wire.Is4() && isUnreachable(err) {
		if v6, ok := s.eg.synth(k.dst); ok {
			pg.drop(f)
			if f = pg.flow(k, v6); f == nil {
				return
			}
			err = f.send(seq, data)
		}
	}
	if err != nil {
		s.fails.add(s.log, "icmp", failReason(err), k.dst.String(), err)
	}
}

func (f *pingFlow) send(seq uint16, data []byte) error {
	var typ icmp.Type = ipv4.ICMPTypeEcho
	if f.wire.Is6() {
		typ = ipv6.ICMPTypeEchoRequest // the kernel fills in the checksum
	}
	b, err := (&icmp.Message{Type: typ, Body: &icmp.Echo{ID: int(f.myID), Seq: int(seq), Data: data}}).Marshal(nil)
	if err != nil {
		return err
	}
	_, err = f.conn.WriteTo(b, &net.UDPAddr{IP: f.wire.AsSlice()})
	return err
}

// flow finds or opens the flow for k, sending to wire.
func (pg *pinger) flow(k pingKey, wire netip.Addr) *pingFlow {
	pg.mu.Lock()
	defer pg.mu.Unlock()
	if f, ok := pg.flows[k]; ok {
		return f
	}
	if len(pg.flows) >= maxPingFlows {
		return nil
	}
	v6 := wire.Is6()
	c, err := listenICMP(v6, pg.s.eg.index())
	if err != nil && pg.s.eg.index() > 0 {
		c, err = listenICMP(v6, 0) // can't pin: use the default route
	}
	if err != nil {
		fam := 0
		if v6 {
			fam = 1
		}
		if !pg.warned[fam].Swap(true) {
			pg.s.log.Warnf("icmp", "Can't open %s sockets (%v); pings from devices will time out", map[bool]string{false: "ICMP", true: "ICMPv6"}[v6], err)
		}
		return nil
	}
	f := &pingFlow{key: k, myID: pg.newID(), wire: wire, conn: c}
	f.last.Store(time.Now().UnixNano())
	pg.flows[k] = f
	go pg.read(f)
	return f
}

func (pg *pinger) drop(f *pingFlow) {
	pg.mu.Lock()
	if pg.flows[f.key] == f {
		delete(pg.flows, f.key)
	}
	pg.mu.Unlock()
	f.conn.Close()
}

// newID cycles through 1...65534.
func (pg *pinger) newID() uint16 {
	return uint16(pg.nextID.Add(1)%65534) + 1
}

// read forwards echo replies from the flow's destination to the device,
// with the device's own identifier and addresses.
func (pg *pinger) read(f *pingFlow) {
	s := pg.s
	proto, reply := 1, icmp.Type(ipv4.ICMPTypeEchoReply)
	if f.wire.Is6() {
		proto, reply = 58, ipv6.ICMPTypeEchoReply
	}
	buf := make([]byte, pingReadBuf)
	for {
		n, peer, err := f.conn.ReadFrom(buf)
		if err != nil {
			return // closed by reap or Stop
		}
		var from netip.Addr
		switch a := peer.(type) {
		case *net.UDPAddr:
			from, _ = netip.AddrFromSlice(a.IP)
		case *net.IPAddr:
			from, _ = netip.AddrFromSlice(a.IP)
		}
		if from.Unmap() != f.wire.Unmap() {
			continue
		}
		m, err := icmp.ParseMessage(proto, buf[:n])
		if err != nil || m.Type != reply {
			continue
		}
		echo, ok := m.Body.(*icmp.Echo)
		if !ok {
			continue
		}
		// Darwin keeps our identifier and may hand us other sockets'
		// replies; Linux rewrites it to the socket's port.
		if runtime.GOOS != "linux" && uint16(echo.ID) != f.myID {
			continue
		}
		f.last.Store(time.Now().UnixNano())
		if f.key.client.Is4() {
			s.dev.sendIP(buildEchoReply(f.key.dst, f.key.client, f.key.id, uint16(echo.Seq), echo.Data))
		} else {
			s.dev.sendIP(buildEchoReply6(f.key.dst, f.key.client, f.key.id, uint16(echo.Seq), echo.Data))
		}
	}
}

// reap closes flows idle for at least maxIdle (all if maxIdle is 0).
func (pg *pinger) reap(maxIdle time.Duration) {
	cutoff := time.Now().Add(-maxIdle).UnixNano()
	var dead []*pingFlow
	pg.mu.Lock()
	for k, f := range pg.flows {
		if maxIdle == 0 || f.last.Load() <= cutoff {
			dead = append(dead, f)
			delete(pg.flows, k)
		}
	}
	pg.mu.Unlock()
	for _, f := range dead {
		f.conn.Close()
	}
}
