package engine

import (
	"runtime"
	"strings"
	"testing"
)

func TestEngineStats(t *testing.T) {
	var d diag
	first := d.engineStats()
	if !strings.HasPrefix(first, "heap ") || strings.Contains(first, "GCs") {
		t.Fatalf("first sample should only have memory: %q", first)
	}
	runtime.GC()
	second := d.engineStats()
	for _, want := range []string{"heap ", " GCs", "CPU ", "% of it GC", "goroutines"} {
		if !strings.Contains(second, want) {
			t.Fatalf("missing %q in %q", want, second)
		}
	}
}

func TestCounterDelta(t *testing.T) {
	for _, c := range []struct {
		prev, cur, want uint64
		ok              bool
	}{
		{100, 250, 150, true},
		{1<<32 - 100, 50, 150, true}, // 32-bit wrap
		{1 << 40, 5, 0, false},       // a real reset of a 64-bit counter
	} {
		got, ok := counterDelta(c.prev, c.cur)
		if got != c.want || ok != c.ok {
			t.Errorf("%d -> %d: %d %v", c.prev, c.cur, got, ok)
		}
	}
}
