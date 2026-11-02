package engine

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"time"
)

// listenTCP opens the TCP listener for the config page (and pairing API).
//
// In the app it's a plain wildcard listener. Inside the Network Extension
// (cfg.LowMemory) that doesn't work: iOS scopes the extension's sockets to
// the cellular interface, and the kernel drops connections that arrive from
// hotspot devices on bridge100 ("tcp drop incoming ... interface: pdp_ip0
// ... reason: NECP" in the iPhone's log). So there we listen on the hotspot
// address with the socket explicitly bound to the bridge (IP_BOUND_IF), plus
// on 127.0.0.1, where connections to the phone itself from inside the
// tunnel are sent (see handleTCP).
func listenTCP(s *Server, port int) (net.Listener, error) {
	if !s.cfg.LowMemory {
		return net.Listen("tcp", fmt.Sprintf(":%d", port))
	}
	return newScopedListener(port, s.log, "bridge")
}

// scopedListener merges one loopback listener and one interface-bound
// listener per hotspot bridge into a single net.Listener. Bridges come and
// go as devices join and leave the hotspot, so they're rescanned every few
// seconds.
type scopedListener struct {
	port   int
	prefix string // interface name prefix to bind to ("bridge")
	log    *Logger
	conns  chan net.Conn
	done   chan struct{}
	once   sync.Once

	mu    sync.Mutex
	lns   map[string]net.Listener // key: name#index@addr
	lo    net.Listener
	ready chan struct{} // closed after the first scan
}

func newScopedListener(port int, log *Logger, prefix string) (*scopedListener, error) {
	lo, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	l := &scopedListener{
		port: port, prefix: prefix, log: log,
		conns: make(chan net.Conn), done: make(chan struct{}),
		lns: map[string]net.Listener{}, lo: lo, ready: make(chan struct{}),
	}
	go l.acceptFrom("lo0", lo)
	go l.watch()
	<-l.ready
	return l, nil
}

func (l *scopedListener) watch() {
	l.rescan()
	close(l.ready)
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-t.C:
			l.rescan()
		}
	}
}

// rescan binds a listener to every up interface matching the prefix and
// drops listeners for interfaces (or addresses) that went away.
func (l *scopedListener) rescan() {
	want := map[string]struct {
		idx  int
		addr netip.Addr
		name string
	}{}
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || !strings.HasPrefix(ifc.Name, l.prefix) {
			continue
		}
		if a, ok := firstUsable(ifaceAddrs(ifc), true); ok {
			key := fmt.Sprintf("%s#%d@%s", ifc.Name, ifc.Index, a)
			want[key] = struct {
				idx  int
				addr netip.Addr
				name string
			}{ifc.Index, a, ifc.Name}
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.done:
		return
	default:
	}
	for key, ln := range l.lns {
		if _, ok := want[key]; !ok {
			ln.Close()
			delete(l.lns, key)
		}
	}
	for key, w := range want {
		if _, ok := l.lns[key]; ok {
			continue
		}
		idx := w.idx
		lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
			return bindSocket(network, c, idx)
		}}
		ln, err := lc.Listen(context.Background(), "tcp4", netip.AddrPortFrom(w.addr, uint16(l.port)).String())
		if err != nil {
			l.log.WarnHint("web", "Open Bars retries every few seconds; 127.0.0.1 still works.",
				"Couldn't listen on %s %s:%d (bound to the hotspot interface): %v", w.name, w.addr, l.port, err)
			continue
		}
		l.lns[key] = ln
		l.log.Infof("web", "listening on %s %s:%d (bound to the hotspot interface)", w.name, w.addr, l.port)
		go l.acceptFrom(key, ln)
	}
}

func (l *scopedListener) acceptFrom(key string, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			// Closed by rescan/Close, or the interface vanished: forget it so
			// the next rescan can bind again.
			l.mu.Lock()
			if l.lns[key] == ln {
				delete(l.lns, key)
			}
			l.mu.Unlock()
			return
		}
		select {
		case l.conns <- c:
		case <-l.done:
			c.Close()
			return
		}
	}
}

func (l *scopedListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *scopedListener) Close() error {
	l.once.Do(func() {
		close(l.done)
		l.mu.Lock()
		for key, ln := range l.lns {
			ln.Close()
			delete(l.lns, key)
		}
		l.lo.Close()
		l.mu.Unlock()
	})
	return nil
}

func (l *scopedListener) Addr() net.Addr { return &net.TCPAddr{Port: l.port} }
