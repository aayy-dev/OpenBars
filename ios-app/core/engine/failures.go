package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Per-connection failures (dials, UDP sends, DNS upstreams, rejected
// handshakes) aren't logged one by one: they're counted by reason, and a
// summary is logged at most once a minute while they keep happening. With
// detailed logging on, each one is also logged at debug.

const failEvery = time.Minute

type failKind struct {
	src, label, one, many string // e.g. "tcp", "TCP", "connection", "connections"
	verb                  string
}

var failKinds = map[string]failKind{
	"tcp":  {"tcp", "TCP", "connection", "connections", "failed"},
	"udp":  {"udp", "UDP", "datagram", "datagrams", "couldn't be sent"},
	"dns":  {"dns", "DNS", "upstream query", "upstream queries", "failed"},
	"icmp": {"icmp", "Ping", "ping", "pings", "couldn't be sent"},
	"wg":   {"wg", "WireGuard", "handshake", "handshakes", "were rejected"},
}

type failWindow struct {
	first    time.Time // first failure since the last summary
	lastEmit time.Time
	total    int
	reasons  map[string]int
	last     string // last target
}

type failTally struct {
	mu   sync.Mutex
	kind map[string]*failWindow
}

// add counts one failure of kind ("tcp", "udp", ...) with a short reason
// and the target it concerned. detail is logged at debug if detailed
// logging is on.
func (f *failTally) add(log *Logger, kind, reason, target string, detail error) {
	if log.Detailed() {
		k := failKinds[kind]
		if detail != nil {
			log.Debugf(k.src, "%s to %s failed: %v", k.label, target, detail)
		} else {
			log.Debugf(k.src, "%s to %s failed: %s", k.label, target, reason)
		}
	}
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.kind == nil {
		f.kind = map[string]*failWindow{}
	}
	w := f.kind[kind]
	if w == nil {
		w = &failWindow{}
		f.kind[kind] = w
	}
	if w.total == 0 {
		w.first = now
		w.reasons = map[string]int{}
	}
	w.total++
	w.reasons[reason]++
	if target != "" {
		w.last = target
	}
}

// flush logs a summary for each kind that has failures and hasn't been
// summarized in the last minute (or always, with force).
func (f *failTally) flush(log *Logger, now time.Time, force bool) {
	type out struct{ msg, hint, src string }
	var msgs []out
	f.mu.Lock()
	kinds := make([]string, 0, len(f.kind))
	for k := range f.kind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		w := f.kind[k]
		if w.total == 0 || (!force && !w.lastEmit.IsZero() && now.Sub(w.lastEmit) < failEvery) {
			continue
		}
		msg, hint := summarize(k, w, now)
		msgs = append(msgs, out{msg, hint, failKinds[k].src})
		w.total, w.reasons, w.last = 0, nil, ""
		w.lastEmit = now
	}
	f.mu.Unlock()
	for _, m := range msgs {
		log.WarnHint(m.src, m.hint, "%s", m.msg)
	}
}

// summarize builds e.g. "TCP: 14 connections failed in the last minute
// (connection refused ×9, timed out ×5); last: 142.250.72.14:443".
func summarize(kind string, w *failWindow, now time.Time) (msg, hint string) {
	k := failKinds[kind]
	type rc struct {
		r string
		n int
	}
	var rs []rc
	for r, n := range w.reasons {
		rs = append(rs, rc{r, n})
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].n != rs[j].n {
			return rs[i].n > rs[j].n
		}
		return rs[i].r < rs[j].r
	})
	parts := make([]string, len(rs))
	for i, x := range rs {
		parts[i] = fmt.Sprintf("%s ×%d", x.r, x.n)
	}
	noun := k.many
	verb := k.verb
	if w.total == 1 {
		noun = k.one
		verb = strings.Replace(verb, "were ", "was ", 1)
	}
	msg = fmt.Sprintf("%s: %d %s %s %s (%s)", k.label, w.total, noun, verb, period(now.Sub(w.first)), strings.Join(parts, ", "))
	if w.last != "" {
		if kind == "wg" {
			msg += "; last from " + w.last
		} else {
			msg += "; last: " + w.last
		}
	}
	// The hint of the most common reason that has one.
	for _, x := range rs {
		if hint = failHint(kind, x.r); hint != "" {
			break
		}
	}
	return msg, hint
}

func period(d time.Duration) string {
	switch {
	case d < 50*time.Second:
		s := int(d.Round(time.Second) / time.Second)
		if s < 1 {
			s = 1
		}
		return fmt.Sprintf("in the last %d s", s)
	case d < 90*time.Second:
		return "in the last minute"
	}
	return fmt.Sprintf("in the last %d min", int(d.Round(time.Minute)/time.Minute))
}

// Reasons used by the tally besides failReason's.
const (
	reasonUnknownKey  = "unknown device key"
	reasonWrongServer = "made for another key of this iPhone"
	reasonTooMany     = "too many flows"
	reasonBusy        = "too many queries in flight"
	reasonNoNAT64     = "IPv4 unreachable, no NAT64"
)

// failHint suggests what to try for the most common reason.
func failHint(kind, reason string) string {
	switch kind {
	case "dns":
		return "Check the upstream DNS servers in Open Bars Settings (Tunnel)."
	case "wg":
		if reason == reasonWrongServer {
			return "That device has an old config: download its config again, or pair it again."
		}
		return "A device is using a key Open Bars doesn't know (it was removed, or its config is old). Add it again."
	}
	switch reason {
	case "timed out":
		return "Check that cellular data is on for Open Bars."
	case "network unreachable", "host unreachable", "address not available", "IPv6 not available", reasonNoNAT64:
		return "The phone has no working route to the internet right now. Check that Cellular Data is on and allowed for Open Bars."
	case "not permitted":
		return "iOS blocked the connection. Check that Open Bars is allowed to use cellular data (Settings › Cellular)."
	case "too many open files", reasonTooMany:
		return "A device is opening a very large number of connections."
	}
	return ""
}

// failReason turns a dial/send error into a short, stable reason.
func failReason(err error) string {
	var ne net.Error
	switch {
	case err == nil:
		return "unknown"
	case errors.Is(err, errNoNAT64):
		return reasonNoNAT64
	case errors.Is(err, context.Canceled):
		return "stopped"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, syscall.ETIMEDOUT),
		errors.As(err, &ne) && ne.Timeout():
		return "timed out"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.ENETDOWN):
		return "network unreachable"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.EHOSTDOWN):
		return "host unreachable"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "address not available"
	case errors.Is(err, syscall.EAFNOSUPPORT):
		return "IPv6 not available"
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		return "not permitted"
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		return "too many open files"
	case errors.Is(err, syscall.ENOBUFS):
		return "no buffer space"
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}
