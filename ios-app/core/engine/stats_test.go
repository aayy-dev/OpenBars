package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatsRecord(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	path := filepath.Join(t.TempDir(), "usage.json")
	st := newStats(path, "America/Chicago", NewLog(10))
	if st.loc.String() != loc.String() {
		t.Fatalf("zone %v", st.loc)
	}
	t0 := time.Date(2026, 11, 1, 23, 59, 58, 0, loc)
	names := map[string]string{"A": "MacBook", "B": "PC"}

	st.record(t0, map[string]Rates{"A": {1000, 100}, "B": {0, 0}}, names)
	st.record(t0.Add(time.Second), map[string]Rates{"A": {3000, 300}, "B": {500, 50}}, names)
	// Midnight: a new day and hour. B restarted its counters (re-added).
	st.record(t0.Add(2*time.Second), map[string]Rates{"A": {3000, 300}, "B": {200, 20}}, names)

	live := st.since(0)
	if len(live) != 3 {
		t.Fatalf("live: %d samples", len(live))
	}
	if live[1].Down != 2500 || live[1].Up != 250 || live[1].Peers["A"] != (Rates{2000, 200}) {
		t.Fatalf("second sample %+v", live[1])
	}
	if live[2].Peers["B"] != (Rates{200, 20}) || live[2].Peers["A"] != (Rates{}) && len(live[2].Peers) != 1 {
		t.Fatalf("reset sample %+v", live[2])
	}
	if got := st.since(live[1].T); len(got) != 1 {
		t.Fatalf("since: %d", len(got))
	}

	v := st.view(t0.Add(3*time.Second), 30)
	if len(v.Days) != 2 || v.Days[0].Date != "2026-11-01" || v.Days[1].Date != "2026-11-02" {
		t.Fatalf("days %+v", v.Days)
	}
	if v.Days[0].Down != 3500 || v.Days[0].Peers["A"] != (Rates{3000, 300}) || v.Days[0].Peers["B"] != (Rates{500, 50}) {
		t.Fatalf("day 1 %+v", v.Days[0])
	}
	if v.Days[1].Down != 200 {
		t.Fatalf("day 2 %+v", v.Days[1])
	}
	if len(v.Hours) != 2 || v.Hours[0].Start != time.Date(2026, 11, 1, 23, 0, 0, 0, loc).Unix() {
		t.Fatalf("hours %+v", v.Hours)
	}
	if v.Names["B"] != "PC" {
		t.Fatalf("names %+v", v.Names)
	}

	// Saved and read back (ReadUsage is what the app uses while stopped).
	if err := st.save(t0.Add(3*time.Second), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	back := newStats(path, "America/Chicago", NewLog(10)).view(t0.Add(3*time.Second), 30)
	if len(back.Days) != 2 || back.Days[0].Down != 3500 || back.Names["A"] != "MacBook" {
		t.Fatalf("read back %+v", back)
	}
	// Nothing changed since: no rewrite.
	st.saved = time.Time{}
	os.Remove(path)
	if err := st.save(t0.Add(4*time.Second), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("saved although nothing changed")
	}
}

func TestStatsRetention(t *testing.T) {
	st := newStats("", "UTC", NewLog(10))
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < liveSeconds+50; i++ {
		st.record(now.Add(time.Duration(i)*time.Second), map[string]Rates{"A": {int64(i), 0}}, nil)
	}
	if n := len(st.since(0)); n != liveSeconds {
		t.Fatalf("live kept %d", n)
	}
	// An hour 8 days ago and a day 401 days ago are dropped.
	st.usage.Hours["1"] = &UsageBucket{Down: 1}
	st.usage.Days["2025-01-01"] = &UsageBucket{Down: 1}
	v := st.view(now, 0)
	for _, h := range v.Hours {
		if h.Start == 1 {
			t.Fatal("old hour kept")
		}
	}
	for _, d := range v.Days {
		if d.Date == "2025-01-01" {
			t.Fatal("old day kept")
		}
	}
}

// End to end: a device's traffic through the server shows up in History.
func TestServerHistory(t *testing.T) {
	priv, _ := GenerateKey()
	srv, err := Start(Config{PrivateKey: priv, ListenPort: 51992, UsageFile: filepath.Join(t.TempDir(), "u.json")}, NewLog(10))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	time.Sleep(2500 * time.Millisecond)
	if n := len(srv.History(0)); n < 2 {
		t.Fatalf("history has %d samples after 2.5 s", n)
	}
	if v := srv.Usage(7); v.Days == nil || v.Hours == nil {
		t.Fatalf("usage view %+v", v)
	}
}

func TestLastSeenSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	st := newStats(path, "UTC", NewLog(16))
	t0 := time.Unix(1_800_000_000, 0)
	// Sent to the device only: not "seen" (it may be gone).
	st.record(t0, map[string]Rates{"k": {500, 0}}, map[string]string{"k": "PC"})
	if got := st.lastSeen("k"); got != 0 {
		t.Fatalf("seen after send-only traffic: %d", got)
	}
	// A keepalive from the device.
	st.record(t0.Add(25*time.Second), map[string]Rates{"k": {500, 32}}, nil)
	if got := st.lastSeen("k"); got != t0.Add(25*time.Second).Unix() {
		t.Fatalf("lastSeen = %d", got)
	}
	if err := st.save(t0.Add(26*time.Second), true); err != nil {
		t.Fatal(err)
	}
	again := newStats(path, "UTC", NewLog(16))
	if got := again.lastSeen("k"); got != t0.Add(25*time.Second).Unix() {
		t.Fatalf("after restart lastSeen = %d", got)
	}
	if v := again.view(t0.Add(30*time.Second), 7); v.Seen["k"] != t0.Add(25*time.Second).Unix() {
		t.Fatalf("view.Seen = %v", v.Seen)
	}
}
