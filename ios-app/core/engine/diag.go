package engine

import (
	"fmt"
	"net"
	"runtime"
	"runtime/metrics"
	"sort"
	"strings"
)

// Diagnostics for the upload question: when a device joins the hotspot,
// does iOS bring up a separate cellular interface (a tethering PDN), and
// which cellular interface carries hotspot vs relayed traffic?

type diag struct {
	lastIfaces string
	lastCount  map[string][2]uint64
	ticks      int
	loggedErr  bool
	lastRT     rtSample
}

// rtSample is a snapshot of Go runtime counters, to tell whether the engine
// is starved (e.g. the Network Extension's memory cap making GC thrash).
type rtSample struct {
	gcCPU, userCPU, totalCPU float64 // cpu-seconds
	cycles                   uint64
	ok                       bool
}

var rtMetricNames = []string{
	"/cpu/classes/gc/total:cpu-seconds",
	"/cpu/classes/user:cpu-seconds",
	"/cpu/classes/total:cpu-seconds",
	"/gc/cycles/total:gc-cycles",
	"/gc/heap/live:bytes",
	"/memory/classes/total:bytes",
	"/gc/gomemlimit:bytes",
}

// engineStats reports memory and CPU use since the previous call, e.g.
// "heap 12.3MB live, 38.0MB total (limit 30.0MB) · 41 GCs · CPU 63% busy, 48% of it GC · 212 goroutines".
func (d *diag) engineStats() string {
	samples := make([]metrics.Sample, len(rtMetricNames))
	for i, n := range rtMetricNames {
		samples[i].Name = n
	}
	metrics.Read(samples)
	f := func(i int) float64 {
		if samples[i].Value.Kind() == metrics.KindFloat64 {
			return samples[i].Value.Float64()
		}
		return 0
	}
	u := func(i int) uint64 {
		if samples[i].Value.Kind() == metrics.KindUint64 {
			return samples[i].Value.Uint64()
		}
		return 0
	}
	cur := rtSample{gcCPU: f(0), userCPU: f(1), totalCPU: f(2), cycles: u(3), ok: true}
	prev := d.lastRT
	d.lastRT = cur

	s := fmt.Sprintf("heap %s live, %s total", fmtBytes(u(4)), fmtBytes(u(5)))
	if lim := u(6); lim > 0 && lim < 1<<40 {
		s += fmt.Sprintf(" (limit %s)", fmtBytes(lim))
	}
	if prev.ok {
		dGC, dUser, dTotal := cur.gcCPU-prev.gcCPU, cur.userCPU-prev.userCPU, cur.totalCPU-prev.totalCPU
		s += fmt.Sprintf(" · %d GCs", cur.cycles-prev.cycles)
		if dTotal > 0 {
			busy := (dGC + dUser) / dTotal * 100
			gcShare := 0.0
			if dGC+dUser > 0 {
				gcShare = dGC / (dGC + dUser) * 100
			}
			s += fmt.Sprintf(" · CPU %.0f%% busy, %.0f%% of it GC", busy, gcShare)
		}
	}
	return s + fmt.Sprintf(" · %d goroutines", runtime.NumGoroutine())
}

func diagWatched(name string) bool {
	return strings.HasPrefix(name, "pdp_ip") || strings.HasPrefix(name, "bridge")
}

// ifaceSummary lists the watched interfaces that are up, with addresses.
func ifaceSummary() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var parts []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || !diagWatched(ifc.Name) {
			continue
		}
		var addrs []string
		for _, a := range ifaceAddrs(ifc) {
			if !a.IsLinkLocalUnicast() {
				addrs = append(addrs, a.String())
			}
		}
		if len(addrs) == 0 {
			continue
		}
		sort.Strings(addrs)
		parts = append(parts, ifc.Name+"["+strings.Join(addrs, " ")+"]")
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func fmtBytes(n uint64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

// tick runs every housekeeping interval (15s).
func (d *diag) tick(log *Logger) {
	if s := ifaceSummary(); s != d.lastIfaces {
		log.Infof("diag", "Interfaces now: %s", orNone(s))
		d.lastIfaces = s
	}
	d.ticks++
	if d.ticks%2 != 0 { // traffic every 30s
		return
	}
	cur, err := ifaceCounters()
	if err != nil {
		if !d.loggedErr {
			log.Infof("diag", "Interface counters unavailable: %v", err)
			d.loggedErr = true
		}
		return
	}
	if cur == nil {
		return
	}
	if d.lastCount != nil {
		var parts []string
		var total uint64
		var names []string
		for name := range cur {
			if diagWatched(name) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			prev, ok := d.lastCount[name]
			c := cur[name]
			in, okIn := counterDelta(prev[0], c[0])
			out, okOut := counterDelta(prev[1], c[1])
			if !ok || !okIn || !okOut {
				continue
			}
			if in+out == 0 {
				continue
			}
			total += in + out
			parts = append(parts, fmt.Sprintf("%s ↓%s ↑%s", name, fmtBytes(in), fmtBytes(out)))
		}
		rt := d.engineStats() // always sample, so each interval is 30s
		if total > 64<<10 {
			log.Debugf("diag", "Last 30 s: %s", strings.Join(parts, " · "))
			log.Debugf("diag", "Engine: %s", rt)
		}
	} else {
		d.engineStats() // baseline
	}
	d.lastCount = cur
}

// counterDelta is how much a byte counter grew. The kernel hands apps these
// counters truncated to 32 bits (they wrap every 4 GiB), so a smaller value
// after a 32-bit one is a wrap; ok is false for a real reset.
func counterDelta(prev, cur uint64) (uint64, bool) {
	switch {
	case cur >= prev:
		return cur - prev, true
	case prev < 1<<32:
		return cur + 1<<32 - prev, true
	}
	return 0, false
}
