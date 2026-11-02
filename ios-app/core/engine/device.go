package engine

import (
	"fmt"
	"net/netip"
	"os"
	"sync"

	"golang.zx2c4.com/wireguard/tun"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// netDev is the tun.Device wireguard-go reads and writes. Decrypted packets
// from clients go to the interceptor (UDP, IPv4 ping) or into a userspace
// gVisor TCP/IP stack; whatever the stack or the interceptor sends back is
// queued for wireguard-go to encrypt.
//
// The stack's NIC is promiscuous and may spoof, so it accepts connections
// to any address and answers as that address.

const (
	nicID        = 1
	linkQueueLen = 1024
	outQueueLen  = 1024
	devBatchSize = 16
)

type netDev struct {
	mtu    int
	stack  *stack.Stack
	ep     *channel.Endpoint
	notify *channel.NotificationHandle
	events chan tun.Event
	out    chan []byte // packets for wireguard-go to send to clients
	done   chan struct{}
	once   sync.Once

	// intercept sees every client packet first; true means it took it.
	// Set once before wireguard-go starts.
	intercept func(pkt []byte) bool
}

func newNetDev(mtu int, gateway netip.Addr, lowMemory bool) (*netDev, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4, icmp.NewProtocol6},
		HandleLocal:        false,
	})
	fail := func(what string, e tcpip.Error) (*netDev, error) {
		s.Close()
		return nil, fmt.Errorf("netstack: %s: %v", what, e)
	}
	sack := tcpip.TCPSACKEnabled(true)
	if e := s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack); e != nil {
		return fail("SACK", e)
	}
	def, max := 1<<20, 8<<20
	if lowMemory { // Network Extension memory cap: many flows x 8 MB won't fit
		def, max = 128<<10, 1<<20
	}
	rcv := tcpip.TCPReceiveBufferSizeRangeOption{Min: 4096, Default: def, Max: max}
	if e := s.SetTransportProtocolOption(tcp.ProtocolNumber, &rcv); e != nil {
		return fail("receive buffer", e)
	}
	snd := tcpip.TCPSendBufferSizeRangeOption{Min: 4096, Default: def, Max: max}
	if e := s.SetTransportProtocolOption(tcp.ProtocolNumber, &snd); e != nil {
		return fail("send buffer", e)
	}
	moderate := tcpip.TCPModerateReceiveBufferOption(true)
	if e := s.SetTransportProtocolOption(tcp.ProtocolNumber, &moderate); e != nil {
		return fail("receive buffer moderation", e)
	}

	d := &netDev{
		mtu:    mtu,
		stack:  s,
		ep:     channel.New(linkQueueLen, uint32(mtu), ""),
		events: make(chan tun.Event, 4),
		out:    make(chan []byte, outQueueLen),
		done:   make(chan struct{}),
	}
	d.notify = d.ep.AddNotify(d)
	if e := s.CreateNIC(nicID, d.ep); e != nil {
		d.ep.RemoveNotify(d.notify)
		d.ep.Close()
		return fail("create NIC", e)
	}
	if e := s.SetPromiscuousMode(nicID, true); e != nil {
		d.Close()
		return nil, fmt.Errorf("netstack: promiscuous mode: %v", e)
	}
	if e := s.SetSpoofing(nicID, true); e != nil {
		d.Close()
		return nil, fmt.Errorf("netstack: spoofing: %v", e)
	}
	gw := tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFrom4(gateway.As4()).WithPrefix(),
	}
	if e := s.AddProtocolAddress(nicID, gw, stack.AddressProperties{}); e != nil {
		d.Close()
		return nil, fmt.Errorf("netstack: gateway address: %v", e)
	}
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})
	d.events <- tun.EventUp
	return d, nil
}

// WriteNotify is called by the link endpoint for each packet the stack
// sends. Queuing blocks while the queue is full: back-pressure.
func (d *netDev) WriteNotify() {
	pkt := d.ep.Read()
	if pkt == nil {
		return
	}
	buf := pkt.ToBuffer()
	b := buf.Flatten()
	buf.Release()
	pkt.DecRef()
	d.send(b)
}

// sendIP queues a packet built by the engine for a client, split into
// fragments if it's bigger than the tunnel's MTU (big DNS answers, say).
func (d *netDev) sendIP(pkt []byte) {
	if pkt == nil {
		return
	}
	for _, f := range fragment(pkt, d.mtu) {
		d.send(f)
	}
}

// send queues a packet for a client, waiting for room unless closed.
func (d *netDev) send(pkt []byte) {
	if pkt == nil {
		return
	}
	select {
	case d.out <- pkt:
	case <-d.done:
	}
}

// Read hands wireguard-go packets to encrypt: it waits for one, then takes
// whatever else is ready, up to len(bufs).
func (d *netDev) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	var p []byte
	select {
	case p = <-d.out:
	case <-d.done:
		return 0, os.ErrClosed
	}
	sizes[0] = copy(bufs[0][offset:], p)
	n := 1
	for n < len(bufs) {
		select {
		case p = <-d.out:
			sizes[n] = copy(bufs[n][offset:], p)
			n++
		default:
			return n, nil
		}
	}
	return n, nil
}

// Write takes decrypted packets from clients. Problems with one packet
// never fail the call.
func (d *netDev) Write(bufs [][]byte, offset int) (int, error) {
	for _, b := range bufs {
		if len(b) <= offset {
			continue
		}
		d.inject(b[offset:])
	}
	return len(bufs), nil
}

// inject handles one packet from a WireGuard client. The packet
// is not retained after it returns.
func (d *netDev) inject(pkt []byte) {
	if len(pkt) == 0 {
		return
	}
	if d.intercept != nil && d.intercept(pkt) {
		return
	}
	var proto tcpip.NetworkProtocolNumber
	switch pkt[0] >> 4 {
	case 4:
		proto = header.IPv4ProtocolNumber
	case 6:
		proto = header.IPv6ProtocolNumber
	default:
		return
	}
	// MakeWithData copies, so the stack never holds the caller's slice.
	pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(pkt)})
	d.ep.InjectInbound(proto, pkb)
	pkb.DecRef()
}

func (d *netDev) File() *os.File           { return nil }
func (d *netDev) MTU() (int, error)        { return d.mtu, nil }
func (d *netDev) Name() (string, error)    { return "openbars", nil }
func (d *netDev) Events() <-chan tun.Event { return d.events }
func (d *netDev) BatchSize() int           { return devBatchSize }

// Close tears the stack down. Safe to call more than once.
func (d *netDev) Close() error {
	d.once.Do(func() {
		close(d.done)
		d.stack.RemoveNIC(nicID)
		d.stack.Close()
		d.ep.RemoveNotify(d.notify)
		d.ep.Close()
		close(d.events)
	})
	return nil
}
