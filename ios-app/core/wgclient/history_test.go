package wgclient

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestHistoryRecord(t *testing.T) {
	h := &history{}
	h.record(100, 1000, 100, 0, 0)
	h.record(101, 3000, 300, 1000, 100)
	h.record(102, 500, 50, 3000, 300) // counters restarted
	got := h.since(0)
	want := []sample{{100, 1000, 100}, {101, 2000, 200}, {102, 500, 50}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d: got %+v want %+v", i, got[i], want[i])
		}
	}
	if s := h.since(101); len(s) != 1 || s[0].T != 102 {
		t.Fatalf("since(101) = %+v", s)
	}
	for i := 0; i < historySeconds+20; i++ {
		h.record(int64(200+i), 0, 0, 0, 0)
	}
	if n := len(h.since(0)); n != historySeconds {
		t.Fatalf("kept %d samples", n)
	}
}

func TestHistorySampler(t *testing.T) {
	h := &history{}
	var rx atomic.Int64
	h.start(func() (int64, int64) { return rx.Add(1000), 0 })
	time.Sleep(2500 * time.Millisecond)
	h.halt()
	if n := len(h.since(0)); n != 0 {
		t.Fatalf("halt should clear history, have %d", n)
	}
	// HistoryJSON on the package history is an empty array when stopped.
	var s []sample
	if err := json.Unmarshal([]byte(HistoryJSON(0)), &s); err != nil || len(s) != 0 {
		t.Fatalf("HistoryJSON = %v %v", s, err)
	}
}

func TestHistorySamplerRuns(t *testing.T) {
	h := &history{}
	var rx atomic.Int64
	h.start(func() (int64, int64) { return rx.Add(1000), 500 })
	time.Sleep(2300 * time.Millisecond)
	got := h.since(0)
	h.halt()
	if len(got) < 2 || got[1].Down != 1000 || got[0].Up != 500 || got[1].Up != 0 {
		t.Fatalf("samples %+v", got)
	}
}
