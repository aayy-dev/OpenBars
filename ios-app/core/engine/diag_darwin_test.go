//go:build darwin

package engine

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The hand-parsed if_msghdr2 counters must agree with netstat -ib.
func TestIfaceCountersMatchNetstat(t *testing.T) {
	c, err := ifaceCounters()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("netstat", "-ib", "-I", "lo0").Output()
	if err != nil {
		t.Skip("netstat unavailable")
	}
	// Name Mtu Network Address Ipkts Ierrs Ibytes Opkts Oerrs Obytes Coll
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || f[0] != "lo0" || !strings.HasPrefix(f[2], "<Link") {
			continue
		}
		ib, _ := strconv.ParseUint(f[5], 10, 64)
		ob, _ := strconv.ParseUint(f[8], 10, 64)
		got := c["lo0"]
		// The kernel gives apps 32-bit counters (netstat shows the full
		// 64-bit ones), and they move between the two reads.
		near := func(parsed, full uint64) bool {
			want := full & (1<<32 - 1)
			d := int64(parsed) - int64(want)
			return d > -1<<26 && d < 1<<26 || parsed == full
		}
		if !near(got[0], ib) || !near(got[1], ob) {
			t.Fatalf("lo0 counters %v, netstat says in=%d out=%d", got, ib, ob)
		}
		t.Logf("lo0 ok: parsed %v, netstat in=%d out=%d", got, ib, ob)
		return
	}
	t.Skip("no lo0 link line in netstat output")
}
