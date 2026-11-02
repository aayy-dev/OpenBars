package engine

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

// egress opens the phone-side sockets that carry clients' traffic out.
// With bind_cellular they're pinned (IP_BOUND_IF) to the cellular interface
// iOS uses for internet. If a VPN on the phone owns the internet route,
// they follow the default route so traffic goes through the VPN.
//
// IPv4 destinations that are unreachable (IPv6-only carriers without CLAT)
// are retried through the carrier's NAT64 (RFC 7050 discovery, RFC 6052
// /96 synthesis).

const (
	dialTimeout    = 10 * time.Second
	dialKeepAlive  = 30 * time.Second
	nat64Timeout   = 3 * time.Second
	nat64Retry     = 2 * time.Minute
	nat64QueryName = "ipv4only.arpa"
)

var errNoNAT64 = errors.New("IPv4 unreachable and no NAT64 prefix known")

type egress struct {
	ctx          context.Context
	log          *Logger
	bindCellular bool

	mu    sync.RWMutex
	idx   int    // pinned interface index, 0 = default route
	name  string // pinned interface name
	vpn   bool
	known bool // refreshed at least once

	natMu      sync.Mutex
	nat64      netip.Prefix // invalid until discovered
	natChecked time.Time    // last lookup start
	natBusy    bool
	natLast    string // outcome last logged

	// lookup resolves AAAA records; replaced in tests.
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

func newEgress(ctx context.Context, bindCellular bool, log *Logger) *egress {
	e := &egress{ctx: ctx, log: log, bindCellular: bindCellular}
	e.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip6", host)
	}
	e.refresh()
	e.startNAT64Lookup()
	return e
}

// pinFor decides where egress goes for a given interface state: the
// cellular interface when asked to and no VPN is active, otherwise the
// default route (index 0).
func pinFor(ni NetInfo, bindCellular bool) (int, string) {
	// With a VPN on the phone, follow the
	// default route so traffic goes through the VPN.
	if bindCellular && !ni.VPNActive && ni.CellularIdx > 0 {
		return ni.CellularIdx, ni.CellularIf
	}
	return 0, ""
}

// refresh re-detects the interfaces and re-pins new sockets. Existing
// sockets keep their binding.
func (e *egress) refresh() NetInfo {
	ni := DetectNetInfo()
	idx, name := pinFor(ni, e.bindCellular)
	e.mu.Lock()
	changed := !e.known || idx != e.idx || name != e.name || ni.VPNActive != e.vpn
	hadVPN := e.known && e.vpn
	e.idx, e.name, e.vpn, e.known = idx, name, ni.VPNActive, true
	e.mu.Unlock()
	if !changed {
		return ni
	}
	switch {
	case ni.VPNActive:
		e.log.Infof("egress", "A VPN on the phone owns the default route (%s); following the default route through it", ni.VPNIf)
	case idx > 0:
		if hadVPN {
			e.log.Infof("egress", "The phone's VPN is gone")
		}
		e.log.Infof("egress", "Sending traffic out %s (%s); iOS default route: %s", name, ni.CellularIP, orNone(ni.DefaultIf))
	default:
		if hadVPN {
			e.log.Infof("egress", "The phone's VPN is gone")
		}
		if e.bindCellular {
			e.log.WarnHint("egress", "Check that Cellular Data is on, or turn off the cellular-only setting in Open Bars.",
				"No cellular interface found; using the phone's default route (%s)", orNone(ni.DefaultIf))
		} else {
			e.log.Infof("egress", "Using the phone's default route (%s); cellular-only is off", orNone(ni.DefaultIf))
		}
	}
	return ni
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func (e *egress) index() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.idx
}

// ifName is the pinned interface's name, or "default route".
func (e *egress) ifName() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.idx == 0 {
		return "default route"
	}
	return e.name
}

// isUnreachable reports errors that mean "no route this way" (as opposed
// to the destination refusing or timing out).
func isUnreachable(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EAFNOSUPPORT)
}

func (e *egress) dialer(idx int) *net.Dialer {
	return &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: dialKeepAlive,
		Control: func(network, address string, c syscall.RawConn) error {
			return bindSocket(network, c, idx)
		},
	}
}

// DialTCP connects to dst from the pinned interface, falling back to NAT64
// for unreachable IPv4 and then to the default route.
func (e *egress) DialTCP(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
	idx := e.index()
	c, err := e.dialTCPOn(ctx, dst, idx)
	if err != nil && isUnreachable(err) && idx != 0 {
		c, err = e.dialTCPOn(ctx, dst, 0)
	}
	return c, err
}

func (e *egress) dialTCPOn(ctx context.Context, dst netip.AddrPort, idx int) (net.Conn, error) {
	network := "tcp4"
	if dst.Addr().Is6() {
		network = "tcp6"
	}
	d := e.dialer(idx)
	c, err := d.DialContext(ctx, network, dst.String())
	if err != nil && isUnreachable(err) && dst.Addr().Is4() {
		if v6, ok := e.synth(dst.Addr()); ok {
			c, err = d.DialContext(ctx, "tcp6", netip.AddrPortFrom(v6, dst.Port()).String())
		}
	}
	return c, err
}

// ListenUDP opens an unconnected UDP socket on an ephemeral port, pinned
// like DialTCP; if pinning fails it falls back to an unpinned socket.
func (e *egress) ListenUDP(v6 bool) (*net.UDPConn, error) {
	network, addr := "udp4", "0.0.0.0:0"
	if v6 {
		network, addr = "udp6", "[::]:0"
	}
	idx := e.index()
	pc, err := e.listenOn(network, addr, idx)
	if err != nil && idx != 0 {
		pc, err = e.listenOn(network, addr, 0)
	}
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}

func (e *egress) listenOn(network, addr string, idx int) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		return bindSocket(network, c, idx)
	}}
	return lc.ListenPacket(context.Background(), network, addr)
}

// synth maps an IPv4 address into the NAT64 prefix. It never waits: with
// no prefix known it may start a lookup in the background and reports
// false, so a later packet benefits.
func (e *egress) synth(v4 netip.Addr) (netip.Addr, bool) {
	e.natMu.Lock()
	p := e.nat64
	stale := !p.IsValid() && time.Since(e.natChecked) >= nat64Retry
	e.natMu.Unlock()
	if !p.IsValid() {
		if stale {
			e.startNAT64Lookup()
		}
		return netip.Addr{}, false
	}
	return synthNAT64(p, v4), true
}

// unsynth recovers the IPv4 address embedded in a NAT64 address.
func (e *egress) unsynth(v6 netip.Addr) (netip.Addr, bool) {
	e.natMu.Lock()
	p := e.nat64
	e.natMu.Unlock()
	return unsynthNAT64(p, v6)
}

func synthNAT64(p netip.Prefix, v4 netip.Addr) netip.Addr {
	b := p.Addr().As16()
	a := v4.As4()
	copy(b[12:], a[:])
	return netip.AddrFrom16(b)
}

func unsynthNAT64(p netip.Prefix, v6 netip.Addr) (netip.Addr, bool) {
	if !p.IsValid() || !v6.Is6() || v6.Is4In6() || !p.Contains(v6) {
		return netip.Addr{}, false
	}
	b := v6.As16()
	return netip.AddrFrom4([4]byte(b[12:16])), true
}

// startNAT64Lookup discovers the NAT64 prefix in the background, unless a
// lookup is already running.
func (e *egress) startNAT64Lookup() {
	e.natMu.Lock()
	if e.natBusy {
		e.natMu.Unlock()
		return
	}
	e.natBusy = true
	e.natChecked = time.Now()
	e.natMu.Unlock()
	go e.lookupNAT64()
}

func (e *egress) lookupNAT64() {
	ctx, cancel := context.WithTimeout(e.ctx, nat64Timeout)
	addrs, err := e.lookup(ctx, nat64QueryName)
	cancel()

	var prefix netip.Prefix
	for _, a := range addrs {
		if p, ok := nat64PrefixOf(a); ok {
			prefix = p
			break
		}
	}
	e.natMu.Lock()
	e.natBusy = false
	if prefix.IsValid() {
		e.nat64 = prefix
	}
	var outcome string
	switch {
	case prefix.IsValid():
		outcome = "found " + prefix.String()
	case err == nil || isNotFound(err):
		outcome = "none"
	case e.ctx.Err() != nil:
		outcome = e.natLast // stopping: say nothing
	default:
		outcome = "error"
	}
	report := outcome != e.natLast
	e.natLast = outcome
	e.natMu.Unlock()

	if !report {
		return
	}
	switch outcome {
	case "none":
		e.log.Infof("nat64", "No NAT64 on this network (fine when the carrier gives IPv4)")
	case "error":
		e.log.WarnHint("nat64", "Only matters on IPv6-only carriers; Open Bars retries in 2 minutes.",
			"NAT64 discovery failed: %v", err)
	default:
		e.log.Infof("nat64", "NAT64 prefix %s found; IPv4 destinations will be reached over IPv6 if needed", prefix)
	}
}

// nat64PrefixOf returns the /96 prefix of a DNS64 answer for
// ipv4only.arpa (whose IPv4 addresses are 192.0.0.170 and .171).
func nat64PrefixOf(a netip.Addr) (netip.Prefix, bool) {
	if !a.Is6() || a.Is4In6() {
		return netip.Prefix{}, false
	}
	b := a.As16()
	if b[12] != 192 || b[13] != 0 || b[14] != 0 || (b[15] != 170 && b[15] != 171) {
		return netip.Prefix{}, false
	}
	p, err := a.Prefix(96)
	return p, err == nil
}

func isNotFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}
