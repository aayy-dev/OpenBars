package engine

import (
	"strconv"
	"time"
)

// Data limits. A device can have rules (so much per hour, day, week or
// month, or a total that never resets), all devices together can have a
// cap, and either one reaching its amount means the device is blocked,
// slowed down, or only reported, as set in the app.
//
// Usage comes from the same per-device WireGuard counters as the charts
// (stats.go). Periods follow the phone's local time: hours and days are the
// saved buckets, a week starts on WeekStart, a month on the billing day.
// Anything that counts from a moment instead of a period boundary (a total
// limit, "Reset usage", a grant of more data) is measured against a
// lifetime counter: the engine notes the counter's value the first time it
// sees that moment. While the engine isn't running no traffic flows, so the
// counter is the same then as at the moment itself.
//
// Limits are decided every second (stats loop) and applied to packets in
// enforce.go.

const (
	ActionBlock  = "block"
	ActionSlow   = "slow"
	ActionNotify = "notify"

	PeriodHour  = "hour"
	PeriodDay   = "day"
	PeriodWeek  = "week"
	PeriodMonth = "month"
	PeriodTotal = "total"

	defaultSlowKbps = 1000
	defaultWarnAt   = 80
)

// LimitRule is one allowance: Bytes per Period.
type LimitRule struct {
	Period string `json:"period"` // hour, day, week, month or total
	Bytes  int64  `json:"bytes"`
	// Start is when a total limit starts counting (unix seconds). The app
	// sets it when the limit is made.
	Start int64 `json:"start,omitempty"`
}

// Grant lets a device past its limits for a while: Bytes more data from
// Given, or no limit until Until.
type Grant struct {
	ID    string `json:"id"`
	Bytes int64  `json:"bytes,omitempty"`
	Until int64  `json:"until,omitempty"` // unix seconds
	Given int64  `json:"given"`           // unix seconds
	Ask   string `json:"ask,omitempty"`   // the request it answers, if any
}

// DeviceLimit is everything limiting one device.
type DeviceLimit struct {
	Rules    []LimitRule `json:"rules,omitempty"`
	Action   string      `json:"action,omitempty"`    // "" = the default action
	SlowKbps int         `json:"slow_kbps,omitempty"` // 0 = the default speed
	Paused   bool        `json:"paused,omitempty"`    // no internet at all
	Grants   []Grant     `json:"grants,omitempty"`
	// Reset: usage before this moment (unix seconds) doesn't count.
	Reset int64 `json:"reset,omitempty"`
	// Keys: earlier keys of the same device (it paired again with a new
	// one). Their usage counts toward these limits too.
	Keys []string `json:"keys,omitempty"`
}

// LimitSettings are the app-wide limit settings.
type LimitSettings struct {
	Action     string      `json:"action,omitempty"`      // "" = block
	SlowKbps   int         `json:"slow_kbps,omitempty"`   // 0 = 1000
	WarnAt     int         `json:"warn_at,omitempty"`     // percent; 0 = 80
	WeekStart  int         `json:"week_start,omitempty"`  // 0 = Sunday ... 6 = Saturday
	MonthStart int         `json:"month_start,omitempty"` // billing day, 1-31; 0 = 1
	Cap        []LimitRule `json:"cap,omitempty"`         // all devices together
	CapAction  string      `json:"cap_action,omitempty"`  // "" = Action
}

func (ls LimitSettings) action() string {
	if validAction(ls.Action) {
		return ls.Action
	}
	return ActionBlock
}

func (ls LimitSettings) capAction() string {
	if validAction(ls.CapAction) {
		return ls.CapAction
	}
	return ls.action()
}

func (ls LimitSettings) slowKbps() int {
	if ls.SlowKbps > 0 {
		return ls.SlowKbps
	}
	return defaultSlowKbps
}

func (ls LimitSettings) warnAt() int64 {
	if ls.WarnAt > 0 && ls.WarnAt < 100 {
		return int64(ls.WarnAt)
	}
	return defaultWarnAt
}

func validAction(a string) bool {
	return a == ActionBlock || a == ActionSlow || a == ActionNotify
}

func validPeriod(p string) bool {
	switch p {
	case PeriodHour, PeriodDay, PeriodWeek, PeriodMonth, PeriodTotal:
		return true
	}
	return false
}

// RuleStatus is a rule with how much of it is used.
type RuleStatus struct {
	Period string `json:"period"`
	Bytes  int64  `json:"bytes"`
	Used   int64  `json:"used"`
	Resets int64  `json:"resets"` // unix seconds when it starts over; 0 = never
}

func (r RuleStatus) over() bool { return r.Bytes > 0 && r.Used >= r.Bytes }

// GrantStatus is a grant with what's left of it.
type GrantStatus struct {
	ID     string `json:"id"`
	Bytes  int64  `json:"bytes,omitempty"`
	Left   int64  `json:"left,omitempty"`
	Until  int64  `json:"until,omitempty"`
	Active bool   `json:"active"`
}

// Limit states.
const (
	StateOK       = ""
	StateWarn     = "warn"     // past the warning point of a rule
	StateLimited  = "limited"  // a rule (or the cap) is used up; Action applies
	StateExtended = "extended" // a rule is used up, but a grant lets it through
	StatePaused   = "paused"
)

// LimitStatus is one device's limits as of now.
type LimitStatus struct {
	State  string        `json:"state"`
	Action string        `json:"action,omitempty"` // block, slow or notify (when limited or paused)
	Rules  []RuleStatus  `json:"rules,omitempty"`
	Grants []GrantStatus `json:"grants,omitempty"`
	Cap    bool          `json:"cap,omitempty"` // the all-devices cap is used up
}

// limitEvent is a rule crossing its warning point or its amount.
type limitEvent struct {
	kind   string // warn, limit, cap_warn, cap
	key    string
	period string
	bytes  int64
	used   int64
}

// periodBounds returns when the period containing now started and when the
// next one starts, in loc.
func periodBounds(period string, now time.Time, loc *time.Location, weekStart, monthStart int) (time.Time, time.Time) {
	t := now.In(loc)
	y, m, d := t.Date()
	switch period {
	case PeriodHour:
		start := time.Date(y, m, d, t.Hour(), 0, 0, 0, loc)
		return start, start.Add(time.Hour)
	case PeriodDay:
		return time.Date(y, m, d, 0, 0, 0, 0, loc), time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	case PeriodWeek:
		back := (int(t.Weekday()) - weekStart%7 + 7) % 7
		return time.Date(y, m, d-back, 0, 0, 0, 0, loc), time.Date(y, m, d-back+7, 0, 0, 0, 0, loc)
	case PeriodMonth:
		if monthStart < 1 {
			monthStart = 1
		}
		start := billingDay(y, m, monthStart, loc)
		if t.Before(start) {
			start = billingDay(y, m-1, monthStart, loc)
		}
		sy, sm, _ := start.Date()
		return start, billingDay(sy, sm+1, monthStart, loc)
	}
	return time.Time{}, time.Time{}
}

// billingDay is day of month m, or the month's last day if it's shorter.
func billingDay(y int, m time.Month, day int, loc *time.Location) time.Time {
	first := time.Date(y, m, 1, 0, 0, 0, 0, loc)
	last := first.AddDate(0, 1, -1).Day()
	if day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, loc)
}

// limitCalc works out limits over the usage file. Caller holds st.mu.
// With persist false nothing is recorded: moments the engine hasn't seen
// count from now, and no events are produced.
type limitCalc struct {
	st      *stats
	now     time.Time
	ls      LimitSettings
	persist bool
	events  []limitEvent
}

// mark is the lifetime counter of key at moment at (see the package
// comment above).
func (c *limitCalc) mark(key, kind string, at int64) int64 {
	name := kind + ":" + key + "@" + strconv.FormatInt(at, 10)
	if v, ok := c.st.usage.Marks[name]; ok {
		return v
	}
	v := c.st.usage.Lifetime[key]
	if c.persist {
		c.st.usage.Marks[name] = v
		c.st.dirty = true
	}
	return v
}

// since is the keys' usage from moment at.
func (c *limitCalc) since(keys []string, kind string, at int64) int64 {
	var total int64
	for _, key := range keys {
		if at <= 0 {
			total += c.st.usage.Lifetime[key]
		} else if n := c.st.usage.Lifetime[key] - c.mark(key, kind, at); n > 0 {
			total += n
		}
	}
	return total
}

// bucketBytes is key's traffic in a bucket ("" = all devices).
func bucketBytes(b *UsageBucket, key string) int64 {
	if b == nil {
		return 0
	}
	if key == "" {
		return b.Down + b.Up
	}
	r := b.Peers[key]
	return r[0] + r[1]
}

// periodUsage is the keys' usage in the period that started at start.
func (c *limitCalc) periodUsage(keys []string, period string, start time.Time) int64 {
	u := &c.st.usage
	var n int64
	if period == PeriodHour {
		b := u.Hours[strconv.FormatInt(start.Unix(), 10)]
		for _, key := range keys {
			n += bucketBytes(b, key)
		}
		return n
	}
	from := start.Format("2006-01-02")
	to := c.now.In(c.st.loc).Format("2006-01-02")
	for day, b := range u.Days {
		if day >= from && day <= to {
			for _, key := range keys {
				n += bucketBytes(b, key)
			}
		}
	}
	return n
}

// rule works out one rule for the keys, counting nothing before reset.
func (c *limitCalc) rule(keys []string, r LimitRule, reset int64) RuleStatus {
	rs := RuleStatus{Period: r.Period, Bytes: r.Bytes}
	if r.Period == PeriodTotal {
		from := r.Start
		if reset > from {
			from = reset
		}
		rs.Used = c.since(keys, "s", from)
		return rs
	}
	start, next := periodBounds(r.Period, c.now, c.st.loc, c.ls.WeekStart, c.ls.MonthStart)
	rs.Resets = next.Unix()
	if reset > start.Unix() {
		rs.Used = c.since(keys, "s", reset)
	} else {
		rs.Used = c.periodUsage(keys, r.Period, start)
	}
	return rs
}

// fire records an event once per rule and period.
func (c *limitCalc) fire(kind, key string, r RuleStatus, periodStart int64) {
	if !c.persist {
		return
	}
	name := kind + "|" + key + "|" + r.Period + "|" + strconv.FormatInt(r.Bytes, 10) + "|" + strconv.FormatInt(periodStart, 10)
	if _, done := c.st.usage.Fired[name]; done {
		return
	}
	c.st.usage.Fired[name] = c.now.Unix()
	c.st.dirty = true
	c.events = append(c.events, limitEvent{kind: kind, key: key, period: r.Period, bytes: r.Bytes, used: r.Used})
}

// periodStartOf is what fire uses to tell periods apart.
func (c *limitCalc) periodStartOf(r LimitRule, reset int64) int64 {
	if r.Period == PeriodTotal {
		if reset > r.Start {
			return reset
		}
		return r.Start
	}
	start, _ := periodBounds(r.Period, c.now, c.st.loc, c.ls.WeekStart, c.ls.MonthStart)
	if reset > start.Unix() {
		return reset
	}
	return start.Unix()
}

// rules works out a list of rules for the keys (the first names the
// events; "" is all devices); warnKind/limitKind name the events.
func (c *limitCalc) rules(keys []string, rules []LimitRule, reset int64, warnKind, limitKind string) (out []RuleStatus, over, warn bool) {
	warnAt := c.ls.warnAt()
	key := keys[0]
	for _, r := range rules {
		if !validPeriod(r.Period) || r.Bytes <= 0 {
			continue
		}
		rs := c.rule(keys, r, reset)
		out = append(out, rs)
		switch {
		case rs.over():
			over = true
			c.fire(limitKind, key, rs, c.periodStartOf(r, reset))
		case rs.Used*100 >= rs.Bytes*warnAt:
			warn = true
			c.fire(warnKind, key, rs, c.periodStartOf(r, reset))
		}
	}
	return out, over, warn
}

// grants works out a device's grants; active is true if any lets it through.
func (c *limitCalc) grants(keys []string, gs []Grant) (out []GrantStatus, active bool) {
	now := c.now.Unix()
	for _, g := range gs {
		st := GrantStatus{ID: g.ID, Bytes: g.Bytes, Until: g.Until}
		switch {
		case g.Until > 0:
			st.Active = now < g.Until
		case g.Bytes > 0:
			used := c.since(keys, "g", g.Given)
			if used < g.Bytes {
				st.Left = g.Bytes - used
				st.Active = true
			}
		}
		active = active || st.Active
		out = append(out, st)
	}
	return out, active
}

// stricter is the more restrictive of two actions.
func stricter(a, b string) string {
	rank := map[string]int{"": 0, ActionNotify: 1, ActionSlow: 2, ActionBlock: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// evaluate works out every device's limits and the cap. Caller holds st.mu.
func (c *limitCalc) evaluate(peers []PeerConfig) (map[string]LimitStatus, []RuleStatus) {
	capRules, capOver, _ := c.rules([]string{""}, c.ls.Cap, 0, "cap_warn", "cap")
	out := make(map[string]LimitStatus, len(peers))
	for _, p := range peers {
		var ls LimitStatus
		lim := p.Limit
		if lim == nil {
			lim = &DeviceLimit{}
		}
		keys := append([]string{p.PublicKey}, lim.Keys...)
		var over, warn bool
		ls.Rules, over, warn = c.rules(keys, lim.Rules, lim.Reset, "warn", "limit")
		var granted bool
		ls.Grants, granted = c.grants(keys, lim.Grants)
		action := lim.Action
		if !validAction(action) {
			action = c.ls.action()
		}
		switch {
		case lim.Paused:
			ls.State, ls.Action = StatePaused, ActionBlock
		case over && granted:
			ls.State = StateExtended
		case over:
			ls.State, ls.Action = StateLimited, action
		case warn:
			ls.State = StateWarn
		}
		if capOver && !lim.Paused {
			ls.Cap = true
			if ls.State != StateLimited {
				ls.State, ls.Action = StateLimited, c.ls.capAction()
			} else {
				ls.Action = stricter(ls.Action, c.ls.capAction())
			}
		}
		out[p.PublicKey] = ls
	}
	return out, capRules
}

// pruneLimits drops grant marks and event records nobody needs any more.
// Caller holds st.mu.
func (st *stats) pruneLimits(now time.Time) {
	oldGrant := now.AddDate(0, 0, -90).Unix()
	for name := range st.usage.Marks {
		if len(name) > 2 && name[:2] == "g:" {
			if at, err := strconv.ParseInt(name[lastAt(name)+1:], 10, 64); err == nil && at < oldGrant {
				delete(st.usage.Marks, name)
			}
		}
	}
	oldFired := now.AddDate(0, 0, -keepDays).Unix()
	for name, t := range st.usage.Fired {
		if t < oldFired {
			delete(st.usage.Fired, name)
		}
	}
}

func lastAt(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '@' {
			return i
		}
	}
	return -1
}

// limits works out limits as of now (see limitCalc).
func (st *stats) limits(now time.Time, peers []PeerConfig, ls LimitSettings, persist bool) (map[string]LimitStatus, []RuleStatus, []limitEvent) {
	st.mu.Lock()
	defer st.mu.Unlock()
	c := &limitCalc{st: st, now: now, ls: ls, persist: persist}
	statuses, capRules := c.evaluate(peers)
	return statuses, capRules, c.events
}

// LimitsView is what ReadLimits returns: each device's limits by public
// key, and the all-devices cap.
type LimitsView struct {
	Peers map[string]LimitStatus `json:"peers"`
	Cap   []RuleStatus           `json:"cap"`
}

// ReadLimits works out limits from a saved usage file without running the
// engine, so the app can show them while sharing is off.
func ReadLimits(path, timeZone string, peers []PeerConfig, ls LimitSettings) LimitsView {
	st := newStats(path, timeZone, NewLog(4))
	statuses, capRules, _ := st.limits(time.Now(), peers, ls, false)
	return LimitsView{Peers: statuses, Cap: capRules}
}
