package hotspot

import (
	"net/netip"
	"testing"
)

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, a := range s {
		out = append(out, netip.MustParseAddr(a))
	}
	return out
}

func TestFind(t *testing.T) {
	eth := Iface{Index: 5, LUID: 50, Name: "Ethernet", Up: true, Addrs: addrs("192.168.1.11"), Gateways: addrs("192.168.1.1")}
	wifi := Iface{Index: 9, LUID: 90, Name: "Wi-Fi", Wireless: true, Up: true, Addrs: addrs("172.20.10.4"), Gateways: addrs("172.20.10.1")}
	usb := Iface{Index: 3, LUID: 30, Name: "Apple Mobile Device Ethernet", Up: true, Addrs: addrs("172.20.10.6")}
	tunnel := Iface{Index: 12, LUID: 120, Name: "Open Bars", Up: true, Addrs: addrs("172.20.10.9")}

	if _, ok := Find([]Iface{eth}, 0); ok {
		t.Fatal("found a hotspot on a home network")
	}
	got, ok := Find([]Iface{eth, usb, wifi}, 0)
	if !ok || got.Name != "Wi-Fi" {
		t.Fatalf("got %v, want Wi-Fi (has the iPhone as gateway)", got)
	}
	if got.Address() != netip.MustParseAddr("172.20.10.4") {
		t.Fatalf("address %v", got.Address())
	}
	got, ok = Find([]Iface{eth, usb}, 0)
	if !ok || got.Name != "Apple Mobile Device Ethernet" {
		t.Fatalf("USB hotspot not found: %v", got)
	}
	if _, ok := Find([]Iface{tunnel}, 120); ok {
		t.Fatal("matched the tunnel's own adapter")
	}
	down := wifi
	down.Up = false
	if _, ok := Find([]Iface{down}, 0); ok {
		t.Fatal("matched an adapter that's down")
	}
	gw := Iface{Index: 7, Up: true, Addrs: addrs("172.20.10.1")}
	if _, ok := Find([]Iface{gw}, 0); ok {
		t.Fatal("an adapter holding the iPhone's own address isn't a client")
	}
}
