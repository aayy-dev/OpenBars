// Package applog is the in-memory log shared by the engine (wgcore) and the
// Connect client (wgclient). Entries carry a level, a short source name, a
// one-line message and an optional hint. Times are stored as Unix
// milliseconds; formatting them for display is the app's job.
//
// Noise control: entries are never changed once added (readers fetch by
// sequence number). An entry identical (level, source, message) to the last
// one added, within a minute of the previous occurrence, is only counted;
// when the run of repeats ends, one summary entry with the count is
// appended. Debug entries are only kept while detailed logging is on.
package applog

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Level is an entry's severity.
type Level int8

const (
	Debug Level = iota
	Info
	Warn
	Error
)

func (l Level) String() string {
	switch l {
	case Debug:
		return "debug"
	case Warn:
		return "warn"
	case Error:
		return "error"
	}
	return "info"
}

// ParseLevel maps "debug", "info", "warn"/"warning" and "error" (any case)
// to a Level. Anything else is Info.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return Debug
	case "warn", "warning":
		return Warn
	case "error":
		return Error
	}
	return Info
}

// Entry is one log entry, in the JSON shape the apps decode.
type Entry struct {
	Seq  int64  `json:"seq"`
	T    int64  `json:"t"` // Unix milliseconds
	Lvl  string `json:"lvl"`
	Src  string `json:"src"`
	Msg  string `json:"msg"`
	Hint string `json:"hint,omitempty"`
	Rep  int    `json:"rep,omitempty"` // on a repeat summary: occurrences since the previous identical entry
}

const (
	// RepeatWindow: an identical entry within this long of the previous
	// occurrence is counted as a repeat.
	RepeatWindow = 60 * time.Second
	// RepeatSettle: a pending run of repeats is written out when the log is
	// read and the last repeat is at least this old.
	RepeatSettle = 5 * time.Second
)

const maxMsgBytes = 2000

// Logger is a fixed-size ring of entries. All methods are safe for
// concurrent use, and a nil *Logger discards everything.
type Logger struct {
	mu       sync.Mutex
	buf      []Entry // ring storage, grows up to cap
	head     int     // index of the oldest entry when full
	capacity int
	seq      int64 // last sequence number handed out
	echo     func(string)
	boot     int64 // Unix ms when this ring was created

	// The last entry added, and the repeats of it not yet written out.
	prev    Entry // Seq unused; T = time of the latest occurrence
	hasPrev bool
	pending int

	detailed atomic.Bool
	now      func() time.Time // for tests
}

// New returns a Logger that keeps the last capacity entries (minimum 1).
func New(capacity int) *Logger {
	if capacity < 1 {
		capacity = 1
	}
	return &Logger{capacity: capacity, now: time.Now, boot: time.Now().UnixMilli()}
}

// SetDetailed turns debug entries on or off.
func (l *Logger) SetDetailed(on bool) {
	if l != nil {
		l.detailed.Store(on)
	}
}

// Detailed reports whether debug entries are being kept. Check it before
// building expensive debug messages.
func (l *Logger) Detailed() bool { return l != nil && l.detailed.Load() }

// SetEcho mirrors every entry, as one text line, to fn (tests use t.Log).
// fn is called outside the logger's lock.
func (l *Logger) SetEcho(fn func(string)) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.echo = fn
	l.mu.Unlock()
}

func (l *Logger) Debugf(src, format string, args ...any) {
	if l.Detailed() {
		l.Add(Debug, src, fmt.Sprintf(format, args...), "")
	}
}

func (l *Logger) Infof(src, format string, args ...any) {
	l.Add(Info, src, fmt.Sprintf(format, args...), "")
}

func (l *Logger) Warnf(src, format string, args ...any) {
	l.Add(Warn, src, fmt.Sprintf(format, args...), "")
}

func (l *Logger) Errorf(src, format string, args ...any) {
	l.Add(Error, src, fmt.Sprintf(format, args...), "")
}

// WarnHint logs a warning with a one-line suggestion of what to try.
func (l *Logger) WarnHint(src, hint, format string, args ...any) {
	l.Add(Warn, src, fmt.Sprintf(format, args...), hint)
}

// ErrorHint logs an error with a one-line suggestion of what to try.
func (l *Logger) ErrorHint(src, hint, format string, args ...any) {
	l.Add(Error, src, fmt.Sprintf(format, args...), hint)
}

// Add records an entry. Debug entries are dropped unless detailed logging
// is on.
func (l *Logger) Add(lvl Level, src, msg, hint string) {
	if l == nil || (lvl == Debug && !l.detailed.Load()) {
		return
	}
	src = strings.TrimSpace(src)
	if src == "" {
		src = "app"
	}
	msg = oneLine(msg)
	hint = oneLine(hint)
	t := l.now().UnixMilli()
	lv := lvl.String()

	l.mu.Lock()
	echo := l.echo
	l.settle(t, RepeatWindow)
	if l.hasPrev && l.prev.Lvl == lv && l.prev.Src == src && l.prev.Msg == msg && t-l.prev.T < RepeatWindow.Milliseconds() {
		l.pending++
		l.prev.T = t
		l.mu.Unlock()
		l.doEcho(echo, lv, src, msg+" (repeat)", "")
		return
	}
	l.settle(t, 0)
	e := Entry{T: t, Lvl: lv, Src: src, Msg: msg, Hint: hint}
	l.push(e)
	l.prev, l.hasPrev = e, true
	l.mu.Unlock()
	l.doEcho(echo, lv, src, msg, hint)
}

// settle writes out a pending run of repeats as one summary entry if its
// last repeat is at least age old at time now. Caller holds mu.
func (l *Logger) settle(now int64, age time.Duration) {
	if l.pending == 0 || now-l.prev.T < age.Milliseconds() {
		return
	}
	e := l.prev
	e.Rep = l.pending
	l.push(e)
	l.pending = 0
}

func (l *Logger) doEcho(echo func(string), lv, src, msg, hint string) {
	if echo == nil {
		return
	}
	line := fmt.Sprintf("%-5s %-6s %s", strings.ToUpper(lv), src, msg)
	if hint != "" {
		line += " ↳ " + hint
	}
	echo(line)
}

// push appends e with the next sequence number. Caller holds mu.
func (l *Logger) push(e Entry) {
	l.seq++
	e.Seq = l.seq
	if len(l.buf) < l.capacity {
		l.buf = append(l.buf, e)
		return
	}
	l.buf[l.head] = e
	l.head = (l.head + 1) % l.capacity
}

// ordered returns the entries oldest first. Caller holds mu.
func (l *Logger) ordered() []Entry {
	out := make([]Entry, 0, len(l.buf))
	if len(l.buf) < l.capacity {
		return append(out, l.buf...)
	}
	out = append(out, l.buf[l.head:]...)
	return append(out, l.buf[:l.head]...)
}

// Clear empties the ring. Sequence numbers keep counting.
func (l *Logger) Clear() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.buf = nil
	l.head = 0
	l.hasPrev, l.pending = false, 0
	l.mu.Unlock()
}

// Since returns the entries with seq > after, oldest first, plus the oldest
// seq still in the ring (first) and the seq of the last entry returned
// (next; after itself if none). If after is beyond the newest entry, the
// process must have restarted since the caller's last fetch, so everything
// is returned.
func (l *Logger) Since(after int64) (entries []Entry, first, next int64) {
	entries = []Entry{}
	if l == nil {
		return entries, 1, after
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.settle(l.now().UnixMilli(), RepeatSettle)
	all := l.ordered()
	first = l.seq + 1
	if len(all) > 0 {
		first = all[0].Seq
	}
	if after > l.seq {
		after = 0
	}
	next = after
	for _, e := range all {
		if e.Seq <= after {
			continue
		}
		entries = append(entries, e)
		next = e.Seq
	}
	return entries, first, next
}

// JSON is Since as {"next","first","boot","entries":[...]}. boot is when
// this ring was created (Unix ms), so a reader can tell a restarted process
// from a long-running one.
func (l *Logger) JSON(after int64) string {
	entries, first, next := l.Since(after)
	var boot int64
	if l != nil {
		boot = l.boot
	}
	b, _ := json.Marshal(struct {
		Next    int64   `json:"next"`
		First   int64   `json:"first"`
		Boot    int64   `json:"boot"`
		Entries []Entry `json:"entries"`
	}{next, first, boot, entries})
	return string(b)
}

// Tail renders every stored entry as "level src message" lines, oldest
// first, plus a pending run of repeats as the summary it will become.
// Debugging and tests only; the apps use JSON.
func (l *Logger) Tail() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	all := l.ordered()
	if l.pending > 0 {
		e := l.prev
		e.Rep = l.pending
		all = append(all, e)
	}
	l.mu.Unlock()
	var b strings.Builder
	for i, e := range all {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s %s %s", e.Lvl, e.Src, e.Msg)
		if e.Hint != "" {
			b.WriteString(" ↳ " + e.Hint)
		}
		if e.Rep > 0 {
			fmt.Fprintf(&b, " (×%d more)", e.Rep)
		}
	}
	return b.String()
}

// oneLine flattens line breaks and caps the length.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "\r\n") {
		s = strings.Join(strings.Fields(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)), " ")
	}
	if len(s) > maxMsgBytes {
		cut := maxMsgBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// ShortKey shortens a base64 public key for logs: its first 8 characters
// and an ellipsis. Private keys must never be passed here or logged.
func ShortKey(pub string) string {
	pub = strings.TrimSpace(pub)
	if len(pub) <= 8 {
		return pub
	}
	return pub[:8] + "…"
}
