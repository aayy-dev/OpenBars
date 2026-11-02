package applog

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time       { return c.t }
func (c *clock) step(d time.Duration) { c.t = c.t.Add(d) }
func newTestRing(max int) (*Ring, *clock) {
	c := &clock{t: time.Date(2026, 11, 2, 19, 4, 5, 123e6, time.UTC)}
	r := NewRing(max)
	r.now = c.now
	return r, c
}

func TestRepeatsAreSummarized(t *testing.T) {
	r, c := newTestRing(100)
	r.Warnf("svc", "iPhone not answering")
	for i := 0; i < 3; i++ {
		c.step(10 * time.Second)
		r.Warnf("svc", "iPhone not answering")
	}
	if got := len(r.Since(0).Entries); got != 1 {
		// The pending repeats are only 0 s old, so reading doesn't flush them yet.
		t.Fatalf("entries before settle = %d, want 1", got)
	}
	c.step(6 * time.Second)
	p := r.Since(0)
	if len(p.Entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(p.Entries), p.Entries)
	}
	sum := p.Entries[1]
	if sum.Rep != 3 || sum.Msg != "iPhone not answering" || sum.Level != Warn {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.T != c.t.Add(-6*time.Second).UnixMilli() {
		t.Errorf("summary time should be the last repeat's")
	}
	// The first entry was never modified.
	if p.Entries[0].Rep != 0 {
		t.Errorf("first entry changed: %+v", p.Entries[0])
	}
}

func TestDifferentEntryEndsRun(t *testing.T) {
	r, c := newTestRing(100)
	r.Infof("net", "a")
	c.step(time.Second)
	r.Infof("net", "a")
	c.step(time.Second)
	r.Infof("net", "b")
	p := r.Since(0)
	var msgs []string
	for _, e := range p.Entries {
		msgs = append(msgs, Message(e))
	}
	if strings.Join(msgs, "|") != "a|a (×1 more)|b" {
		t.Fatalf("got %q", msgs)
	}
}

func TestRepeatAfterWindowIsNewEntry(t *testing.T) {
	r, c := newTestRing(100)
	r.Infof("net", "a")
	c.step(61 * time.Second)
	r.Infof("net", "a")
	if n := len(r.Since(0).Entries); n != 2 {
		t.Fatalf("entries = %d, want 2", n)
	}
}

func TestDebugNeedsDetailed(t *testing.T) {
	r, _ := newTestRing(100)
	r.Debugf("wg", "hidden")
	r.SetDetailed(true)
	r.Debugf("wg", "shown")
	p := r.Since(0)
	if len(p.Entries) != 1 || p.Entries[0].Msg != "shown" {
		t.Fatalf("got %+v", p.Entries)
	}
}

func TestSinceAndCapacity(t *testing.T) {
	r, c := newTestRing(3)
	for i := 0; i < 5; i++ {
		c.step(time.Second)
		r.Infof("app", "n%d", i)
	}
	p := r.Since(0)
	if p.First != 3 || p.Next != 5 || len(p.Entries) != 3 {
		t.Fatalf("page = first %d next %d n %d", p.First, p.Next, len(p.Entries))
	}
	p = r.Since(4)
	if len(p.Entries) != 1 || p.Entries[0].Msg != "n4" || p.Next != 5 {
		t.Fatalf("since 4 = %+v", p)
	}
	p = r.Since(5)
	if len(p.Entries) != 0 || p.Next != 5 {
		t.Fatalf("since 5 = %+v", p)
	}
	r.Clear()
	r.Infof("app", "after clear")
	p = r.Since(5)
	if len(p.Entries) != 1 || p.Entries[0].Seq != 6 {
		t.Fatalf("after clear = %+v", p)
	}
}

func TestLineFormat(t *testing.T) {
	loc := time.FixedZone("CST", -6*3600)
	e := Entry{T: time.Date(2026, 11, 2, 19, 4, 5, 123e6, time.UTC).UnixMilli(), Level: Warn, Src: "tcp",
		Msg: "14 connections failed", Hint: "Check cellular data."}
	want := "2026-11-02 13:04:05.123  WARN   tcp     14 connections failed\n" +
		"                                        ↳ Check cellular data."
	if got := Line(e, loc); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	e.Hint, e.Rep, e.Level = "", 3, Error
	if got := Line(e, time.UTC); got != "2026-11-02 19:04:05.123  ERROR  tcp     14 connections failed (×3 more)" {
		t.Fatalf("got %q", got)
	}
}

func TestZoneLabel(t *testing.T) {
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	if got := ZoneLabel(time.FixedZone("CST", -6*3600), at); got != "local time (CST, UTC−06:00)" {
		t.Errorf("got %q", got)
	}
	if got := ZoneLabel(time.FixedZone("", 5*3600+1800), at); got != "local time (UTC+05:30)" {
		t.Errorf("got %q", got)
	}
	if got := ZoneLabel(time.UTC, at); got != "UTC" {
		t.Errorf("got %q", got)
	}
}

func TestFilePersistsAndCompacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	lf, saved, err := OpenFile(path, 3)
	if err != nil || len(saved) != 0 {
		t.Fatalf("open: %v %v", saved, err)
	}
	r, c := newTestRing(3)
	r.OnAdd(lf.Append)
	for i := 0; i < 7; i++ {
		c.step(time.Second)
		r.Infof("app", "n%d", i)
	}
	lf.Close()

	lf2, saved, err := OpenFile(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer lf2.Close()
	if len(saved) != 3 || saved[0].Msg != "n4" || saved[2].Msg != "n6" {
		t.Fatalf("saved = %+v", saved)
	}
	r2, _ := newTestRing(3)
	r2.Restore(saved)
	r2.Infof("app", "next")
	p := r2.Since(0)
	if last := p.Entries[len(p.Entries)-1]; last.Seq != 8 {
		t.Fatalf("restored ring continues at %d, want 8", last.Seq)
	}
}
