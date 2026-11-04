package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
	_ "time/tzdata" // local days for usage even where the OS zone database isn't readable
)

// Traffic history for the app's charts, built from WireGuard's per-device
// byte counters (so the device list always adds up to the total):
//   - live:  one sample per second for the last 5 minutes (memory only)
//   - hours: totals per local hour for the last 7 days      (saved)
//   - days:  totals per local day for the last 400 days     (saved)
// "Down" is to the device (WireGuard tx), "up" is from it (rx).
// The saved file stays small: about 60 bytes per device per day.

const (
	liveSeconds = 300
	keepHours   = 7 * 24
	keepDays    = 400
	saveEvery   = time.Minute
	// Busier traffic is saved sooner, so a crash or a killed app loses at
	// most about this much usage (which data limits count on).
	saveAfterBytes = 10_000_000
	saveSoonest    = 5 * time.Second
)

// Rates is [down, up] in bytes.
type Rates [2]int64

// Sample is one second of traffic.
type Sample struct {
	T     int64            `json:"t"` // unix seconds
	Down  int64            `json:"down"`
	Up    int64            `json:"up"`
	Peers map[string]Rates `json:"peers,omitempty"` // by public key; only devices that moved data
}

// UsageBucket is the traffic in one hour or day.
type UsageBucket struct {
	Down  int64            `json:"down"`
	Up    int64            `json:"up"`
	Peers map[string]Rates `json:"peers,omitempty"`
}

func (b *UsageBucket) add(total Rates, peers map[string]Rates) {
	b.Down += total[0]
	b.Up += total[1]
	if b.Peers == nil {
		b.Peers = map[string]Rates{}
	}
	for k, r := range peers {
		p := b.Peers[k]
		b.Peers[k] = Rates{p[0] + r[0], p[1] + r[1]}
	}
}

type usageFile struct {
	Version int                     `json:"version"`
	Hours   map[string]*UsageBucket `json:"hours"` // unix start of the local hour
	Days    map[string]*UsageBucket `json:"days"`  // local date, 2006-01-02
	Names   map[string]string       `json:"names"` // public key -> device name, kept after removal
	// Seen is when each device last sent anything (unix seconds), so the app
	// can say "last seen" across restarts. Connected devices send keepalives
	// at least every 25 s, so a recent time also means "connected now".
	Seen map[string]int64 `json:"seen,omitempty"`

	// For data limits (limits.go): bytes each device ever moved ("" = all
	// devices), that counter at moments limits count from, and which limit
	// notices were already sent.
	Lifetime map[string]int64 `json:"lifetime,omitempty"`
	Marks    map[string]int64 `json:"marks,omitempty"`
	Fired    map[string]int64 `json:"fired,omitempty"`
}

type stats struct {
	mu    sync.Mutex
	loc   *time.Location
	path  string
	live  []Sample
	prev  map[string]Rates // last cumulative counters per device
	usage usageFile
	dirty bool
	saved time.Time
	// unsaved is the traffic recorded since the last save.
	unsaved int64
}

func newStats(path, timeZone string, log *Logger) *stats {
	st := &stats{loc: time.Local, path: path, prev: map[string]Rates{}}
	if timeZone != "" {
		if loc, err := time.LoadLocation(timeZone); err == nil {
			st.loc = loc
		} else {
			log.Warnf("usage", "Unknown time zone %q, using %s", timeZone, st.loc)
		}
	}
	st.usage = usageFile{Version: 1, Hours: map[string]*UsageBucket{}, Days: map[string]*UsageBucket{}, Names: map[string]string{}, Seen: map[string]int64{},
		Lifetime: map[string]int64{}, Marks: map[string]int64{}, Fired: map[string]int64{}}
	if path != "" {
		if b, err := os.ReadFile(path); err != nil && !os.IsNotExist(err) {
			log.WarnHint("usage", "Usage history starts over.", "Can't read %s: %v", filepath.Base(path), err)
		} else if err == nil {
			var u usageFile
			if err := json.Unmarshal(b, &u); err == nil && u.Version == 1 {
				if u.Hours != nil {
					st.usage.Hours = u.Hours
				}
				if u.Days != nil {
					st.usage.Days = u.Days
				}
				if u.Names != nil {
					st.usage.Names = u.Names
				}
				if u.Seen != nil {
					st.usage.Seen = u.Seen
				}
				if u.Lifetime != nil {
					st.usage.Lifetime = u.Lifetime
				}
				if u.Marks != nil {
					st.usage.Marks = u.Marks
				}
				if u.Fired != nil {
					st.usage.Fired = u.Fired
				}
			} else {
				log.WarnHint("usage", "Usage history starts over.", "Can't read %s, starting over: %v", filepath.Base(path), err)
			}
		}
	}
	st.saved = time.Now()
	return st
}

// record turns cumulative per-device counters into one second of traffic.
func (st *stats) record(now time.Time, counters map[string]Rates, names map[string]string) {
	st.mu.Lock()
	defer st.mu.Unlock()

	smp := Sample{T: now.Unix()}
	var total Rates
	for k, c := range counters {
		p := st.prev[k] // zero for a device we haven't seen: count from 0
		d := Rates{c[0] - p[0], c[1] - p[1]}
		if d[0] < 0 || d[1] < 0 { // counters restarted (device re-added)
			d = c
		}
		st.prev[k] = c
		if d[1] > 0 { // received from the device (keepalives count)
			st.usage.Seen[k] = now.Unix()
		}
		if d[0] == 0 && d[1] == 0 {
			continue
		}
		if smp.Peers == nil {
			smp.Peers = map[string]Rates{}
		}
		smp.Peers[k] = d
		total[0] += d[0]
		total[1] += d[1]
		st.usage.Lifetime[k] += d[0] + d[1]
	}
	for k := range st.prev {
		if _, ok := counters[k]; !ok {
			delete(st.prev, k)
		}
	}
	smp.Down, smp.Up = total[0], total[1]

	st.live = append(st.live, smp)
	if len(st.live) > liveSeconds {
		st.live = append(st.live[:0], st.live[len(st.live)-liveSeconds:]...)
	}

	if total[0] != 0 || total[1] != 0 {
		st.usage.Lifetime[""] += total[0] + total[1]
		st.unsaved += total[0] + total[1]
		local := now.In(st.loc)
		hourKey := strconv.FormatInt(time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, st.loc).Unix(), 10)
		dayKey := local.Format("2006-01-02")
		for _, m := range []struct {
			buckets map[string]*UsageBucket
			key     string
		}{{st.usage.Hours, hourKey}, {st.usage.Days, dayKey}} {
			b := m.buckets[m.key]
			if b == nil {
				b = &UsageBucket{}
				m.buckets[m.key] = b
			}
			b.add(total, smp.Peers)
		}
		st.dirty = true
	}
	for k, n := range names {
		if n != "" && st.usage.Names[k] != n {
			st.usage.Names[k] = n
			st.dirty = true
		}
	}
}

// prune drops hours and days past their retention. Caller holds mu.
func (st *stats) prune(now time.Time) {
	oldestHour := now.Add(-keepHours * time.Hour).Unix()
	for k := range st.usage.Hours {
		if t, err := strconv.ParseInt(k, 10, 64); err != nil || t < oldestHour {
			delete(st.usage.Hours, k)
		}
	}
	oldestDay := now.In(st.loc).AddDate(0, 0, -keepDays).Format("2006-01-02")
	for k := range st.usage.Days {
		if k < oldestDay {
			delete(st.usage.Days, k)
		}
	}
	st.pruneLimits(now)
}

// save writes the usage file if it changed: at most once a minute, or every
// few seconds while a lot of data moves, unless forced.
func (st *stats) save(now time.Time, force bool) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.path == "" || !st.dirty {
		return nil
	}
	since := now.Sub(st.saved)
	due := since >= saveEvery || (since >= saveSoonest && st.unsaved >= saveAfterBytes)
	if !force && !due {
		return nil
	}
	st.prune(now)
	b, err := json.Marshal(st.usage)
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(st.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.path); err != nil {
		return err
	}
	st.dirty = false
	st.saved = now
	st.unsaved = 0
	return nil
}

// since returns the live samples newer than t (unix seconds), oldest first.
func (st *stats) since(t int64) []Sample {
	st.mu.Lock()
	defer st.mu.Unlock()
	i := sort.Search(len(st.live), func(i int) bool { return st.live[i].T > t })
	out := make([]Sample, len(st.live)-i)
	copy(out, st.live[i:])
	return out
}

// UsagePoint is one hour (Start) or day (Date) of usage.
type UsagePoint struct {
	Start int64  `json:"start,omitempty"`
	Date  string `json:"date,omitempty"`
	UsageBucket
}

// UsageView is what the app charts: hours (last 7 days) and the last n days,
// oldest first, with device names (including removed devices).
type UsageView struct {
	Hours []UsagePoint      `json:"hours"`
	Days  []UsagePoint      `json:"days"`
	Names map[string]string `json:"names"`
	Seen  map[string]int64  `json:"seen"` // public key -> unix seconds it last sent anything
}

func (st *stats) view(now time.Time, days int) UsageView {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.prune(now)
	v := UsageView{Hours: []UsagePoint{}, Days: []UsagePoint{}, Names: map[string]string{}, Seen: map[string]int64{}}
	for k, b := range st.usage.Hours {
		t, _ := strconv.ParseInt(k, 10, 64)
		v.Hours = append(v.Hours, UsagePoint{Start: t, UsageBucket: *b})
	}
	sort.Slice(v.Hours, func(i, j int) bool { return v.Hours[i].Start < v.Hours[j].Start })
	if days <= 0 || days > keepDays {
		days = keepDays
	}
	oldest := now.In(st.loc).AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	for k, b := range st.usage.Days {
		if k >= oldest {
			v.Days = append(v.Days, UsagePoint{Date: k, UsageBucket: *b})
		}
	}
	sort.Slice(v.Days, func(i, j int) bool { return v.Days[i].Date < v.Days[j].Date })
	for k, n := range st.usage.Names {
		v.Names[k] = n
	}
	for k, t := range st.usage.Seen {
		v.Seen[k] = t
	}
	return v
}

// lastSeen is when the device with this public key last sent anything (unix
// seconds, 0 = never).
func (st *stats) lastSeen(key string) int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.usage.Seen[key]
}

// ReadUsage reads a saved usage file without running the server, so the app
// can show history while sharing is off.
func ReadUsage(path, timeZone string, days int) UsageView {
	return newStats(path, timeZone, NewLog(4)).view(time.Now(), days)
}
