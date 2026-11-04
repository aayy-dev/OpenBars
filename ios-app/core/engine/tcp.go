package engine

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// TCP from clients ends in the userspace stack, which hands each new SYN to
// handleTCP. The upstream connection is dialed first, and the client's
// handshake is completed only if that worked: unreachable or refusing hosts
// get a quick RST instead of a connection that dies later.

const (
	tcpDialBudget  = 15 * time.Second
	tcpDrainWait   = 2 * time.Minute
	tcpKeepIdle    = 60 * time.Second
	tcpKeepIntvl   = 30 * time.Second
	loopbackDialer = "127.0.0.1"
	dnsTCPDial     = 4 * time.Second // per upstream resolver before trying the next
)

var errBlocked = errors.New("blocked destination")

func addrFromTCPIP(a tcpip.Address) netip.Addr {
	switch a.Len() {
	case 4:
		return netip.AddrFrom4(a.As4())
	case 16:
		return netip.AddrFrom16(a.As16()).Unmap()
	}
	return netip.Addr{}
}

func (s *Server) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	dst := netip.AddrPortFrom(addrFromTCPIP(id.LocalAddress), id.LocalPort)
	client := netip.AddrPortFrom(addrFromTCPIP(id.RemoteAddress), id.RemotePort)

	if dst.Addr() == s.gw && dst.Port() == statusPagePort {
		s.serveStatusConn(r) // served right here: see statuspage.go
		return
	}

	ctx, cancel := context.WithTimeout(s.ctx, tcpDialBudget)
	out, err := s.dialUpstream(ctx, dst)
	cancel()
	if err != nil {
		r.Complete(true)
		if errors.Is(err, errBlocked) {
			s.log.Debugf("tcp", "%s -> %s blocked", client, dst)
		} else if s.ctx.Err() == nil {
			s.fails.add(s.log, "tcp", failReason(err), dst.String(), err)
		}
		return
	}

	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		r.Complete(true)
		out.Close()
		s.log.Debugf("tcp", "%s -> %s: handshake with the device failed: %v", client, dst, terr)
		return
	}
	r.Complete(false)
	ep.SocketOptions().SetKeepAlive(true)
	idle := tcpip.KeepaliveIdleOption(tcpKeepIdle)
	ep.SetSockOpt(&idle)
	intvl := tcpip.KeepaliveIntervalOption(tcpKeepIntvl)
	ep.SetSockOpt(&intvl)
	in := gonet.NewTCPConn(&wq, ep)
	// Blocking a device resets its connections (enforce.go): a reset goes
	// out at once, where a close would wait behind data it no longer acks.
	unregister := s.conns.add(client.Addr(), func() { ep.Abort(); in.Close(); out.Close() })
	defer unregister()

	s.tcpActive.Add(1)
	s.tcpTotal.Add(1)
	defer s.tcpActive.Add(-1)
	begin := time.Now()
	if s.log.Detailed() {
		s.log.Debugf("tcp", "%s -> %s opened", client, dst)
	}

	var up, down int64
	done := make(chan struct{}, 2)
	go func() {
		up = copyCounting(out, in, &s.bytesUp, s.pacer(client.Addr(), true))
		if tc, ok := out.(*net.TCPConn); ok {
			tc.CloseWrite() // the device is done sending
		}
		done <- struct{}{}
	}()
	go func() {
		down = copyCounting(in, out, &s.bytesDown, s.pacer(client.Addr(), false))
		in.CloseWrite()
		done <- struct{}{}
	}()
	<-done
	t := time.NewTimer(tcpDrainWait)
	finished := false
	select {
	case <-done:
		finished = true
	case <-t.C:
	case <-s.ctx.Done():
	}
	t.Stop()
	in.Close()
	out.Close()
	if !finished {
		<-done // unblocked by the closes
	}
	if s.log.Detailed() {
		s.log.Debugf("tcp", "%s -> %s closed: ↑%s ↓%s in %s", client, dst,
			fmtBytes(uint64(up)), fmtBytes(uint64(down)), time.Since(begin).Round(time.Millisecond))
	}
}

// dialUpstream picks where a client's TCP connection really goes.
func (s *Server) dialUpstream(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
	switch {
	case dst.Addr() == s.gw && dst.Port() == dnsGatewayPt && len(s.dnsUp) > 0:
		// DNS over TCP to the gateway: the upstream resolvers, in order.
		var lastErr error
		for _, up := range s.dnsUp {
			c, err := func() (net.Conn, error) {
				dctx, cancel := context.WithTimeout(ctx, dnsTCPDial)
				defer cancel()
				return s.eg.DialTCP(dctx, up)
			}()
			if err == nil {
				return c, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, lastErr
	case dst.Addr() == s.gw || s.isPhoneAddr(dst.Addr()):
		// The phone itself (config page, pairing API through the tunnel):
		// served locally, never sent out cellular.
		var d net.Dialer
		return d.DialContext(ctx, "tcp4", net.JoinHostPort(loopbackDialer, strconv.Itoa(int(dst.Port()))))
	case dropDestination(dst.Addr()):
		return nil, errBlocked
	}
	return s.eg.DialTCP(ctx, dst)
}
