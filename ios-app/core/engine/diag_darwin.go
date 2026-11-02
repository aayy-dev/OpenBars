//go:build darwin

package engine

import (
	"encoding/binary"
	"net"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

// ifaceCounters reads per-interface byte counters (sysctl NET_RT_IFLIST2,
// the same data getifaddrs uses). Offsets of struct if_msghdr2 on arm64
// XNU, taken from the macOS SDK: type @3, index @12, ibytes @96, obytes @104.
func ifaceCounters() (map[string][2]uint64, error) {
	const netRtIflist2, rtmIfinfo2 = 6, 18
	// {CTL_NET, PF_ROUTE, 0, AF_UNSPEC, NET_RT_IFLIST2, 0}; the node has no
	// sysctl name, so go through x/net/route's numeric fetch.
	buf, err := route.FetchRIB(unix.AF_UNSPEC, route.RIBType(netRtIflist2), 0)
	if err != nil {
		return nil, err
	}
	names := map[int]string{}
	if ifs, err := net.Interfaces(); err == nil {
		for _, ifc := range ifs {
			names[ifc.Index] = ifc.Name
		}
	}
	out := map[string][2]uint64{}
	for off := 0; off+4 <= len(buf); {
		n := int(binary.LittleEndian.Uint16(buf[off:]))
		if n < 4 || off+n > len(buf) {
			break
		}
		if buf[off+3] == rtmIfinfo2 && n >= 112 {
			idx := int(binary.LittleEndian.Uint16(buf[off+12:]))
			if name, ok := names[idx]; ok {
				out[name] = [2]uint64{binary.LittleEndian.Uint64(buf[off+96:]), binary.LittleEndian.Uint64(buf[off+104:])}
			}
		}
		off += n
	}
	return out, nil
}
