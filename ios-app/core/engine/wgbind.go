package engine

import (
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	_ "unsafe" // go:linkname

	_ "golang.zx2c4.com/wireguard/conn"
)

// In the Network Extension, iOS scopes our sockets to the cellular interface
// and drops packets that hotspot devices send to them (see listenTCP). The
// WireGuard UDP port has the same problem, so there its sockets get bound to
// the hotspot bridge with IP_BOUND_IF, like the TCP listeners.
//
// wireguard-go opens its UDP sockets itself and applies every function in
// conn.controlFns to them (socket buffer sizes etc.). It has no public hook
// for binding to an interface on Apple platforms, so we add one to that list.
//
//go:linkname wgControlFns golang.zx2c4.com/wireguard/conn.controlFns
var wgControlFns []func(network, address string, c syscall.RawConn) error

var (
	wgBridgeIdx atomic.Int32 // 0 = don't bind
	wgScopeOnce sync.Once
)

func installWGScope() {
	wgScopeOnce.Do(func() {
		wgControlFns = append(wgControlFns, func(network, address string, c syscall.RawConn) error {
			if i := wgBridgeIdx.Load(); i > 0 {
				return bindSocket(network, c, int(i))
			}
			return nil
		})
	})
}

// hotspotBridge returns the Personal Hotspot bridge (the interface hotspot
// devices are on), or 0 if there is none right now. iOS creates it when the
// first device joins.
func hotspotBridge() (int, string) {
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || !strings.HasPrefix(ifc.Name, "bridge") {
			continue
		}
		if _, ok := firstUsable(ifaceAddrs(ifc), true); ok {
			return ifc.Index, ifc.Name
		}
	}
	return 0, ""
}

// scopeWireGuardToBridge keeps WireGuard's sockets bound to the current
// hotspot bridge, rebinding when it appears, changes or goes away.
func (s *Server) scopeWireGuardToBridge() {
	installWGScope()
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		idx, name := hotspotBridge()
		if int32(idx) != wgBridgeIdx.Load() {
			wgBridgeIdx.Store(int32(idx))
			switch err := s.wg.BindUpdate(); {
			case err != nil:
				s.log.ErrorHint("wg", "Devices may not reach the iPhone. Turn Personal Hotspot off and on, or restart sharing.",
					"Couldn't rebind WireGuard's UDP %d: %v", s.cfg.ListenPort, err)
			case idx > 0:
				s.log.Infof("wg", "UDP %d bound to %s (the hotspot interface)", s.cfg.ListenPort, name)
			default:
				s.log.Infof("wg", "Hotspot interface gone; UDP %d unbound until a device joins", s.cfg.ListenPort)
			}
		}
		select {
		case <-s.ctx.Done():
			wgBridgeIdx.Store(0)
			return
		case <-t.C:
		}
	}
}
