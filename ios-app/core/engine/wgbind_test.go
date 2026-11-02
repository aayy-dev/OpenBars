package engine

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

// The hook must reach wireguard-go's sockets: bind them to lo0 and check a
// packet sent over loopback still arrives.
func TestWireGuardSocketScope(t *testing.T) {
	before := len(wgControlFns)
	installWGScope()
	if len(wgControlFns) != before+1 && before == 0 {
		t.Fatalf("hook not installed: %d -> %d", before, len(wgControlFns))
	}
	lo, err := net.InterfaceByName("lo0")
	if err != nil {
		t.Skip("no lo0")
	}
	wgBridgeIdx.Store(int32(lo.Index))
	defer wgBridgeIdx.Store(0)

	b := conn.NewStdNetBind()
	fns, port, err := b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	c, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go func() {
		for i := 0; i < 20; i++ {
			c.Write([]byte("ping"))
			time.Sleep(50 * time.Millisecond)
		}
	}()

	got := make(chan string, 1)
	for _, fn := range fns {
		fn := fn
		go func() {
			bufs := [][]byte{make([]byte, 1500)}
			sizes := []int{0}
			eps := []conn.Endpoint{nil}
			if n, err := fn(bufs, sizes, eps); err == nil && n > 0 {
				select {
				case got <- string(bufs[0][:sizes[0]]):
				default:
				}
			}
		}()
	}
	select {
	case s := <-got:
		if s != "ping" {
			t.Fatalf("got %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing received on a socket bound to lo0")
	}
}

func TestPeerAddr6(t *testing.T) {
	v6, ok := peerAddr6(netip.MustParseAddr("10.66.66.5"))
	if !ok || v6.String() != "fd0b:a75::a42:4205" {
		t.Fatalf("got %v %v", v6, ok)
	}
	if _, ok := peerAddr6(netip.MustParseAddr("fd00::1")); ok {
		t.Fatal("v6 input should be rejected")
	}
}
