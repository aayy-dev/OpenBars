package engine

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// Applying limits to packets. Every packet from a device (netDev.inject) and
// to a device (netDev.send) passes allowFrom/allowTo, which look up the
// device's tunnel address in a map rebuilt whenever a decision changes:
//   - blocked (paused, or limited with the block action): only the
//     gateway's status page (TCP 80) and pings to the gateway get through,
//     so the device can still see why and ask for more. DNS is dropped too,
//     so it can't be used to carry data.
//   - slowed: a token bucket per direction holds the device to the speed.
//     TCP is paced where the engine copies it (pacer): its data waits for
//     the bucket instead of being dropped, which TCP would answer by
//     stalling. Other packets (UDP, so QUIC too) over the speed are dropped,
//     and they back off to it. The status page isn't slowed.
// When a device becomes blocked, its open connections are closed, so a
// download in progress stops at once instead of draining buffers; the
// close (a TCP reset or FIN without data) is let through so the device's
// apps see it right away.
//
// Limits are decided once a second, and a fast connection moves many
// megabytes in a second. So a device that would be blocked at its limit
// also gets an allowance: what's left before its nearest limit, counted
// down by every packet. When it runs out, its packets stop at once; the
// next decision makes it officially limited.

type enforcement struct {
	block    bool
	kbps     int
	up, down *tokenBucket
	// budget is the bytes left before the device's nearest limit (nil =
	// not counted). Reset at every decision.
	budget *atomic.Int64
}

// spend takes n bytes from the allowance; false once it's used up.
func (e *enforcement) spend(n int) bool {
	return e.budget == nil || e.budget.Add(-int64(n)) >= 0
}

type enforcer struct {
	m atomic.Pointer[map[netip.Addr]*enforcement]
}

// lookup is the enforcement for a tunnel address, or nil.
func (e *enforcer) lookup(a netip.Addr) *enforcement {
	m := e.m.Load()
	if m == nil {
		return nil
	}
	return (*m)[a]
}

// ipEnds reads a packet's source, destination, protocol and destination
// port (0 if not TCP/UDP or not in this fragment).
func ipEnds(b []byte) (src, dst netip.Addr, proto uint8, dport uint16, ok bool) {
	if len(b) < 1 {
		return
	}
	switch b[0] >> 4 {
	case 4:
		if len(b) < 20 {
			return
		}
		src = netip.AddrFrom4([4]byte(b[12:16]))
		dst = netip.AddrFrom4([4]byte(b[16:20]))
		proto = b[9]
		ihl := int(b[0]&0x0f) * 4
		fragOff := (int(b[6])&0x1f)<<8 | int(b[7])
		if fragOff == 0 && len(b) >= ihl+4 && (proto == protoTCP || proto == protoUDP) {
			dport = uint16(b[ihl+2])<<8 | uint16(b[ihl+3])
		}
		return src, dst, proto, dport, true
	case 6:
		if len(b) < 40 {
			return
		}
		src = netip.AddrFrom16([16]byte(b[8:24]))
		dst = netip.AddrFrom16([16]byte(b[24:40]))
		proto = b[6] // extension headers aren't walked: their ports read as 0
		if len(b) >= 44 && (proto == protoTCP || proto == protoUDP) {
			dport = uint16(b[42])<<8 | uint16(b[43])
		}
		return src, dst, proto, dport, true
	}
	return
}

// allowFrom decides a packet from a device.
func (s *Server) allowFrom(pkt []byte) bool {
	src, dst, proto, dport, ok := ipEnds(pkt)
	if !ok {
		return true
	}
	e := s.enf.lookup(src)
	if e == nil {
		return true
	}
	if dst == s.gw {
		if proto == protoTCP && dport == statusPagePort {
			return true
		}
		if proto == protoICMP {
			return true
		}
		if e.block {
			return false // DNS too: it could carry data
		}
	}
	if e.block || !e.spend(len(pkt)) {
		return false
	}
	return e.up == nil || proto == protoTCP || e.up.take(len(pkt)) // TCP: paced by pacer
}

// allowTo decides a packet to a device.
func (s *Server) allowTo(pkt []byte) bool {
	src, dst, proto, _, ok := ipEnds(pkt)
	if !ok {
		return true
	}
	e := s.enf.lookup(dst)
	if e == nil {
		return true
	}
	if src == s.gw {
		return true // the status page and pings; DNS answers only follow allowed queries
	}
	if e.block || !e.spend(len(pkt)) {
		return tcpCloseOnly(pkt) // so the device sees its connections end
	}
	return e.down == nil || proto == protoTCP || e.down.take(len(pkt)) // TCP: paced by pacer
}

// paceChunk is the most a paced TCP write sends at once.
const paceChunk = 8 << 10

// pacer holds a TCP connection's data to its device's speed while the
// device is slowed (up: from the device). It looks the device up on every
// write, so slowing and lifting apply to open connections too.
func (s *Server) pacer(client netip.Addr, up bool) func(n int) {
	return func(n int) {
		e := s.enf.lookup(client)
		if e == nil || e.kbps == 0 {
			return
		}
		b := e.down
		if up {
			b = e.up
		}
		if b != nil {
			b.wait(n, s.ctx.Done())
		}
	}
}

// tcpCloseOnly reports a TCP reset or FIN that carries no data.
func tcpCloseOnly(b []byte) bool {
	var tcp []byte
	switch b[0] >> 4 {
	case 4:
		ihl := int(b[0]&0x0f) * 4
		total := int(b[2])<<8 | int(b[3])
		if b[9] != protoTCP || total < ihl+20 || total > len(b) {
			return false
		}
		tcp = b[ihl:total]
	case 6:
		if b[6] != protoTCP || len(b) < 60 {
			return false
		}
		tcp = b[40:]
	default:
		return false
	}
	off := int(tcp[12]>>4) * 4
	const fin, rst = 0x01, 0x04
	return off >= 20 && len(tcp) == off && tcp[13]&(fin|rst) != 0
}

// applyLimits rebuilds the enforcement map from the latest decisions and
// closes the connections of devices that just became blocked.
func (s *Server) applyLimits(statuses map[string]LimitStatus, peers []PeerConfig, ls LimitSettings) {
	old := s.enf.m.Load()
	next := map[netip.Addr]*enforcement{}
	var newlyBlocked []netip.Addr
	for _, p := range peers {
		st, ok := statuses[p.PublicKey]
		if !ok {
			continue
		}
		var e *enforcement
		budget := int64(-1)
		switch {
		case st.State == StateLimited || st.State == StatePaused:
			switch st.Action {
			case ActionBlock:
				e = &enforcement{block: true}
			case ActionSlow:
				kbps := ls.slowKbps()
				if p.Limit != nil && p.Limit.SlowKbps > 0 && !st.Cap {
					kbps = p.Limit.SlowKbps
				}
				e = &enforcement{kbps: kbps}
			}
		case deviceAction(p, ls) == ActionBlock:
			// Not limited yet: count down what's left (see above).
			if budget = allowance(st); budget >= 0 {
				e = &enforcement{}
			}
		}
		if e == nil {
			continue // no limit, or notify only
		}
		addr, err := netip.ParseAddr(p.Address)
		if err != nil {
			continue
		}
		addrs := []netip.Addr{addr.Unmap()}
		if v6, ok := peerAddr6(addr); ok {
			addrs = append(addrs, v6)
		}
		var shared *atomic.Int64 // one allowance for the device's IPv4 and IPv6
		for _, a := range addrs {
			prev := (*enforcement)(nil)
			if old != nil {
				prev = (*old)[a]
			}
			ea := *e
			if !ea.block && ea.kbps > 0 {
				if prev != nil && !prev.block && prev.kbps == ea.kbps {
					ea.up, ea.down = prev.up, prev.down // keep their state
				} else {
					ea.up, ea.down = newTokenBucket(ea.kbps), newTokenBucket(ea.kbps)
				}
			}
			if budget >= 0 {
				if shared == nil {
					if prev != nil && prev.budget != nil {
						shared = prev.budget
					} else {
						shared = new(atomic.Int64)
					}
					shared.Store(budget)
				}
				ea.budget = shared
			}
			next[a] = &ea
			if ea.block && (prev == nil || !prev.block) {
				newlyBlocked = append(newlyBlocked, a)
			}
		}
	}
	if old == nil || !sameEnforcement(*old, next) {
		s.enf.m.Store(&next)
	}
	for _, a := range newlyBlocked {
		s.conns.closeClient(a)
		s.udp.closeClient(a)
	}
}

func sameEnforcement(a, b map[netip.Addr]*enforcement) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		y, ok := b[k]
		// Same allowance counter: its new value is already stored.
		if !ok || x.block != y.block || x.kbps != y.kbps || x.budget != y.budget || x.up != y.up {
			return false
		}
	}
	return true
}

// deviceAction is what happens when the device reaches its own limits.
func deviceAction(p PeerConfig, ls LimitSettings) string {
	if p.Limit != nil && validAction(p.Limit.Action) {
		return p.Limit.Action
	}
	return ls.action()
}

// allowance is the bytes a device can still move before its nearest limit
// (-1 = not limited: no rules, or a grant of time).
func allowance(st LimitStatus) int64 {
	if st.State == StateExtended {
		var left int64
		for _, g := range st.Grants {
			if g.Active && g.Until > 0 {
				return -1
			}
			if g.Active {
				left += g.Left
			}
		}
		return left
	}
	least := int64(-1)
	for _, r := range st.Rules {
		left := r.Bytes - r.Used
		if left < 0 {
			left = 0
		}
		if least < 0 || left < least {
			least = left
		}
	}
	return least
}

// tokenBucket allows kbps on average with a short burst.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64 // bytes per second
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(kbps int) *tokenBucket {
	rate := float64(kbps) * 1000 / 8
	burst := rate / 2 // half a second
	if burst < 32<<10 {
		burst = 32 << 10
	}
	return &tokenBucket{rate: rate, burst: burst, tokens: burst, last: time.Now()}
}

// wait takes n bytes, sleeping until the bucket has them (or done closes).
func (b *tokenBucket) wait(n int, done <-chan struct{}) {
	b.mu.Lock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	b.last = now
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.tokens -= float64(n) // may go below zero: the debt is the wait
	debt := -b.tokens
	b.mu.Unlock()
	if debt <= 0 {
		return
	}
	t := time.NewTimer(time.Duration(debt / b.rate * float64(time.Second)))
	defer t.Stop()
	select {
	case <-t.C:
	case <-done:
	}
}

func (b *tokenBucket) take(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	b.last = now
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

// connRegistry knows each device's open TCP connections, so they can be
// closed when it's blocked.
type connRegistry struct {
	mu    sync.Mutex
	next  int64
	conns map[netip.Addr]map[int64]func()
}

func (r *connRegistry) add(client netip.Addr, closeFn func()) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conns == nil {
		r.conns = map[netip.Addr]map[int64]func(){}
	}
	r.next++
	id := r.next
	if r.conns[client] == nil {
		r.conns[client] = map[int64]func(){}
	}
	r.conns[client][id] = closeFn
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.conns[client], id)
		if len(r.conns[client]) == 0 {
			delete(r.conns, client)
		}
	}
}

func (r *connRegistry) closeClient(client netip.Addr) {
	r.mu.Lock()
	fns := make([]func(), 0, len(r.conns[client]))
	for _, f := range r.conns[client] {
		fns = append(fns, f)
	}
	r.mu.Unlock()
	for _, f := range fns {
		f()
	}
}

// closeClient closes every UDP flow of a device.
func (n *udpNAT) closeClient(client netip.Addr) {
	var dead []*udpFlow
	n.mu.Lock()
	for k, f := range n.flows {
		if k.Addr() == client {
			dead = append(dead, f)
			delete(n.flows, k)
		}
	}
	n.mu.Unlock()
	for _, f := range dead {
		f.close()
	}
}
