// Package hotspot finds the network adapter that's on an iPhone's Personal
// Hotspot. An iPhone hotspot always hands out addresses in 172.20.10.0/28
// with itself at 172.20.10.1, over Wi-Fi or a USB cable. Which iPhone it is
// gets confirmed separately by asking Open Bars on it.
package hotspot

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

var (
	// Subnet is the iPhone hotspot's network.
	Subnet = netip.MustParsePrefix("172.20.10.0/28")
	// Gateway is the iPhone on its own hotspot.
	Gateway = netip.MustParseAddr("172.20.10.1")
)

// Iface is one network adapter as Windows reports it.
type Iface struct {
	Index    uint32 // IPv4 interface index
	LUID     uint64
	Name     string // "Wi-Fi", "Ethernet 3"
	Desc     string // "Intel(R) Wi-Fi 6 AX201 160MHz"
	Wireless bool
	Up       bool
	Addrs    []netip.Addr // IPv4 unicast addresses
	Gateways []netip.Addr
}

// String describes the adapter for logs: `Wi-Fi "Broadcom WiFi" (172.20.10.4)`.
func (i Iface) String() string {
	kind := "Ethernet/USB"
	if i.Wireless {
		kind = "Wi-Fi"
	}
	var addrs []string
	for _, a := range i.Addrs {
		addrs = append(addrs, a.String())
	}
	return fmt.Sprintf("%s %q (%s)", kind, i.Name, strings.Join(addrs, ", "))
}

// Address is the adapter's address on the hotspot (zero if none).
func (i Iface) Address() netip.Addr {
	for _, a := range i.Addrs {
		if Subnet.Contains(a) && a != Gateway {
			return a
		}
	}
	return netip.Addr{}
}

func (i Iface) hasGateway() bool {
	for _, g := range i.Gateways {
		if g == Gateway {
			return true
		}
	}
	return false
}

// Find returns the adapter on an iPhone hotspot, skipping the tunnel's own
// adapter (exclude, by LUID). When several qualify it prefers one whose
// gateway is the iPhone, then Wi-Fi, then the lowest index, so the choice is
// stable.
func Find(ifaces []Iface, exclude uint64) (Iface, bool) {
	var found []Iface
	for _, i := range ifaces {
		if !i.Up || (exclude != 0 && i.LUID == exclude) || !i.Address().IsValid() {
			continue
		}
		found = append(found, i)
	}
	if len(found) == 0 {
		return Iface{}, false
	}
	sort.SliceStable(found, func(a, b int) bool {
		x, y := found[a], found[b]
		if x.hasGateway() != y.hasGateway() {
			return x.hasGateway()
		}
		if x.Wireless != y.Wireless {
			return x.Wireless
		}
		return x.Index < y.Index
	})
	return found[0], true
}
