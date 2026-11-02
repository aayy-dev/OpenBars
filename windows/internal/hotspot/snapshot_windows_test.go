package hotspot

import "testing"

func TestSnapshotReadsAdapters(t *testing.T) {
	ifs, err := Snapshot()
	if err != nil || len(ifs) == 0 {
		t.Fatalf("snapshot: %d adapters, %v", len(ifs), err)
	}
	for _, i := range ifs {
		t.Logf("%-3d up=%-5v %s  gateways=%v", i.Index, i.Up, i, i.Gateways)
	}
	hs, ok := Find(ifs, 0)
	t.Logf("on an iPhone hotspot: %v %v", ok, hs)
}
