package engine

import (
	"encoding/binary"
	"net/netip"
	"sync/atomic"
)

// IP packet parsing and building for the paths that bypass the userspace
// TCP/IP stack (UDP NAT, DNS, ICMP echo).

const (
	protoICMP   = 1
	protoTCP    = 6
	protoUDP    = 17
	protoICMPv6 = 58
)

// ipPacket is a parsed packet from a client. payload aliases the input.
type ipPacket struct {
	version  int
	proto    uint8 // upper-layer protocol (after IPv6 extension headers)
	src, dst netip.Addr
	payload  []byte // the transport header and data (or this fragment's part)

	// Fragments (IPv4, or IPv6 with a Fragment header).
	frag     bool
	fragID   uint32
	fragOff  int  // byte offset of payload within the original datagram
	fragMore bool // more fragments follow
}

// parsePacket reads the IP header (and walks IPv6 extension headers).
// Checksums are not verified.
func parsePacket(b []byte) (ipPacket, bool) {
	if len(b) == 0 {
		return ipPacket{}, false
	}
	switch b[0] >> 4 {
	case 4:
		if len(b) < 20 {
			return ipPacket{}, false
		}
		ihl := int(b[0]&0x0f) * 4
		total := int(binary.BigEndian.Uint16(b[2:4]))
		if ihl < 20 || total < ihl || total > len(b) {
			return ipPacket{}, false
		}
		ff := binary.BigEndian.Uint16(b[6:8])
		p := ipPacket{
			version: 4,
			proto:   b[9],
			src:     netip.AddrFrom4([4]byte(b[12:16])),
			dst:     netip.AddrFrom4([4]byte(b[16:20])),
			payload: b[ihl:total],
		}
		if ff&0x2000 != 0 || ff&0x1fff != 0 {
			p.frag, p.fragMore = true, ff&0x2000 != 0
			p.fragID = uint32(binary.BigEndian.Uint16(b[4:6]))
			p.fragOff = int(ff&0x1fff) * 8
		}
		return p, true
	case 6:
		if len(b) < 40 {
			return ipPacket{}, false
		}
		plen := int(binary.BigEndian.Uint16(b[4:6]))
		if 40+plen > len(b) {
			return ipPacket{}, false
		}
		p := ipPacket{
			version: 6,
			proto:   b[6],
			src:     netip.AddrFrom16([16]byte(b[8:24])),
			dst:     netip.AddrFrom16([16]byte(b[24:40])),
			payload: b[40 : 40+plen],
		}
		// Skip extension headers so proto and payload are the upper layer.
	walk:
		for hops := 0; hops < 8; hops++ {
			h := p.payload
			switch p.proto {
			case 0, 43, 60: // hop-by-hop, routing, destination options
				if len(h) < 8 || len(h) < (int(h[1])+1)*8 {
					return ipPacket{}, false
				}
				p.proto, p.payload = h[0], h[(int(h[1])+1)*8:]
				continue
			case 44: // fragment
				if len(h) < 8 {
					return ipPacket{}, false
				}
				off := binary.BigEndian.Uint16(h[2:4])
				p.frag, p.fragMore = true, off&1 != 0
				p.fragOff = int(off>>3) * 8
				p.fragID = binary.BigEndian.Uint32(h[4:8])
				p.proto, p.payload = h[0], h[8:]
				if p.fragOff != 0 {
					return p, true // only the first fragment carries more headers
				}
				continue
			}
			break walk
		}
		return p, true
	}
	return ipPacket{}, false
}

// csumAdd adds b to a running ones'-complement sum (odd length padded).
func csumAdd(sum uint32, b []byte) uint32 {
	for len(b) >= 2 {
		sum += uint32(b[0])<<8 | uint32(b[1])
		b = b[2:]
	}
	if len(b) == 1 {
		sum += uint32(b[0]) << 8
	}
	return sum
}

// csumFold finishes an Internet checksum.
func csumFold(sum uint32) uint16 {
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// ipHeader writes an IPv4 or IPv6 header for a payload of plen bytes into
// a new packet with room for the payload, and returns it with the header
// length. ok is false if the payload can't fit the length fields.
func ipHeader(src, dst netip.Addr, proto uint8, plen int) (pkt []byte, hlen int, ok bool) {
	if src.Is4() {
		if 20+plen > 0xffff {
			return nil, 0, false
		}
		pkt = make([]byte, 20+plen)
		h := pkt[:20]
		h[0] = 0x45
		binary.BigEndian.PutUint16(h[2:4], uint16(20+plen))
		h[8] = 64 // TTL
		h[9] = proto
		s4, d4 := src.As4(), dst.As4()
		copy(h[12:16], s4[:])
		copy(h[16:20], d4[:])
		binary.BigEndian.PutUint16(h[10:12], csumFold(csumAdd(0, h)))
		return pkt, 20, true
	}
	if plen > 0xffff {
		return nil, 0, false
	}
	pkt = make([]byte, 40+plen)
	h := pkt[:40]
	h[0] = 0x60
	binary.BigEndian.PutUint16(h[4:6], uint16(plen))
	h[6] = proto
	h[7] = 64 // hop limit
	s16, d16 := src.As16(), dst.As16()
	copy(h[8:24], s16[:])
	copy(h[24:40], d16[:])
	return pkt, 40, true
}

// pseudoSum is the checksum of the IPv4/IPv6 pseudo-header.
func pseudoSum(src, dst netip.Addr, proto uint8, length int) uint32 {
	var sum uint32
	if src.Is4() {
		s, d := src.As4(), dst.As4()
		sum = csumAdd(sum, s[:])
		sum = csumAdd(sum, d[:])
	} else {
		s, d := src.As16(), dst.As16()
		sum = csumAdd(sum, s[:])
		sum = csumAdd(sum, d[:])
	}
	return sum + uint32(proto) + uint32(length>>16) + uint32(length&0xffff)
}

// buildUDP makes an IP+UDP packet. src and dst must be the same family.
// It returns nil if data is too big for one datagram.
func buildUDP(src, dst netip.AddrPort, data []byte) []byte {
	ulen := 8 + len(data)
	if ulen > 0xffff {
		return nil
	}
	pkt, hl, ok := ipHeader(src.Addr(), dst.Addr(), protoUDP, ulen)
	if !ok {
		return nil
	}
	u := pkt[hl:]
	binary.BigEndian.PutUint16(u[0:2], src.Port())
	binary.BigEndian.PutUint16(u[2:4], dst.Port())
	binary.BigEndian.PutUint16(u[4:6], uint16(ulen))
	copy(u[8:], data)
	c := csumFold(csumAdd(pseudoSum(src.Addr(), dst.Addr(), protoUDP, ulen), u))
	if c == 0 {
		c = 0xffff // 0 means "no checksum"
	}
	binary.BigEndian.PutUint16(u[6:8], c)
	return pkt
}

// buildEchoReply makes an IPv4 ICMP echo reply.
func buildEchoReply(src, dst netip.Addr, id, seq uint16, data []byte) []byte {
	pkt, hl, ok := ipHeader(src, dst, protoICMP, 8+len(data))
	if !ok {
		return nil
	}
	m := pkt[hl:]
	m[0], m[1] = 0, 0 // echo reply, code 0
	binary.BigEndian.PutUint16(m[4:6], id)
	binary.BigEndian.PutUint16(m[6:8], seq)
	copy(m[8:], data)
	binary.BigEndian.PutUint16(m[2:4], csumFold(csumAdd(0, m)))
	return pkt
}

// buildEchoReply6 makes an ICMPv6 echo reply.
func buildEchoReply6(src, dst netip.Addr, id, seq uint16, data []byte) []byte {
	pkt, hl, ok := ipHeader(src, dst, protoICMPv6, 8+len(data))
	if !ok {
		return nil
	}
	m := pkt[hl:]
	m[0], m[1] = icmpv6EchoReply, 0
	binary.BigEndian.PutUint16(m[4:6], id)
	binary.BigEndian.PutUint16(m[6:8], seq)
	copy(m[8:], data)
	binary.BigEndian.PutUint16(m[2:4], csumFold(csumAdd(pseudoSum(src, dst, protoICMPv6, len(m)), m)))
	return pkt
}

// dropDestination reports addresses never forwarded: multicast,
// unspecified, link-local, loopback, the limited broadcast, and x.y.z.255
// in private IPv4 ranges (LAN broadcast noise from a device's previous
// network; private addresses aren't reachable over the internet anyway).
func dropDestination(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsMulticast() || a.IsUnspecified() || a.IsLinkLocalUnicast() || a.IsLoopback() {
		return true
	}
	if a.Is4() {
		b := a.As4()
		if b == [4]byte{255, 255, 255, 255} || (b[3] == 255 && a.IsPrivate()) {
			return true
		}
	}
	return false
}

// fragID numbers the fragments of packets split by fragment.
var fragID atomic.Uint32

// fragment splits an IP packet built by this package (no IPv4 options, no
// IPv6 extension headers) into pieces of at most mtu bytes, so a large
// datagram for a client (a big DNS answer, say) fits the tunnel. Packets
// that already fit are returned as they are.
func fragment(pkt []byte, mtu int) [][]byte {
	if len(pkt) <= mtu || len(pkt) == 0 {
		return [][]byte{pkt}
	}
	id := fragID.Add(1)
	var out [][]byte
	if pkt[0]>>4 == 4 {
		hdr, data := pkt[:20], pkt[20:]
		step := (mtu - 20) &^ 7
		if step <= 0 {
			return nil
		}
		for off := 0; off < len(data); off += step {
			end := min(off+step, len(data))
			f := make([]byte, 20+end-off)
			copy(f, hdr)
			copy(f[20:], data[off:end])
			binary.BigEndian.PutUint16(f[2:4], uint16(len(f)))
			binary.BigEndian.PutUint16(f[4:6], uint16(id))
			flags := uint16(off / 8)
			if end < len(data) {
				flags |= 0x2000 // more fragments
			}
			binary.BigEndian.PutUint16(f[6:8], flags)
			f[10], f[11] = 0, 0
			binary.BigEndian.PutUint16(f[10:12], csumFold(csumAdd(0, f[:20])))
			out = append(out, f)
		}
		return out
	}
	hdr, data := pkt[:40], pkt[40:]
	step := (mtu - 48) &^ 7
	if step <= 0 {
		return nil
	}
	for off := 0; off < len(data); off += step {
		end := min(off+step, len(data))
		f := make([]byte, 48+end-off)
		copy(f, hdr)
		f[6] = 44 // next header: fragment
		binary.BigEndian.PutUint16(f[4:6], uint16(8+end-off))
		fh := f[40:48]
		fh[0] = hdr[6] // the original next header
		offFlags := uint16(off/8) << 3
		if end < len(data) {
			offFlags |= 1
		}
		binary.BigEndian.PutUint16(fh[2:4], offFlags)
		binary.BigEndian.PutUint32(fh[4:8], id)
		copy(f[48:], data[off:end])
		out = append(out, f)
	}
	return out
}
