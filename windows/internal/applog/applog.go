// Package applog is Open Bars' log: the same entry model, noise control and
// display format as the iPhone and Mac apps (see the logging spec), so logs
// from every device read the same way.
//
// Entries are append-only. Readers fetch them incrementally by sequence
// number, so an entry never changes once it's been added; repeats of the
// same message are counted and reported in one summary entry instead.
package applog

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Level is how important an entry is.
type Level string

const (
	Debug Level = "debug"
	Info  Level = "info"
	Warn  Level = "warn"
	Error Level = "error"
)

// ParseLevel maps unknown levels to Info.
func ParseLevel(s string) Level {
	switch Level(strings.ToLower(strings.TrimSpace(s))) {
	case Debug:
		return Debug
	case Warn:
		return Warn
	case Error:
		return Error
	}
	return Info
}

// Entry is one log line. JSON keys match the other Open Bars apps.
type Entry struct {
	Seq   int64  `json:"seq"`
	T     int64  `json:"t"` // Unix milliseconds
	Level Level  `json:"lvl"`
	Src   string `json:"src"`
	Msg   string `json:"msg"`
	Hint  string `json:"hint,omitempty"`
	Rep   int    `json:"rep,omitempty"` // >0: a summary of this many more identical entries
}

// Time returns the entry's instant.
func (e Entry) Time() time.Time { return time.UnixMilli(e.T) }

// Page is what LogsJSON-style readers get: entries after a sequence number.
type Page struct {
	Next    int64   `json:"next"`
	First   int64   `json:"first"`
	Entries []Entry `json:"entries"`
}

const (
	repeatWindow = 60 * time.Second // identical entries this close together are counted, not added
	settleRepeat = 5 * time.Second  // a pending repeat this old is flushed when someone reads
)

// Ring keeps the most recent entries in memory.
type Ring struct {
	mu       sync.Mutex
	max      int
	entries  []Entry
	seq      int64
	detailed bool
	now      func() time.Time
	onAdd    func(Entry) // persistence hook, called with the lock held

	// The current run of repeats of the last added entry.
	pending     int
	pendingLast time.Time
}

// NewRing makes a ring holding up to max entries.
func NewRing(max int) *Ring {
	return &Ring{max: max, now: time.Now}
}

// OnAdd sets a hook that sees every entry as it's added (e.g. to save it).
func (r *Ring) OnAdd(f func(Entry)) {
	r.mu.Lock()
	r.onAdd = f
	r.mu.Unlock()
}

// SetDetailed turns debug entries on or off.
func (r *Ring) SetDetailed(on bool) {
	r.mu.Lock()
	r.detailed = on
	r.mu.Unlock()
}

// Detailed reports whether debug entries are kept.
func (r *Ring) Detailed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.detailed
}

// Add records an entry. Debug entries are dropped unless detailed logging is on.
func (r *Ring) Add(level Level, src, msg, hint string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if level == Debug && !r.detailed {
		return
	}
	now := r.now()
	if n := len(r.entries); n > 0 {
		last := r.entries[n-1]
		prev := last.Time()
		if r.pending > 0 {
			prev = r.pendingLast
		}
		if last.Rep == 0 && last.Level == level && last.Src == src && last.Msg == msg &&
			now.Sub(prev) < repeatWindow {
			r.pending++
			r.pendingLast = now
			return
		}
	}
	r.flushLocked()
	r.appendLocked(Entry{T: now.UnixMilli(), Level: level, Src: src, Msg: msg, Hint: hint})
}

// Addf is Add with formatting.
func (r *Ring) Addf(level Level, src, hint, format string, args ...any) {
	r.Add(level, src, fmt.Sprintf(format, args...), hint)
}

func (r *Ring) Debugf(src, format string, args ...any) { r.Addf(Debug, src, "", format, args...) }
func (r *Ring) Infof(src, format string, args ...any)  { r.Addf(Info, src, "", format, args...) }
func (r *Ring) Warnf(src, format string, args ...any)  { r.Addf(Warn, src, "", format, args...) }
func (r *Ring) Errorf(src, format string, args ...any) { r.Addf(Error, src, "", format, args...) }

// Restore puts previously saved entries back (oldest first), e.g. after a
// restart. New entries continue numbering after the last restored one.
func (r *Ring) Restore(saved []Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range saved {
		if e.Seq <= r.seq {
			continue
		}
		r.entries = append(r.entries, e)
		r.seq = e.Seq
	}
	r.trimLocked()
}

// Since returns entries with seq > after, oldest first.
func (r *Ring) Since(after int64) Page {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending > 0 && r.now().Sub(r.pendingLast) >= settleRepeat {
		r.flushLocked()
	}
	p := Page{Next: after, First: r.seq + 1, Entries: []Entry{}}
	if len(r.entries) > 0 {
		p.First = r.entries[0].Seq
	}
	for _, e := range r.entries {
		if e.Seq > after {
			p.Entries = append(p.Entries, e)
			p.Next = e.Seq
		}
	}
	return p
}

// Clear drops every entry. Sequence numbers keep counting.
func (r *Ring) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = nil
	r.pending = 0
}

// flushLocked appends the summary of a pending run of repeats, if any.
func (r *Ring) flushLocked() {
	if r.pending == 0 || len(r.entries) == 0 {
		r.pending = 0
		return
	}
	last := r.entries[len(r.entries)-1]
	n := r.pending
	r.pending = 0
	r.appendLocked(Entry{T: r.pendingLast.UnixMilli(), Level: last.Level, Src: last.Src,
		Msg: last.Msg, Hint: last.Hint, Rep: n})
}

func (r *Ring) appendLocked(e Entry) {
	r.seq++
	e.Seq = r.seq
	r.entries = append(r.entries, e)
	r.trimLocked()
	if r.onAdd != nil {
		r.onAdd(e)
	}
}

func (r *Ring) trimLocked() {
	if over := len(r.entries) - r.max; over > 0 {
		r.entries = append(r.entries[:0:0], r.entries[over:]...)
	}
}
