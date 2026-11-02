package applog

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTest(n int) (*Logger, *clock) {
	c := &clock{t: time.UnixMilli(1_800_000_000_000)}
	l := New(n)
	l.now = c.now
	return l, c
}

type page struct {
	Next    int64   `json:"next"`
	First   int64   `json:"first"`
	Boot    int64   `json:"boot"`
	Entries []Entry `json:"entries"`
}

func fetch(t *testing.T, l *Logger, after int64) page {
	t.Helper()
	var p page
	s := l.JSON(after)
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatalf("bad JSON %s: %v", s, err)
	}
	if p.Entries == nil {
		t.Fatalf("entries must be [] not null: %s", s)
	}
	return p
}

func TestEntryJSONShape(t *testing.T) {
	l, c := newTest(10)
	l.WarnHint("web", "Is Personal Hotspot on?", "Couldn't listen on %s", "172.20.10.1:8080")
	s := l.JSON(0)
	want := `{"next":1,"first":1,"boot":` + jsonInt(l.boot) + `,"entries":[{"seq":1,"t":` + jsonInt(c.t.UnixMilli()) +
		`,"lvl":"warn","src":"web","msg":"Couldn't listen on 172.20.10.1:8080","hint":"Is Personal Hotspot on?"}]}`
	if s != want {
		t.Fatalf("got  %s\nwant %s", s, want)
	}
	// Empty: next echoes after, first is the next seq to be used.
	l.Clear()
	if p := fetch(t, l, 1); p.Next != 1 || p.First != 2 || len(p.Entries) != 0 {
		t.Fatalf("after clear %+v", p)
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestRingWrapsAndSeqSurvivesClear(t *testing.T) {
	l, c := newTest(3)
	for _, m := range []string{"a", "b", "c", "d", "e"} {
		c.t = c.t.Add(time.Second)
		l.Infof("share", "%s", m)
	}
	p := fetch(t, l, 0)
	if len(p.Entries) != 3 || p.First != 3 || p.Next != 5 || p.Entries[0].Msg != "c" || p.Entries[2].Msg != "e" {
		t.Fatalf("wrap: %+v", p)
	}
	if p := fetch(t, l, 4); len(p.Entries) != 1 || p.Entries[0].Seq != 5 {
		t.Fatalf("since 4: %+v", p)
	}
	if p := fetch(t, l, 5); len(p.Entries) != 0 || p.Next != 5 {
		t.Fatalf("since 5: %+v", p)
	}
	l.Clear()
	l.Infof("share", "f")
	if p := fetch(t, l, 5); len(p.Entries) != 1 || p.Entries[0].Seq != 6 || p.First != 6 {
		t.Fatalf("after clear: %+v", p)
	}
	// A reader that remembers a seq from a previous process gets everything.
	if p := fetch(t, l, 99); len(p.Entries) != 1 || p.Next != 6 {
		t.Fatalf("restart: %+v", p)
	}
}

func TestLevelsAndDetailed(t *testing.T) {
	l, _ := newTest(10)
	l.Debugf("tcp", "hidden")
	l.Add(Debug, "app", "hidden too", "")
	l.SetDetailed(true)
	l.Debugf("tcp", "shown")
	l.Add(ParseLevel("bogus"), "", "from swift", "")
	l.Add(ParseLevel("ERROR"), "ext", "line1\nline2", "")
	p := fetch(t, l, 0)
	if len(p.Entries) != 3 {
		t.Fatalf("%+v", p.Entries)
	}
	if e := p.Entries[0]; e.Lvl != "debug" || e.Msg != "shown" {
		t.Fatalf("%+v", e)
	}
	if e := p.Entries[1]; e.Lvl != "info" || e.Src != "app" {
		t.Fatalf("unknown level / empty src: %+v", e)
	}
	if e := p.Entries[2]; e.Lvl != "error" || e.Msg != "line1 line2" {
		t.Fatalf("%+v", e)
	}
}

type ent struct {
	lvl, msg string
	t        int64 // seconds after the test clock's start
	rep      int
}

func entries(p page) []ent {
	var out []ent
	for _, e := range p.Entries {
		out = append(out, ent{e.Lvl, e.Msg, (e.T - 1_800_000_000_000) / 1000, e.Rep})
	}
	return out
}

func checkEntries(t *testing.T, got []ent, want ...ent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d: got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func (c *clock) at(s int) { c.t = time.UnixMilli(1_800_000_000_000 + int64(s)*1000) }

// Repeats are counted, never written into an existing entry; one summary
// entry is appended when the run ends because something else is logged.
func TestRepeatsEndedByOtherEntry(t *testing.T) {
	l, c := newTest(10)
	c.at(0)
	l.WarnHint("udp", "hint", "boom")
	first := fetch(t, l, 0) // a reader has the first entry already
	for _, s := range []int{1, 2, 3} {
		c.at(s)
		l.WarnHint("udp", "hint", "boom")
	}
	if p := fetch(t, l, first.Next); len(p.Entries) != 0 {
		t.Fatalf("repeats must not show up while recent: %+v", entries(p))
	}
	c.at(4)
	l.Infof("udp", "boom") // different level: ends the run
	p := fetch(t, l, first.Next)
	checkEntries(t, entries(p), ent{"warn", "boom", 3, 3}, ent{"info", "boom", 4, 0})
	if p.Entries[0].Hint != "hint" || p.Entries[0].Seq != 2 {
		t.Fatalf("summary %+v", p.Entries[0])
	}
	// The first entry was never touched.
	if all := fetch(t, l, 0); all.Entries[0].Rep != 0 || all.Entries[0].T != 1_800_000_000_000 {
		t.Fatalf("first entry changed: %+v", all.Entries[0])
	}
}

// Each repeat must be within 60 s of the previous occurrence (a sliding
// window); after a 60 s gap the run ends and the next one is a new entry.
func TestRepeatsEndedByQuiet(t *testing.T) {
	l, c := newTest(10)
	for _, s := range []int{0, 50, 100, 150} { // 150 s span, each gap < 60 s
		c.at(s)
		l.Errorf("usage", "save failed")
	}
	c.at(210) // 60 s after the last repeat: the run is over
	l.Errorf("usage", "save failed")
	checkEntries(t, entries(fetch(t, l, 0)),
		ent{"error", "save failed", 0, 0},
		ent{"error", "save failed", 150, 3},
		ent{"error", "save failed", 210, 0})
}

// A reader settles a pending run once its last repeat is 5 s old, and
// repeats after that start a new run.
func TestRepeatsSettledByRead(t *testing.T) {
	l, c := newTest(10)
	for _, s := range []int{0, 1, 2} {
		c.at(s)
		l.Warnf("wg", "x")
	}
	c.at(6)
	if p := fetch(t, l, 0); len(p.Entries) != 1 {
		t.Fatalf("settled too early: %+v", entries(p))
	}
	c.at(7)
	p := fetch(t, l, 0)
	checkEntries(t, entries(p), ent{"warn", "x", 0, 0}, ent{"warn", "x", 2, 2})
	c.at(20)
	l.Warnf("wg", "x") // within 60 s of the previous occurrence: counted again
	c.at(30)
	q := fetch(t, l, p.Next)
	checkEntries(t, entries(q), ent{"warn", "x", 20, 1})
	// Nothing pending: reads add nothing.
	c.at(100)
	if r := fetch(t, l, q.Next); len(r.Entries) != 0 {
		t.Fatalf("%+v", entries(r))
	}
}

func TestClearDropsPendingRepeats(t *testing.T) {
	l, _ := newTest(10)
	l.Infof("a", "m")
	l.Infof("a", "m")
	l.Clear()
	l.Infof("a", "m")
	p := fetch(t, l, 0)
	checkEntries(t, entries(p), ent{"info", "m", 0, 0})
	if p.Entries[0].Seq != 2 {
		t.Fatalf("seq %d", p.Entries[0].Seq)
	}
}

func TestEchoAndTail(t *testing.T) {
	l, _ := newTest(10)
	var got []string
	l.SetEcho(func(s string) { got = append(got, s) })
	l.WarnHint("web", "try this", "listening on %s", "en0")
	l.WarnHint("web", "try this", "listening on %s", "en0")
	if len(got) != 2 || !strings.Contains(got[0], "WARN") || !strings.Contains(got[0], "listening on en0") {
		t.Fatalf("echo %q", got)
	}
	if tail := l.Tail(); tail != "warn web listening on en0 ↳ try this\nwarn web listening on en0 ↳ try this (×1 more)" {
		t.Fatalf("tail %q", tail)
	}
}

func TestNilAndLimits(t *testing.T) {
	var l *Logger
	l.Infof("x", "y")
	l.SetDetailed(true)
	l.Clear()
	if l.JSON(0) == "" || l.Tail() != "" || l.Detailed() {
		t.Fatal("nil logger")
	}
	m := New(0)
	m.Infof("a", "%s", strings.Repeat("é", 3000))
	e, _, _ := m.Since(0)
	if len(e) != 1 || len(e[0].Msg) > maxMsgBytes+len("…") || !strings.HasSuffix(e[0].Msg, "…") {
		t.Fatalf("cap: %d", len(e[0].Msg))
	}
	if ShortKey("ABCDEFGHIJKLMNOP") != "ABCDEFGH…" || ShortKey("abc") != "abc" {
		t.Fatal("ShortKey")
	}
}

func TestConcurrent(t *testing.T) {
	l := New(100)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				l.Infof("tcp", "%d %d", i, j)
				if j%10 == 0 {
					l.JSON(int64(j))
				}
			}
		}(i)
	}
	wg.Wait()
	e, _, next := l.Since(0)
	if len(e) != 100 || next != 1600 {
		t.Fatalf("%d entries, next %d", len(e), next)
	}
}
