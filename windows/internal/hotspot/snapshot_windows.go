package hotspot

import (
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// Snapshot lists the IPv4 adapters Windows knows about.
func Snapshot() ([]Iface, error) {
	adapters, err := winipcfg.GetAdaptersAddresses(windows.AF_INET, winipcfg.GAAFlagIncludeGateways|
		winipcfg.GAAFlagSkipAnycast|winipcfg.GAAFlagSkipMulticast|winipcfg.GAAFlagSkipDNSServer)
	if err != nil {
		return nil, err
	}
	var out []Iface
	for _, a := range adapters {
		i := Iface{
			Index:    a.IfIndex,
			LUID:     uint64(a.LUID),
			Name:     a.FriendlyName(),
			Desc:     a.Description(),
			Wireless: a.IfType == winipcfg.IfTypeIEEE80211,
			Up:       a.OperStatus == winipcfg.IfOperStatusUp,
		}
		for u := a.FirstUnicastAddress; u != nil; u = u.Next {
			if ip, ok := netip.AddrFromSlice(u.Address.IP()); ok {
				i.Addrs = append(i.Addrs, ip.Unmap())
			}
		}
		for g := a.FirstGatewayAddress; g != nil; g = g.Next {
			if ip, ok := netip.AddrFromSlice(g.Address.IP()); ok {
				i.Gateways = append(i.Gateways, ip.Unmap())
			}
		}
		out = append(out, i)
	}
	return out, nil
}
