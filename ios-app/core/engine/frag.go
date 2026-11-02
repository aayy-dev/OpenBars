package engine

import (
	"net/netip"
	"sort"
	"sync"
	"time"
)

// Reassembly of fragmented UDP datagrams from clients (IPv4, or IPv6 with a
// Fragment header), so a big datagram from a device still goes out as one.
// It's deliberately small: a handful of datagrams in progress, each at most
// 64 KiB, dropped if not complete within a few seconds.

const (
	maxReassembly   = 64
	reassemblyLimit = 65535
	reassemblyTTL   = 10 * time.Second
)

type fragKey struct {
	version  int
	src, dst netip.Addr
	proto    uint8
	id       uint32
}

type fragPiece struct {
	off  int
	data []byte
}

type fragBuf struct {
	pieces []fragPiece
	size   int // total length once the last fragment arrived, else -1
	bytes  int
	born   time.Time
}

type reassembler struct {
	mu   sync.Mutex
	bufs map[fragKey]*fragBuf
}

func newReassembler() *reassembler {
	return &reassembler{bufs: map[fragKey]*fragBuf{}}
}

// add takes one fragment; when it completes a datagram it returns the
// whole packet (payload = the full transport header and data).
func (r *reassembler) add(p *ipPacket) (ipPacket, bool) {
	k := fragKey{p.version, p.src, p.dst, p.proto, p.fragID}
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.bufs[k]
	if b == nil {
		if len(r.bufs) >= maxReassembly {
			return ipPacket{}, false
		}
		b = &fragBuf{size: -1, born: time.Now()}
		r.bufs[k] = b
	}
	end := p.fragOff + len(p.payload)
	if end > reassemblyLimit || b.bytes+len(p.payload) > reassemblyLimit {
		delete(r.bufs, k)
		return ipPacket{}, false
	}
	b.pieces = append(b.pieces, fragPiece{p.fragOff, append([]byte(nil), p.payload...)})
	b.bytes += len(p.payload)
	if !p.fragMore {
		b.size = end
	}
	if b.size < 0 {
		return ipPacket{}, false
	}
	// Complete when the pieces cover 0..size without gaps.
	sort.Slice(b.pieces, func(i, j int) bool { return b.pieces[i].off < b.pieces[j].off })
	covered := 0
	for _, pc := range b.pieces {
		if pc.off > covered {
			return ipPacket{}, false
		}
		covered = max(covered, pc.off+len(pc.data))
	}
	if covered < b.size {
		return ipPacket{}, false
	}
	whole := make([]byte, b.size)
	for _, pc := range b.pieces {
		copy(whole[pc.off:], pc.data)
	}
	delete(r.bufs, k)
	return ipPacket{version: p.version, proto: p.proto, src: p.src, dst: p.dst, payload: whole}, true
}

// reap drops datagrams that didn't complete in time.
func (r *reassembler) reap() {
	cutoff := time.Now().Add(-reassemblyTTL)
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, b := range r.bufs {
		if b.born.Before(cutoff) {
			delete(r.bufs, k)
		}
	}
}
