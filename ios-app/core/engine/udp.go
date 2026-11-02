package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// UDP is NATed outside the userspace stack. Each client source address:port
// is a flow that owns up to two unconnected phone-side sockets (IPv4 and
// IPv6). Any remote host may answer through the same mapping
// (endpoint-independent mapping and filtering, i.e. "full cone"), which is
// what consoles and PC games want for a good NAT type.

const (
	udpIdle           = 3 * time.Minute // games leave mappings quiet for a while
	maxUDPFlows       = 4096
	maxUDPFlowsLowMem = 512 // in the Network Extension, under iOS's memory cap
	// udpReadBuf is per socket while it waits: big enough for large DNS
	// answers and jumbo datagrams, small enough for many flows.
	udpReadBuf   = 8 << 10
	dnsTimeout   = 2500 * time.Millisecond
	dnsInFlight  = 256
	dnsGatewayPt = 53
)

var errFlowClosed = errors.New("flow closed")

type udpNAT struct {
	s     *Server
	mu    sync.Mutex
	flows map[netip.AddrPort]*udpFlow
}

type udpFlow struct {
	nat    *udpNAT
	client netip.AddrPort
	born   time.Time
	first  netip.AddrPort // first destination, for the debug log
	last   atomic.Int64   // unix nanoseconds of the last send or receive
	up     atomic.Int64
	down   atomic.Int64

	mu     sync.Mutex
	c4, c6 *net.UDPConn
	closed bool
}

func newUDPNAT(s *Server) *udpNAT {
	return &udpNAT{s: s, flows: map[netip.AddrPort]*udpFlow{}}
}

func (n *udpNAT) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.flows)
}

// handle takes one UDP datagram from a client (p.payload is the UDP header
// and data). It runs on wireguard-go's receive path and must not block.
func (n *udpNAT) handle(p *ipPacket) {
	b := p.payload
	if len(b) < 8 {
		return
	}
	length := int(binary.BigEndian.Uint16(b[4:6]))
	if length < 8 || length > len(b) {
		length = len(b)
	}
	data := b[8:length]
	client := netip.AddrPortFrom(p.src, binary.BigEndian.Uint16(b[0:2]))
	dst := netip.AddrPortFrom(p.dst, binary.BigEndian.Uint16(b[2:4]))
	s := n.s

	if p.dst == s.gw {
		if dst.Port() == dnsGatewayPt {
			s.dnsQuery(bytes.Clone(data), client, dst)
		}
		return
	}
	if dropDestination(p.dst) {
		return
	}
	f := n.flow(client, dst)
	if f == nil {
		return
	}
	f.send(dst, data)
}

// flow finds or creates the flow for a client endpoint; nil when full.
func (n *udpNAT) flow(client, dst netip.AddrPort) *udpFlow {
	n.mu.Lock()
	f, ok := n.flows[client]
	if !ok {
		limit := maxUDPFlows
		if n.s.cfg.LowMemory {
			limit = maxUDPFlowsLowMem
		}
		if len(n.flows) >= limit {
			n.mu.Unlock()
			n.s.fails.add(n.s.log, "udp", reasonTooMany, dst.String(), nil)
			return nil
		}
		f = &udpFlow{nat: n, client: client, born: time.Now(), first: dst}
		f.last.Store(time.Now().UnixNano())
		n.flows[client] = f
	}
	n.mu.Unlock()
	if !ok && n.s.log.Detailed() {
		n.s.log.Debugf("udp", "Flow %s opened (first to %s)", client, dst)
	}
	return f
}

func (f *udpFlow) touch() { f.last.Store(time.Now().UnixNano()) }

// socket returns the flow's IPv4 or IPv6 socket, opening it (and its
// reader) on first use.
func (f *udpFlow) socket(v6 bool) (*net.UDPConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errFlowClosed
	}
	if v6 && f.c6 != nil {
		return f.c6, nil
	}
	if !v6 && f.c4 != nil {
		return f.c4, nil
	}
	c, err := f.nat.s.eg.ListenUDP(v6)
	if err != nil {
		return nil, err
	}
	if v6 {
		f.c6 = c
	} else {
		f.c4 = c
	}
	go f.read(c)
	return c, nil
}

func (f *udpFlow) send(dst netip.AddrPort, data []byte) {
	s := f.nat.s
	f.touch()
	if dst.Addr().Is4() {
		c, err := f.socket(false)
		if err == nil {
			if _, err = c.WriteToUDPAddrPort(data, dst); err == nil {
				f.sent(len(data))
				return
			}
		}
		if errors.Is(err, errFlowClosed) {
			return
		}
		if c != nil && !isUnreachable(err) {
			s.fails.add(s.log, "udp", failReason(err), dst.String(), err)
			return
		}
		// Unreachable over IPv4 (or no IPv4 socket): try NAT64.
		v6, ok := s.eg.synth(dst.Addr())
		if !ok {
			s.fails.add(s.log, "udp", failReason(err), dst.String(), err)
			return
		}
		dst = netip.AddrPortFrom(v6, dst.Port())
	}
	c, err := f.socket(true)
	if err == nil {
		_, err = c.WriteToUDPAddrPort(data, dst)
	}
	if err != nil {
		if !errors.Is(err, errFlowClosed) {
			s.fails.add(s.log, "udp", failReason(err), dst.String(), err)
		}
		return
	}
	f.sent(len(data))
}

func (f *udpFlow) sent(n int) {
	f.up.Add(int64(n))
	f.nat.s.bytesUp.Add(int64(n))
}

// read forwards datagrams from one socket to the client until the socket
// is closed.
func (f *udpFlow) read(c *net.UDPConn) {
	s := f.nat.s
	buf := make([]byte, udpReadBuf)
	client4 := f.client.Addr().Is4()
	for {
		n, from, err := c.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		src := netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		if src.Addr().Is6() && client4 {
			v4, ok := s.eg.unsynth(src.Addr())
			if !ok {
				continue
			}
			src = netip.AddrPortFrom(v4, src.Port())
		} else if src.Addr().Is4() != client4 {
			continue
		}
		f.touch()
		f.down.Add(int64(n))
		s.bytesDown.Add(int64(n))
		s.dev.sendIP(buildUDP(src, f.client, buf[:n]))
	}
}

func (f *udpFlow) close() {
	f.mu.Lock()
	f.closed = true
	c4, c6 := f.c4, f.c6
	f.c4, f.c6 = nil, nil
	f.mu.Unlock()
	if c4 != nil {
		c4.Close()
	}
	if c6 != nil {
		c6.Close()
	}
}

// reap closes flows idle for at least maxIdle (all of them if maxIdle is
// 0). Sockets are closed outside the map lock.
func (n *udpNAT) reap(maxIdle time.Duration) {
	cutoff := time.Now().Add(-maxIdle).UnixNano()
	var dead []*udpFlow
	n.mu.Lock()
	for k, f := range n.flows {
		if maxIdle == 0 || f.last.Load() <= cutoff {
			dead = append(dead, f)
			delete(n.flows, k)
		}
	}
	n.mu.Unlock()
	detailed := n.s.log.Detailed()
	for _, f := range dead {
		f.close()
		if detailed {
			n.s.log.Debugf("udp", "Flow %s closed (first to %s): ↑%s ↓%s in %s",
				f.client, f.first, fmtBytes(uint64(f.up.Load())), fmtBytes(uint64(f.down.Load())), time.Since(f.born).Round(time.Second))
		}
	}
}

// dnsQuery answers a client's query to the gateway from the upstream
// resolvers, in order. Queries beyond the in-flight limit are dropped (the
// client retries); if every upstream fails, nothing is sent.
func (s *Server) dnsQuery(q []byte, client, gw netip.AddrPort) {
	if len(s.dnsUp) == 0 {
		return
	}
	select {
	case s.dnsSlots <- struct{}{}:
	default:
		s.fails.add(s.log, "dns", reasonBusy, s.dnsUp[0].String(), nil)
		return
	}
	go func() {
		defer func() { <-s.dnsSlots }()
		for _, up := range s.dnsUp {
			ans, err := s.dnsExchange(up, q)
			if err == nil {
				s.dev.sendIP(buildUDP(gw, client, ans))
				return
			}
			if s.ctx.Err() != nil {
				return
			}
			s.fails.add(s.log, "dns", failReason(err), up.String(), err)
		}
	}()
}

// dnsExchange sends one query to one upstream over a fresh egress socket
// and returns the first datagram that comes back from exactly that address
// and port.
func (s *Server) dnsExchange(up netip.AddrPort, q []byte) ([]byte, error) {
	deadline := time.Now().Add(dnsTimeout)
	target := up
	c, err := s.eg.ListenUDP(up.Addr().Is6())
	if err == nil {
		c.SetDeadline(deadline)
		_, err = c.WriteToUDPAddrPort(q, target)
		if err != nil {
			c.Close()
			c = nil
		}
	}
	if err != nil && isUnreachable(err) && up.Addr().Is4() {
		v6, ok := s.eg.synth(up.Addr())
		if !ok {
			return nil, errNoNAT64
		}
		target = netip.AddrPortFrom(v6, up.Port())
		c, err = s.eg.ListenUDP(true)
		if err == nil {
			c.SetDeadline(deadline)
			if _, err = c.WriteToUDPAddrPort(q, target); err != nil {
				c.Close()
			}
		}
	}
	if err != nil {
		return nil, err
	}
	defer c.Close()
	buf := make([]byte, udpReadBuf)
	for {
		n, from, err := c.ReadFromUDPAddrPort(buf)
		if err != nil {
			return nil, err
		}
		if from.Addr().Unmap() == target.Addr().Unmap() && from.Port() == target.Port() && dnsAnswers(q, buf[:n]) {
			return bytes.Clone(buf[:n]), nil
		}
		// Anything else (a stray or forged packet) is ignored; keep waiting.
	}
}

// dnsAnswers reports whether resp is the answer to query q: same
// transaction ID, the response bit set, and the same question (names
// compared case-insensitively, as resolvers may change case).
func dnsAnswers(q, resp []byte) bool {
	if len(q) < 12 || len(resp) < 12 || q[0] != resp[0] || q[1] != resp[1] || resp[2]&0x80 == 0 {
		return false
	}
	end := dnsQuestionEnd(q)
	if end < 0 {
		return true // can't read the question; the ID check is all we have
	}
	if len(resp) < end || binary.BigEndian.Uint16(resp[4:6]) != binary.BigEndian.Uint16(q[4:6]) {
		return false
	}
	return bytes.EqualFold(q[12:end], resp[12:end])
}

// dnsQuestionEnd returns where the question section of a message ends, or
// -1 if it can't be read (no question, compression, truncation).
func dnsQuestionEnd(m []byte) int {
	if binary.BigEndian.Uint16(m[4:6]) != 1 {
		return -1
	}
	i := 12
	for i < len(m) {
		l := int(m[i])
		switch {
		case l == 0:
			if i+5 > len(m) {
				return -1
			}
			return i + 5 // root label, type, class
		case l&0xc0 != 0:
			return -1 // compression pointers don't occur in queries
		}
		i += 1 + l
	}
	return -1
}

// parseDNS reads the upstream resolver list: "ip", "ip:port" or
// "[v6]:port"; anything else is ignored.
func parseDNS(list []string) []netip.AddrPort {
	var out []netip.AddrPort
	for _, s := range list {
		s = strings.TrimSpace(s)
		if ap, err := netip.ParseAddrPort(s); err == nil {
			out = append(out, netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()))
		} else if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, netip.AddrPortFrom(a.Unmap(), 53))
		}
	}
	return out
}
