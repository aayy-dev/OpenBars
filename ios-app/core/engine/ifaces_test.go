package engine

import (
	"net/netip"
	"testing"
)

func addrs(ss ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

func TestSelectNetInfo(t *testing.T) {
	iphone := []ifaceInfo{
		{name: "lo0", index: 1, up: true, addrs: addrs("127.0.0.1", "::1")},
		{name: "pdp_ip0", index: 3, up: true, addrs: addrs("fe80::1", "2600:380::5")}, // IMS, IPv6 only
		{name: "pdp_ip1", index: 4, up: true, addrs: addrs("10.20.30.40")},            // internet
		{name: "ipsec0", index: 9, up: true, addrs: addrs("192.0.0.2")},               // Wi-Fi Calling
		{name: "utun3", index: 12, up: true, addrs: addrs("100.64.0.9")},              // not the default
		{name: "bridge100", index: 20, up: true, addrs: addrs("fe80::2", "172.20.10.1")},
		{name: "bridge101", index: 21, up: true, addrs: addrs("172.20.11.1")},
		{name: "en0", index: 5, up: false, addrs: addrs("192.168.1.9")}, // Wi-Fi off
	}
	ni := selectNetInfo(iphone, addrs("10.20.30.40"))
	want := NetInfo{HotspotIP: "172.20.10.1", CellularIf: "pdp_ip1", CellularIP: "10.20.30.40", CellularIdx: 4, DefaultIf: "pdp_ip1"}
	if ni != want {
		t.Fatalf("iPhone:\n got %+v\nwant %+v", ni, want)
	}
	if idx, name := pinFor(ni, true); idx != 4 || name != "pdp_ip1" {
		t.Fatalf("pin %d %s", idx, name)
	}

	// A VPN on the phone owns the route: noted, and egress not pinned.
	ni = selectNetInfo(iphone, addrs("100.64.0.9"))
	if !ni.VPNActive || ni.VPNIf != "utun3" || ni.DefaultIf != "utun3" || ni.CellularIf != "pdp_ip1" {
		t.Fatalf("VPN: %+v", ni)
	}
	if idx, _ := pinFor(ni, true); idx != 0 {
		t.Fatal("must follow the VPN's default route")
	}

	// No route source: still the cellular interface with IPv4.
	ni = selectNetInfo(iphone, nil)
	if ni.CellularIf != "pdp_ip1" || ni.DefaultIf != "" || ni.VPNActive {
		t.Fatalf("no default: %+v", ni)
	}
	// Default is the IPv6-only IMS-like interface: it wins.
	ni = selectNetInfo(iphone, addrs("2600:380::5"))
	if ni.CellularIf != "pdp_ip0" || ni.CellularIP != "2600:380::5" {
		t.Fatalf("IPv6 default: %+v", ni)
	}
	// Only IPv6 cellular, no default: the first one.
	ni = selectNetInfo([]ifaceInfo{{name: "pdp_ip2", index: 7, up: true, addrs: addrs("2600::7")}, {name: "pdp_ip1", index: 6, up: true, addrs: addrs("fe80::9")}}, nil)
	if ni.CellularIf != "pdp_ip2" || ni.CellularIdx != 7 {
		t.Fatalf("v6 only: %+v", ni)
	}
	// Wi-Fi up and default.
	ifs := append([]ifaceInfo{}, iphone...)
	ifs[7].up = true
	ni = selectNetInfo(ifs, addrs("192.168.1.9"))
	if ni.WiFiIP != "192.168.1.9" || ni.DefaultIf != "en0" || ni.VPNActive {
		t.Fatalf("wifi: %+v", ni)
	}
	if idx, _ := pinFor(ni, true); idx != 4 {
		t.Fatal("cellular-only must still pin to cellular when Wi-Fi is the default")
	}
	if idx, _ := pinFor(ni, false); idx != 0 {
		t.Fatal("cellular-only off must use the default route")
	}
	if idx, _ := pinFor(NetInfo{}, true); idx != 0 {
		t.Fatal("no cellular: default route")
	}
}

func TestFirstUsable(t *testing.T) {
	as := addrs("fe80::1", "127.0.0.1", "::1", "fd00::5", "10.0.0.1")
	if a, ok := firstUsable(as, true); !ok || a.String() != "10.0.0.1" {
		t.Fatal(a)
	}
	if a, ok := firstUsable(as, false); !ok || a.String() != "fd00::5" {
		t.Fatal(a)
	}
	if _, ok := firstUsable(addrs("fe80::1", "169.254.0.1"), true); ok {
		t.Fatal("link-local only")
	}
}
