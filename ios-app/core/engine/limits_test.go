package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestPeriodBounds(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, ny)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		period     string
		now        string
		week, mon  int
		start, end string
	}{
		{PeriodHour, "2026-11-04 15:42", 0, 1, "2026-11-04 15:00", "2026-11-04 16:00"},
		{PeriodDay, "2026-11-04 15:42", 0, 1, "2026-11-04 00:00", "2026-11-05 00:00"},
		// Wednesday; weeks from Sunday, and from Monday.
		{PeriodWeek, "2026-11-04 15:42", 0, 1, "2026-11-01 00:00", "2026-11-08 00:00"},
		{PeriodWeek, "2026-11-04 15:42", 1, 1, "2026-11-02 00:00", "2026-11-09 00:00"},
		// Billing day 15, before and after it in the month.
		{PeriodMonth, "2026-11-04 15:42", 0, 15, "2026-10-15 00:00", "2026-11-15 00:00"},
		{PeriodMonth, "2026-11-20 15:42", 0, 15, "2026-11-15 00:00", "2026-12-15 00:00"},
		// Billing day 31 in a 28-day month: the last day.
		{PeriodMonth, "2027-02-28 09:00", 0, 31, "2027-02-28 00:00", "2027-03-31 00:00"},
		{PeriodMonth, "2027-02-27 09:00", 0, 31, "2027-01-31 00:00", "2027-02-28 00:00"},
		// The day daylight saving ends is 25 hours long.
		{PeriodDay, "2026-11-01 12:00", 0, 1, "2026-11-01 00:00", "2026-11-02 00:00"},
	}
	for _, c := range cases {
		start, end := periodBounds(c.period, at(c.now), ny, c.week, c.mon)
		if !start.Equal(at(c.start)) || !end.Equal(at(c.end)) {
			t.Errorf("%s at %s (week %d, billing %d): %s - %s, want %s - %s", c.period, c.now, c.week, c.mon,
				start.Format("2006-01-02 15:04"), end.Format("2006-01-02 15:04"), c.start, c.end)
		}
	}
}

// usage builds stats in memory and feeds it traffic: one call per second.
type usage struct {
	st   *stats
	t    time.Time
	cum  map[string]Rates
	keys []string
}

func newUsage(t *testing.T, start time.Time) *usage {
	st := newStats("", "UTC", NewLog(10))
	return &usage{st: st, t: start, cum: map[string]Rates{}}
}

// move adds bytes for key (half down, half up) one second later.
func (u *usage) move(key string, n int64) {
	u.t = u.t.Add(time.Second)
	r := u.cum[key]
	u.cum[key] = Rates{r[0] + n/2, r[1] + n - n/2}
	counters := map[string]Rates{}
	for k, v := range u.cum {
		counters[k] = v
	}
	u.st.record(u.t, counters, nil)
}

func (u *usage) eval(peers []PeerConfig, ls LimitSettings) (map[string]LimitStatus, []RuleStatus, []limitEvent) {
	return u.st.limits(u.t, peers, ls, true)
}

func TestLimitsDecide(t *testing.T) {
	day := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	const mb = 1_000_000
	peer := func(l *DeviceLimit) []PeerConfig {
		return []PeerConfig{{Name: "kid", PublicKey: "K", Address: "10.66.66.2", Limit: l}}
	}

	t.Run("under_warn_over", func(t *testing.T) {
		u := newUsage(t, day)
		l := &DeviceLimit{Rules: []LimitRule{{Period: PeriodDay, Bytes: 100 * mb}}}
		u.move("K", 50*mb)
		st, _, ev := u.eval(peer(l), LimitSettings{})
		if st["K"].State != StateOK || st["K"].Rules[0].Used != 50*mb || len(ev) != 0 {
			t.Fatalf("at 50%%: %+v %v", st["K"], ev)
		}
		u.move("K", 30*mb)
		st, _, ev = u.eval(peer(l), LimitSettings{})
		if st["K"].State != StateWarn || len(ev) != 1 || ev[0].kind != EventWarn {
			t.Fatalf("at 80%%: %+v %v", st["K"], ev)
		}
		u.move("K", 25*mb)
		st, _, ev = u.eval(peer(l), LimitSettings{})
		if st["K"].State != StateLimited || st["K"].Action != ActionBlock || len(ev) != 1 || ev[0].kind != EventLimit {
			t.Fatalf("over: %+v %v", st["K"], ev)
		}
		// The event comes once per period.
		u.move("K", mb)
		if _, _, ev = u.eval(peer(l), LimitSettings{}); len(ev) != 0 {
			t.Fatalf("event again: %v", ev)
		}
		want := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC).Unix()
		if r := st["K"].Rules[0]; r.Resets != want {
			t.Fatalf("resets %d, want %d", r.Resets, want)
		}
		// Tomorrow starts over.
		u.t = u.t.Add(14 * time.Hour)
		u.move("K", mb)
		if st, _, _ = u.eval(peer(l), LimitSettings{}); st["K"].State != StateOK || st["K"].Rules[0].Used != mb {
			t.Fatalf("next day: %+v", st["K"])
		}
	})

	t.Run("actions", func(t *testing.T) {
		u := newUsage(t, day)
		u.move("K", 200*mb)
		l := &DeviceLimit{Rules: []LimitRule{{Period: PeriodHour, Bytes: 100 * mb}}}
		if st, _, _ := u.eval(peer(l), LimitSettings{Action: ActionSlow}); st["K"].Action != ActionSlow {
			t.Fatalf("default action: %+v", st["K"])
		}
		l.Action = ActionNotify
		if st, _, _ := u.eval(peer(l), LimitSettings{Action: ActionSlow}); st["K"].Action != ActionNotify {
			t.Fatalf("device's own action: %+v", st["K"])
		}
	})

	t.Run("grants", func(t *testing.T) {
		u := newUsage(t, day)
		u.move("K", 200*mb)
		now := u.t.Unix()
		l := &DeviceLimit{Rules: []LimitRule{{Period: PeriodDay, Bytes: 100 * mb}},
			Grants: []Grant{{ID: "a", Bytes: 50 * mb, Given: now}}}
		st, _, _ := u.eval(peer(l), LimitSettings{})
		if st["K"].State != StateExtended || st["K"].Grants[0].Left != 50*mb {
			t.Fatalf("fresh data grant: %+v", st["K"])
		}
		u.move("K", 30*mb)
		if st, _, _ = u.eval(peer(l), LimitSettings{}); st["K"].Grants[0].Left != 20*mb {
			t.Fatalf("after 30 MB: %+v", st["K"].Grants)
		}
		u.move("K", 30*mb)
		if st, _, _ = u.eval(peer(l), LimitSettings{}); st["K"].State != StateLimited {
			t.Fatalf("grant used up: %+v", st["K"])
		}
		l.Grants = append(l.Grants, Grant{ID: "b", Until: u.t.Unix() + 900, Given: u.t.Unix()})
		if st, _, _ = u.eval(peer(l), LimitSettings{}); st["K"].State != StateExtended {
			t.Fatalf("time grant: %+v", st["K"])
		}
		u.t = u.t.Add(16 * time.Minute)
		if st, _, _ = u.eval(peer(l), LimitSettings{}); st["K"].State != StateLimited {
			t.Fatalf("time grant over: %+v", st["K"])
		}
	})

	t.Run("total_counts_from_start", func(t *testing.T) {
		u := newUsage(t, day)
		u.move("K", 500*mb) // before the limit existed
		start := u.t.Unix()
		l := &DeviceLimit{Rules: []LimitRule{{Period: PeriodTotal, Bytes: 100 * mb, Start: start}}}
		st, _, _ := u.eval(peer(l), LimitSettings{})
		if st["K"].Rules[0].Used != 0 || st["K"].Rules[0].Resets != 0 {
			t.Fatalf("new total limit: %+v", st["K"].Rules)
		}
		u.move("K", 60*mb)
		u.t = u.t.Add(40 * 24 * time.Hour) // never resets
		u.move("K", 60*mb)
		if st, _, _ = u.eval(peer(l), LimitSettings{}); st["K"].Rules[0].Used != 120*mb || st["K"].State != StateLimited {
			t.Fatalf("40 days later: %+v", st["K"])
		}
	})

	t.Run("removed_and_paired_again_keeps_counting", func(t *testing.T) {
		u := newUsage(t, day)
		l := &DeviceLimit{Rules: []LimitRule{{Period: PeriodDay, Bytes: 100 * mb}}}
		u.move("K", 90*mb)
		u.eval(peer(l), LimitSettings{})
		u.eval(nil, LimitSettings{}) // removed
		u.move("X", mb)
		if st, _, _ := u.eval(peer(l), LimitSettings{}); st["K"].Rules[0].Used != 90*mb {
			t.Fatalf("back: %+v", st["K"])
		}
	})

	t.Run("new_key_counts_old_usage", func(t *testing.T) {
		u := newUsage(t, day)
		u.move("OLD", 90*mb)
		u.move("NEW", 5*mb)
		l := &DeviceLimit{Rules: []LimitRule{{Period: PeriodDay, Bytes: 100 * mb}}, Keys: []string{"OLD"}}
		peers := []PeerConfig{{Name: "kid", PublicKey: "NEW", Address: "10.66.66.2", Limit: l}}
		if st, _, _ := u.eval(peers, LimitSettings{}); st["NEW"].Rules[0].Used != 95*mb || st["NEW"].State != StateWarn {
			t.Fatalf("with the old key's usage: %+v", st["NEW"])
		}
	})

	t.Run("reset_usage", func(t *testing.T) {
		u := newUsage(t, day)
		u.move("K", 150*mb)
		l := &DeviceLimit{Rules: []LimitRule{{Period: PeriodDay, Bytes: 100 * mb}}, Reset: u.t.Unix() + 1}
		u.t = u.t.Add(time.Second)
		st, _, _ := u.eval(peer(l), LimitSettings{})
		if st["K"].State != StateOK || st["K"].Rules[0].Used != 0 {
			t.Fatalf("after reset: %+v", st["K"])
		}
		u.move("K", 10*mb)
		if st, _, _ = u.eval(peer(l), LimitSettings{}); st["K"].Rules[0].Used != 10*mb {
			t.Fatalf("after reset + 10 MB: %+v", st["K"])
		}
	})

	t.Run("paused", func(t *testing.T) {
		u := newUsage(t, day)
		st, _, _ := u.eval(peer(&DeviceLimit{Paused: true}), LimitSettings{Action: ActionNotify})
		if st["K"].State != StatePaused || st["K"].Action != ActionBlock {
			t.Fatalf("paused: %+v", st["K"])
		}
	})

	t.Run("cap", func(t *testing.T) {
		u := newUsage(t, day)
		u.move("K", 60*mb)
		u.move("L", 50*mb)
		peers := append(peer(nil), PeerConfig{Name: "dad", PublicKey: "L", Address: "10.66.66.3"})
		ls := LimitSettings{Cap: []LimitRule{{Period: PeriodMonth, Bytes: 100 * mb}}, CapAction: ActionSlow}
		st, capRules, ev := u.eval(peers, ls)
		if !st["K"].Cap || !st["L"].Cap || st["L"].Action != ActionSlow || capRules[0].Used != 110*mb {
			t.Fatalf("cap: %+v %+v", st, capRules)
		}
		if len(ev) != 1 || ev[0].kind != EventCap {
			t.Fatalf("cap events: %v", ev)
		}
		// A device that's also over its own blocking limit is blocked.
		peers[0].Limit = &DeviceLimit{Rules: []LimitRule{{Period: PeriodDay, Bytes: 10 * mb}}}
		if st, _, _ = u.eval(peers, ls); st["K"].Action != ActionBlock {
			t.Fatalf("stricter: %+v", st["K"])
		}
	})
}

// Marks and events survive a restart through the usage file.
func TestLimitsSaved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	st := newStats(path, "UTC", NewLog(10))
	st.record(now, map[string]Rates{"K": {300, 200}}, nil)
	peers := []PeerConfig{{PublicKey: "K", Limit: &DeviceLimit{Rules: []LimitRule{{Period: PeriodTotal, Bytes: 1000, Start: now.Unix()}}}}}
	st.limits(now, peers, LimitSettings{}, true)
	st.record(now.Add(time.Second), map[string]Rates{"K": {600, 500}}, nil)
	if err := st.save(now.Add(time.Second), true); err != nil {
		t.Fatal(err)
	}
	st2 := newStats(path, "UTC", NewLog(10))
	s, _, _ := st2.limits(now.Add(2*time.Second), peers, LimitSettings{}, true)
	if s["K"].Rules[0].Used != 600 {
		t.Fatalf("after restart: %+v", s["K"])
	}
	view := ReadLimits(path, "UTC", peers, LimitSettings{})
	if view.Peers["K"].Rules[0].Used != 600 {
		t.Fatalf("read-only: %+v", view)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"lifetime"`) || !strings.Contains(string(b), `"marks"`) {
		t.Fatalf("file: %s", b)
	}
}

func ipv4Packet(src, dst string, proto uint8, dport uint16, size int) []byte {
	b := make([]byte, size)
	b[0] = 0x45
	b[9] = proto
	copy(b[12:16], netip.MustParseAddr(src).AsSlice())
	copy(b[16:20], netip.MustParseAddr(dst).AsSlice())
	b[22], b[23] = byte(dport>>8), byte(dport)
	return b
}

func TestEnforcement(t *testing.T) {
	s := &Server{gw: netip.MustParseAddr("10.66.66.1")}
	s.udp = newUDPNAT(s)
	peers := []PeerConfig{
		{PublicKey: "A", Address: "10.66.66.2"},
		{PublicKey: "B", Address: "10.66.66.3"},
		{PublicKey: "C", Address: "10.66.66.4"},
	}
	s.applyLimits(map[string]LimitStatus{
		"A": {State: StateLimited, Action: ActionBlock},
		"B": {State: StateLimited, Action: ActionSlow},
		"C": {State: StateLimited, Action: ActionNotify},
	}, peers, LimitSettings{SlowKbps: 800})

	web := ipv4Packet("10.66.66.2", "93.184.216.34", protoTCP, 443, 60)
	if s.allowFrom(web) {
		t.Fatal("blocked device reached the internet")
	}
	if !s.allowFrom(ipv4Packet("10.66.66.2", "10.66.66.1", protoTCP, statusPagePort, 60)) {
		t.Fatal("blocked device can't reach the status page")
	}
	if s.allowFrom(ipv4Packet("10.66.66.2", "10.66.66.1", protoUDP, 53, 60)) {
		t.Fatal("blocked device's DNS went out")
	}
	if s.allowTo(ipv4Packet("93.184.216.34", "10.66.66.2", protoTCP, 50000, 1400)) {
		t.Fatal("traffic reached a blocked device")
	}
	if !s.allowTo(ipv4Packet("10.66.66.1", "10.66.66.2", protoTCP, 50000, 1400)) {
		t.Fatal("status page answer dropped")
	}
	if !s.allowFrom(ipv4Packet("10.66.66.4", "93.184.216.34", protoTCP, 443, 60)) {
		t.Fatal("notify-only device was blocked")
	}
	if !s.allowFrom(ipv4Packet("10.66.66.9", "93.184.216.34", protoTCP, 443, 60)) {
		t.Fatal("unlimited device was blocked")
	}
	// Slowed: 800 kbps = 100 KB/s with a 50 KB burst. A burst of 100 KB of
	// UDP gets about half through; TCP isn't dropped (pacer slows it).
	passed := 0
	for i := 0; i < 100; i++ {
		if s.allowTo(ipv4Packet("93.184.216.34", "10.66.66.3", protoUDP, 50000, 1000)) {
			passed++
		}
	}
	if passed < 40 || passed > 60 {
		t.Fatalf("slowed burst: %d of 100 KB passed", passed)
	}
	if !s.allowTo(ipv4Packet("93.184.216.34", "10.66.66.3", protoTCP, 50000, 1000)) {
		t.Fatal("TCP to a slowed device was dropped instead of paced")
	}
	// The pacer holds TCP to the speed: 150 KB past the burst takes ~1.5 s.
	s.ctx = context.Background()
	start := time.Now()
	pace := s.pacer(netip.MustParseAddr("10.66.66.3"), false)
	for i := 0; i < 20; i++ {
		pace(10_000)
	}
	if el := time.Since(start); el < time.Second || el > 3*time.Second {
		t.Fatalf("200 KB at 100 KB/s after the burst was used took %v", el)
	}
	// IPv6 address of the blocked device too.
	v6, _ := peerAddr6(netip.MustParseAddr("10.66.66.2"))
	if s.enf.lookup(v6) == nil || !s.enf.lookup(v6).block {
		t.Fatal("blocked device's IPv6 address isn't blocked")
	}
	// Unblocked again.
	s.applyLimits(map[string]LimitStatus{}, peers, LimitSettings{})
	if !s.allowFrom(web) {
		t.Fatal("still blocked after the limit went away")
	}

	// Not limited yet, 3000 bytes left: packets stop as soon as they're used,
	// before the next once-a-second decision.
	d := []PeerConfig{{PublicKey: "D", Address: "10.66.66.5", Limit: &DeviceLimit{Rules: []LimitRule{{Period: PeriodDay, Bytes: 10_000}}}}}
	s.applyLimits(map[string]LimitStatus{"D": {State: StateWarn, Rules: []RuleStatus{{Period: PeriodDay, Bytes: 10_000, Used: 7_000}}}}, d, LimitSettings{})
	sent := 0
	for i := 0; i < 10; i++ {
		if s.allowTo(ipv4Packet("93.184.216.34", "10.66.66.5", protoTCP, 50000, 1000)) {
			sent++
		}
	}
	if sent != 3 {
		t.Fatalf("allowance of 3000 bytes let %d KB through", sent)
	}
	if !s.allowFrom(ipv4Packet("10.66.66.5", "10.66.66.1", protoTCP, statusPagePort, 60)) {
		t.Fatal("status page blocked when the allowance ran out")
	}
	// Only notify: no allowance.
	d[0].Limit.Action = ActionNotify
	s.applyLimits(map[string]LimitStatus{"D": {State: StateWarn, Rules: []RuleStatus{{Period: PeriodDay, Bytes: 10_000, Used: 9_999}}}}, d, LimitSettings{})
	if !s.allowTo(ipv4Packet("93.184.216.34", "10.66.66.5", protoTCP, 50000, 1400)) {
		t.Fatal("notify-only device was cut off")
	}
}

func TestTCPCloseOnly(t *testing.T) {
	seg := func(flags byte, payload int) []byte {
		b := ipv4Packet("93.184.216.34", "10.66.66.2", protoTCP, 50000, 40+payload)
		b[2], b[3] = byte((40+payload)>>8), byte(40+payload)
		b[20+12] = 5 << 4
		b[20+13] = flags
		return b
	}
	if !tcpCloseOnly(seg(0x04, 0)) || !tcpCloseOnly(seg(0x11, 0)) {
		t.Fatal("reset or FIN without data not let through")
	}
	if tcpCloseOnly(seg(0x11, 100)) || tcpCloseOnly(seg(0x10, 0)) || tcpCloseOnly(seg(0x18, 500)) {
		t.Fatal("data or plain ACK let through")
	}
}

func TestAsks(t *testing.T) {
	var b askBook
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	a, why := b.add(Ask{Key: "K", Name: "kid", Minutes: 15}, now)
	if why != "" || a.ID == "" || a.State != AskPending {
		t.Fatalf("first: %+v %q", a, why)
	}
	if _, why = b.add(Ask{Key: "K", Minutes: 60}, now); why == "" {
		t.Fatal("second while one is pending")
	}
	if !b.answer(a.ID, AskDenied, now.Add(time.Minute)) {
		t.Fatal("deny")
	}
	if _, why = b.add(Ask{Key: "K", Minutes: 60}, now.Add(2*time.Minute)); why == "" {
		t.Fatal("asked again right after a no")
	}
	a2, why := b.add(Ask{Key: "K", Minutes: 60}, now.Add(12*time.Minute))
	if why != "" {
		t.Fatalf("after the cooldown: %q", why)
	}
	if p := b.pending(now.Add(13 * time.Minute)); len(p) != 1 || p[0].ID != a2.ID {
		t.Fatalf("pending: %+v", p)
	}
	if p := b.pending(now.Add(3 * time.Hour)); len(p) != 0 {
		t.Fatalf("unanswered requests should expire: %+v", p)
	}
	if got := cleanMessage("  please\n\n  I need   it  "); got != "please I need it" {
		t.Fatalf("cleanMessage %q", got)
	}
}

// A device that uses up its limit through the tunnel loses the internet,
// still sees the status page, asks for more, and gets back on when the app
// approves.
func TestLimitEndToEnd(t *testing.T) {
	host := hostIPv4(t)
	serverPriv, _ := GenerateKey()
	serverPub, _ := PublicKey(serverPriv)
	clientPriv, _ := GenerateKey()
	clientPub, _ := PublicKey(clientPriv)
	log := NewLog(400)
	limit := &DeviceLimit{Rules: []LimitRule{{Period: PeriodDay, Bytes: 1_000_000}}}
	peer := PeerConfig{Name: "kid", PublicKey: clientPub, Address: "10.66.66.2", Limit: limit}
	srv, err := Start(Config{PrivateKey: serverPriv, ListenPort: 51989, ConnectPort: 18089, Peers: []PeerConfig{peer}}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	c := startClient(t, clientPriv, serverPub, netip.MustParseAddr("10.66.66.2"), 51989)

	blob := make([]byte, 2_000_000)
	ln, err := net.Listen("tcp4", netip.AddrPortFrom(host, 0).String())
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(blob) })}
	go hs.Serve(ln)
	defer hs.Close()
	internet := "http://" + ln.Addr().String() + "/"

	resp, err := c.http.Get(internet)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	// The allowance stops it near 1 MB, not a second's worth later.
	if got >= int64(len(blob)) || got > 1_200_000 {
		t.Fatalf("got %d bytes past a 1 MB limit", got)
	}

	// Within a couple of stats ticks the device is limited and blocked.
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, _ := srv.limitStatus(clientPub)
		if st.State == StateLimited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never limited: %+v", st)
		}
		time.Sleep(200 * time.Millisecond)
	}
	short := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: c.net.DialContext, DisableKeepAlives: true}}
	if resp, err := short.Get(internet); err == nil {
		resp.Body.Close()
		t.Fatal("blocked device still reached the internet")
	}
	ev := srv.events.after(0)
	if len(ev.Events) == 0 || ev.Events[len(ev.Events)-1].Kind != EventLimit {
		t.Fatalf("events: %+v", ev)
	}

	// The status page still works and says why.
	resp, err = c.http.Get("http://10.66.66.1/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "Limit reached") || !strings.Contains(string(page), "kid") {
		t.Fatalf("status page:\n%s", page)
	}
	resp, err = c.http.Get("http://10.66.66.1/status.json")
	if err != nil {
		t.Fatal(err)
	}
	var view DeviceView
	json.NewDecoder(resp.Body).Decode(&view)
	resp.Body.Close()
	if view.Limit.State != StateLimited || view.Name != "kid" {
		t.Fatalf("status.json: %+v", view)
	}

	// Ask for 15 more minutes.
	resp, err = c.http.PostForm("http://10.66.66.1/ask", url.Values{"what": {"15m"}, "message": {"homework"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	asks := srv.Status().Asks
	if len(asks) != 1 || asks[0].Minutes != 15 || asks[0].Message != "homework" || asks[0].Key != clientPub {
		t.Fatalf("asks: %+v", asks)
	}

	// The app approves: a grant that answers the request.
	limit2 := *limit
	limit2.Grants = []Grant{{ID: "g1", Until: time.Now().Add(15 * time.Minute).Unix(), Given: time.Now().Unix(), Ask: asks[0].ID}}
	peer.Limit = &limit2
	if err := srv.SetPeers([]PeerConfig{peer}); err != nil {
		t.Fatal(err)
	}
	if st, _ := srv.limitStatus(clientPub); st.State != StateExtended {
		t.Fatalf("after approval: %+v", st)
	}
	if len(srv.Status().Asks) != 0 {
		t.Fatal("request still pending after approval")
	}
	resp, err = c.http.Get(internet)
	if err != nil {
		t.Fatalf("after approval: %v", err)
	}
	resp.Body.Close()
	fmt.Fprint(io.Discard, resp.StatusCode)
}

// Lots of traffic is saved within seconds, not once a minute.
func TestBusyUsageSavedSooner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	st := newStats(path, "UTC", NewLog(10))
	st.save(now, true) // nothing yet: no file
	st.saved = now
	st.record(now.Add(time.Second), map[string]Rates{"K": {1000, 1000}}, nil)
	st.save(now.Add(6*time.Second), false)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a little traffic was saved before a minute")
	}
	st.record(now.Add(7*time.Second), map[string]Rates{"K": {9_000_000, 3_000_000}}, nil)
	st.save(now.Add(7*time.Second), false)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("12 MB wasn't saved within seconds")
	}
}

// A slowed device's download runs near its speed, without stalls.
func TestSlowEndToEnd(t *testing.T) {
	if raceEnabled {
		t.Skip("timing")
	}
	host := hostIPv4(t)
	serverPriv, _ := GenerateKey()
	serverPub, _ := PublicKey(serverPriv)
	clientPriv, _ := GenerateKey()
	clientPub, _ := PublicKey(clientPriv)
	// Already over a tiny limit, slowed to 8 Mbps (1 MB/s).
	limit := &DeviceLimit{Rules: []LimitRule{{Period: PeriodTotal, Bytes: 1, Start: 1}}, Action: ActionSlow, SlowKbps: 8000}
	srv, err := Start(Config{PrivateKey: serverPriv, ListenPort: 51988, ConnectPort: 18088,
		Peers: []PeerConfig{{Name: "kid", PublicKey: clientPub, Address: "10.66.66.2", Limit: limit}}}, NewLog(200))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	c := startClient(t, clientPriv, serverPub, netip.MustParseAddr("10.66.66.2"), 51988)
	blob := make([]byte, 3_000_000)
	ln, err := net.Listen("tcp4", netip.AddrPortFrom(host, 0).String())
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(blob) })}
	go hs.Serve(ln)
	defer hs.Close()
	// Let the first decision mark it slowed (usage > 1 byte after the handshake).
	resp, err := c.http.Get("http://" + ln.Addr().String() + "/")
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, _ := srv.limitStatus(clientPub); st.State == StateLimited && st.Action == ActionSlow {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never slowed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	start := time.Now()
	resp, err = c.http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	el := time.Since(start)
	mbps := float64(n) * 8 / el.Seconds() / 1e6
	t.Logf("%d bytes in %v: %.1f Mbps (set to 8)", n, el, mbps)
	if n != int64(len(blob)) || mbps < 5 || mbps > 11 {
		t.Fatalf("slowed download: %d bytes at %.1f Mbps, want about 8", n, mbps)
	}
}
