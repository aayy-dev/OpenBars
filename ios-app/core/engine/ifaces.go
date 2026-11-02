package engine

import (
	"net"
	"net/netip"
	"sort"
	"strings"
)

// NetInfo is what the engine knows about the phone's interfaces. Every
// serialized key is always present (the app's decoder needs them all).
type NetInfo struct {
	HotspotIP   string `json:"hotspot_ip"`  // the hotspot bridge's IPv4 (normally 172.20.10.1)
	CellularIf  string `json:"cellular_if"` // the pdp_ip* interface used for internet
	CellularIP  string `json:"cellular_ip"`
	CellularIdx int    `json:"-"` // its index, for IP_BOUND_IF
	WiFiIP      string `json:"wifi_ip"`
	VPNActive   bool   `json:"vpn_active"` // a VPN owns the default route
	VPNIf       string `json:"vpn_if"`
	DefaultIf   string `json:"default_if"` // the interface iOS routes internet through
}

// ifaceAddrs returns an interface's addresses, IPv4-mapped ones unmapped,
// with no zone. Link-local and loopback are included; nil on error.
func ifaceAddrs(ifc net.Interface) []netip.Addr {
	addrs, err := ifc.Addrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		default:
			continue
		}
		if na, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, na.Unmap())
		}
	}
	return out
}

// firstUsable returns the first IPv4 (want4) or global IPv6 address,
// skipping link-local and loopback.
func firstUsable(addrs []netip.Addr, want4 bool) (netip.Addr, bool) {
	for _, a := range addrs {
		if a.IsLinkLocalUnicast() || a.IsLoopback() {
			continue
		}
		if want4 && a.Is4() {
			return a, true
		}
		if !want4 && a.Is6() && a.IsGlobalUnicast() {
			return a, true
		}
	}
	return netip.Addr{}, false
}

// ifaceInfo is the part of an interface the selection looks at.
type ifaceInfo struct {
	name  string
	index int
	up    bool
	addrs []netip.Addr
}

// DetectNetInfo inspects the interfaces:
//   - the default interface is the one holding the source address the OS
//     would use to reach the internet (IPv4, then IPv6);
//   - the hotspot is the first bridge* with an IPv4;
//   - cellular is a pdp_ip* interface, preferring the default one;
//   - a VPN is active only if a utun/ipsec/ppp interface is the default.
func DetectNetInfo() NetInfo {
	ifs, err := net.Interfaces()
	if err != nil {
		return NetInfo{}
	}
	infos := make([]ifaceInfo, 0, len(ifs))
	for _, ifc := range ifs {
		infos = append(infos, ifaceInfo{
			name:  ifc.Name,
			index: ifc.Index,
			up:    ifc.Flags&net.FlagUp != 0,
			addrs: ifaceAddrs(ifc),
		})
	}
	var src []netip.Addr
	for _, target := range []string{"1.1.1.1:53", "[2606:4700:4700::1111]:53"} {
		if a, ok := routeSource(target); ok {
			src = append(src, a)
		}
	}
	return selectNetInfo(infos, src)
}

// routeSource asks the OS which local address it would use to reach
// target. Connecting a UDP socket sends nothing.
func routeSource(target string) (netip.Addr, bool) {
	c, err := net.Dial("udp", target)
	if err != nil {
		return netip.Addr{}, false
	}
	defer c.Close()
	ua, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	a, ok := netip.AddrFromSlice(ua.IP)
	return a.Unmap(), ok
}

func isVPNName(name string) bool {
	return strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "ipsec") || strings.HasPrefix(name, "ppp")
}

// selectNetInfo is DetectNetInfo's decision, over a snapshot of the
// interfaces and the route source addresses.
func selectNetInfo(ifs []ifaceInfo, routeSrc []netip.Addr) NetInfo {
	ifs = append([]ifaceInfo(nil), ifs...)
	sort.SliceStable(ifs, func(i, j int) bool { return ifs[i].name < ifs[j].name })

	var ni NetInfo
	isSrc := func(a netip.Addr) bool {
		for _, s := range routeSrc {
			if s == a {
				return true
			}
		}
		return false
	}
	type cell struct {
		name  string
		index int
		ip    netip.Addr
		has4  bool
	}
	var cells []cell
	for _, ifc := range ifs {
		if !ifc.up {
			continue
		}
		if ni.DefaultIf == "" {
			for _, a := range ifc.addrs {
				if isSrc(a) {
					ni.DefaultIf = ifc.name
					break
				}
			}
		}
		switch {
		case strings.HasPrefix(ifc.name, "bridge"):
			if ni.HotspotIP == "" {
				if a, ok := firstUsable(ifc.addrs, true); ok {
					ni.HotspotIP = a.String()
				}
			}
		case strings.HasPrefix(ifc.name, "pdp_ip"):
			if a, ok := firstUsable(ifc.addrs, true); ok {
				cells = append(cells, cell{ifc.name, ifc.index, a, true})
			} else if a, ok := firstUsable(ifc.addrs, false); ok {
				cells = append(cells, cell{ifc.name, ifc.index, a, false})
			}
		case ifc.name == "en0":
			if a, ok := firstUsable(ifc.addrs, true); ok {
				ni.WiFiIP = a.String()
			}
		}
	}
	if isVPNName(ni.DefaultIf) {
		ni.VPNActive, ni.VPNIf = true, ni.DefaultIf
	}

	// iPhones have several pdp_ip interfaces (internet, IMS/voice, MMS);
	// pinning to a carrier-only one stalls connections. Prefer the one iOS
	// routes internet through, then one with IPv4, then any.
	pick := -1
	for i, c := range cells {
		if c.name == ni.DefaultIf {
			pick = i
			break
		}
	}
	if pick < 0 {
		for i, c := range cells {
			if c.has4 {
				pick = i
				break
			}
		}
	}
	if pick < 0 && len(cells) > 0 {
		pick = 0
	}
	if pick >= 0 {
		c := cells[pick]
		ni.CellularIf, ni.CellularIP, ni.CellularIdx = c.name, c.ip.String(), c.index
	}
	return ni
}
