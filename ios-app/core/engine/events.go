package engine

import (
	"encoding/json"
	"sync"
	"time"
)

// Events are the things worth a notification on the sharing iPhone: a
// device asking to connect, a device or all devices nearing or reaching a
// limit, a device asking for more. Whatever runs the engine (the app, or
// the packet tunnel extension) polls them with EventsJSON and posts the
// notifications.
//
// Sequence numbers start from the clock, so a new engine's events always
// come after an earlier engine's in the same process.

const keepEvents = 200

// Event kinds.
const (
	EventPair    = "pair"     // Pair is set
	EventWarn    = "warn"     // a device passed the warning point of a rule
	EventLimit   = "limit"    // a device used up a rule
	EventCapWarn = "cap_warn" // all devices passed the warning point of the cap
	EventCap     = "cap"      // all devices used up the cap
	EventAsk     = "ask"      // Ask is set
)

// Event is one notification-worthy thing.
type Event struct {
	Seq    int64        `json:"seq"`
	T      int64        `json:"t"` // unix seconds
	Kind   string       `json:"kind"`
	Key    string       `json:"key,omitempty"` // device public key
	Name   string       `json:"name,omitempty"`
	Period string       `json:"period,omitempty"`
	Bytes  int64        `json:"bytes,omitempty"`  // the rule's amount
	Used   int64        `json:"used,omitempty"`   // used so far
	Action string       `json:"action,omitempty"` // what happens now: block, slow or notify
	Pair   *PairRequest `json:"pair,omitempty"`
	Ask    *Ask         `json:"ask,omitempty"`
}

type eventLog struct {
	mu     sync.Mutex
	next   int64
	events []Event
}

func newEventLog() *eventLog {
	return &eventLog{next: time.Now().UnixMicro()}
}

func (l *eventLog) add(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	e.Seq = l.next
	if e.T == 0 {
		e.T = time.Now().Unix()
	}
	l.events = append(l.events, e)
	if len(l.events) > keepEvents {
		l.events = append(l.events[:0], l.events[len(l.events)-keepEvents:]...)
	}
}

// EventsView is EventsJSON's shape: events after the given sequence number
// and the number to pass next time.
type EventsView struct {
	Next   int64   `json:"next"`
	Events []Event `json:"events"`
}

func (l *eventLog) after(seq int64) EventsView {
	l.mu.Lock()
	defer l.mu.Unlock()
	v := EventsView{Next: l.next, Events: []Event{}}
	if seq > l.next {
		seq = 0 // from an older engine with a later clock: send everything
	}
	for _, e := range l.events {
		if e.Seq > seq {
			v.Events = append(v.Events, e)
		}
	}
	return v
}

func (l *eventLog) json(seq int64) string {
	b, _ := json.Marshal(l.after(seq))
	return string(b)
}
