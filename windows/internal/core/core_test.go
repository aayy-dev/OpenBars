package core

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aayy-dev/OpenBars/windows/internal/applog"
	"github.com/aayy-dev/OpenBars/windows/internal/hotspot"
	"github.com/aayy-dev/OpenBars/windows/internal/phone"
	"github.com/aayy-dev/OpenBars/windows/internal/state"
)

type fakeNet struct {
	mu     sync.Mutex
	ifaces []hotspot.Iface
}

func (f *fakeNet) Snapshot() ([]hotspot.Iface, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hotspot.Iface(nil), f.ifaces...), nil
}
func (f *fakeNet) set(i ...hotspot.Iface) { f.mu.Lock(); f.ifaces = i; f.mu.Unlock() }

type fakePhone struct {
	mu       sync.Mutex
	pairing  phone.Pairing
	known    map[string]bool
	checkErr error
	checks   int
}

func (f *fakePhone) Info(context.Context) (phone.Info, error) {
	return phone.Info{HotspotName: "Aayush’s iPhone", Pairing: true, WireGuardPort: 51820}, nil
}
func (f *fakePhone) Pair(_ context.Context, name, kind, pub string) (phone.Pairing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.known[pub] = true
	p := f.pairing
	p.DeviceName = name
	return p, nil
}
func (f *fakePhone) Check(_ context.Context, pub string) (phone.Pairing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if f.checkErr != nil {
		return phone.Pairing{}, f.checkErr
	}
	if !f.known[pub] {
		return phone.Pairing{}, phone.ErrNotPaired
	}
	return f.pairing, nil
}

type fakeTunnel struct {
	running   bool
	cfg       TunnelConfig
	handshake int64
	ups       int
	boundTo   uint32
}

func (t *fakeTunnel) Up(c TunnelConfig) error {
	t.running, t.cfg, t.ups, t.boundTo = true, c, t.ups+1, c.Hotspot.Index
	return nil
}
func (t *fakeTunnel) Down() error           { t.running, t.handshake = false, 0; return nil }
func (t *fakeTunnel) Running() bool         { return t.running }
func (t *fakeTunnel) LUID() uint64          { return 0 }
func (t *fakeTunnel) BindTo(i uint32) error { t.boundTo = i; return nil }
func (t *fakeTunnel) Stats() TunnelStats {
	return TunnelStats{Endpoint: "172.20.10.1:51820", LastHandshake: t.handshake, Since: 1}
}

var (
	home = hotspot.Iface{Index: 5, Name: "Ethernet", Up: true,
		Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.11")}, Gateways: []netip.Addr{netip.MustParseAddr("192.168.1.1")}}
	wifi = hotspot.Iface{Index: 9, Name: "Wi-Fi", Wireless: true, Up: true,
		Addrs: []netip.Addr{netip.MustParseAddr("172.20.10.4")}, Gateways: []netip.Addr{hotspot.Gateway}}
)

type rig struct {
	c     *Controller
	net   *fakeNet
	phone *fakePhone
	tun   *fakeTunnel
	clock time.Time
}

func newRig(t *testing.T) *rig {
	r := &rig{net: &fakeNet{}, tun: &fakeTunnel{}, clock: time.Unix(1_800_000_000, 0)}
	r.phone = &fakePhone{known: map[string]bool{}, pairing: phone.Pairing{
		ServerPublicKey: "c2VydmVy", Endpoint: "172.20.10.1:51820", Address: "10.66.66.5",
		Address6: "fd0b:a75::a42:4205", DNS: "10.66.66.1", MTU: 1280, HotspotName: "Aayush’s iPhone"}}
	r.c = New(Config{
		StatePath: filepath.Join(t.TempDir(), "state.json"), Sealer: state.Plain{},
		Log: applog.NewRing(500), Phone: r.phone, Net: r.net, Tunnel: r.tun,
		DeviceName: "AAYUSH-PC", About: "Open Bars test", Now: func() time.Time { return r.clock },
	})
	return r
}

func (r *rig) step(d time.Duration) {
	r.clock = r.clock.Add(d)
	r.c.evaluate(context.Background())
}

func (r *rig) logText() string {
	var b strings.Builder
	for _, e := range r.c.Logs(0).Entries {
		b.WriteString(applog.Line(e, time.UTC) + "\n")
	}
	return b.String()
}

func TestPairThenConnectOnHotspot(t *testing.T) {
	r := newRig(t)
	r.net.set(home)
	r.step(0)
	if got := r.c.Status().Phase; got != Unpaired {
		t.Fatalf("phase %s, want unpaired", got)
	}
	if _, _, err := r.c.Pair(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.step(0)
	if s := r.c.Status(); s.Phase != NoHotspot || !s.Paired || !s.AutoConnect {
		t.Fatalf("after pairing: %+v", s)
	}
	r.net.set(home, wifi)
	r.step(time.Second)
	if !r.tun.running || r.tun.cfg.Hotspot.Index != 9 || r.tun.cfg.Pairing.DNS != "10.66.66.1" {
		t.Fatalf("tunnel not up as expected: %+v", r.tun)
	}
	if got := r.c.Status().Phase; got != Connecting {
		t.Fatalf("phase %s, want connecting", got)
	}
	r.tun.handshake = r.clock.Unix()
	r.step(time.Second)
	if got := r.c.Status().Phase; got != Connected {
		t.Fatalf("phase %s, want connected", got)
	}
	// Leaving the hotspot takes the tunnel down.
	r.net.set(home)
	r.step(time.Second)
	if r.tun.running || r.c.Status().Phase != NoHotspot {
		t.Fatalf("still up after leaving: %+v", r.c.Status())
	}
	log := r.logText()
	for _, want := range []string{"Paired as", "Joined an iPhone hotspot", "Tunnel up to", "first handshake", "Left the iPhone hotspot", "Tunnel down (left the hotspot)"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

func TestNotSharingBacksOff(t *testing.T) {
	r := newRig(t)
	r.c.Pair(context.Background())
	r.net.set(wifi)
	r.phone.checkErr = &phone.UnreachableError{Reason: "connection refused", Err: errors.New("x")}
	r.step(0)
	s := r.c.Status()
	if s.Phase != NotSharing || s.Hint == "" || r.tun.running {
		t.Fatalf("status %+v", s)
	}
	checks := r.phone.checks
	r.step(2 * time.Second) // inside the 5 s backoff: no new check
	if r.phone.checks != checks {
		t.Fatalf("checked again during backoff")
	}
	r.phone.checkErr = nil
	r.step(4 * time.Second)
	if !r.tun.running {
		t.Fatalf("didn't connect after the iPhone came back: %+v", r.c.Status())
	}
}

func TestDifferentIPhone(t *testing.T) {
	r := newRig(t)
	r.c.Pair(context.Background())
	r.phone.known = map[string]bool{} // this iPhone doesn't know us
	r.net.set(wifi)
	r.step(0)
	if s := r.c.Status(); s.Phase != NotThisPhone || r.tun.running {
		t.Fatalf("status %+v", s)
	}
}

func TestDisconnectHoldsUntilRejoin(t *testing.T) {
	r := newRig(t)
	r.c.Pair(context.Background())
	r.net.set(wifi)
	r.step(0)
	r.c.Disconnect()
	r.step(time.Second)
	if r.tun.running || r.c.Status().Phase != Off {
		t.Fatalf("still connected after Disconnect: %+v", r.c.Status())
	}
	if !strings.Contains(r.logText(), "Tunnel down (you disconnected)") {
		t.Fatalf("wrong reason:\n%s", r.logText())
	}
	r.step(time.Minute)
	if r.tun.running {
		t.Fatal("reconnected while still on the same hotspot")
	}
	r.net.set(home)
	r.step(time.Second)
	r.net.set(wifi)
	r.step(time.Second)
	if !r.tun.running {
		t.Fatal("didn't reconnect after rejoining")
	}
}

func TestManualConnectWhenAutoOff(t *testing.T) {
	r := newRig(t)
	r.c.Pair(context.Background())
	r.c.SetAutoConnect(false)
	r.net.set(wifi)
	r.step(0)
	if r.tun.running {
		t.Fatal("connected with automatic connect off")
	}
	r.c.ConnectNow()
	r.step(time.Second)
	if !r.tun.running {
		t.Fatal("Connect now didn't connect")
	}
}

func TestSettingsChangeIsSaved(t *testing.T) {
	r := newRig(t)
	r.c.Pair(context.Background())
	r.phone.pairing.Address = "10.66.66.9"
	r.net.set(wifi)
	r.step(0)
	if r.tun.cfg.Pairing.Address != "10.66.66.9" {
		t.Fatalf("tunnel used old settings: %+v", r.tun.cfg.Pairing)
	}
	st, _ := state.Load(r.c.cfg.StatePath, state.Plain{})
	if st.Pairing == nil || st.Pairing.Address != "10.66.66.9" {
		t.Fatalf("new settings not saved: %+v", st.Pairing)
	}
}

func TestNoHandshakeWarnsOnceAndQuietRechecks(t *testing.T) {
	r := newRig(t)
	r.c.Pair(context.Background())
	r.net.set(wifi)
	r.step(0)
	r.step(31 * time.Second)
	r.step(31 * time.Second)
	if n := strings.Count(r.logText(), "No handshake with the iPhone"); n != 1 {
		t.Fatalf("handshake warning logged %d times", n)
	}
	r.tun.handshake = r.clock.Unix()
	r.step(time.Second)
	r.phone.checkErr = &phone.UnreachableError{Reason: "timed out", Err: errors.New("x")}
	r.step(4 * time.Minute)
	if r.tun.running {
		t.Fatal("tunnel kept running after the iPhone stopped sharing")
	}
}

func TestDetailedTurnsItselfOff(t *testing.T) {
	r := newRig(t)
	r.c.SetDetailed(true)
	if r.c.Status().DetailedUntil == 0 || !r.c.log.Detailed() {
		t.Fatal("detailed logging not on")
	}
	r.net.set(home)
	r.step(31 * time.Minute)
	if r.c.Status().DetailedUntil != 0 || r.c.log.Detailed() {
		t.Fatal("detailed logging didn't turn off")
	}
}

func TestMovesToNewAdapter(t *testing.T) {
	r := newRig(t)
	r.c.Pair(context.Background())
	r.net.set(wifi)
	r.step(0)
	usb := hotspot.Iface{Index: 3, Name: "Apple Mobile Device Ethernet", Up: true,
		Addrs: []netip.Addr{netip.MustParseAddr("172.20.10.6")}, Gateways: []netip.Addr{hotspot.Gateway}}
	r.net.set(usb)
	r.step(time.Second)
	if !r.tun.running || r.tun.boundTo != 3 || r.tun.ups != 1 {
		t.Fatalf("tunnel %+v", r.tun)
	}
}

func TestDiagnose(t *testing.T) {
	r := newRig(t)
	r.net.set(home)
	got := r.c.Diagnose(context.Background())
	if len(got) != 1 || got[0].State != "failed" || got[0].Hint == "" {
		t.Fatalf("off hotspot: %+v", got)
	}
	r.net.set(wifi)
	got = r.c.Diagnose(context.Background())
	if last := got[len(got)-1]; last.ID != "paired" || last.Action != "pair" {
		t.Fatalf("unpaired: %+v", got)
	}
	r.c.Pair(context.Background())
	r.step(0)
	r.tun.handshake = r.clock.Unix()
	got = r.c.Diagnose(context.Background())
	var ids []string
	for _, ch := range got {
		ids = append(ids, ch.ID+"="+ch.State)
	}
	if strings.Join(ids, " ") != "hotspot=ok sharing=ok paired=ok auto=ok tunnel=ok" {
		t.Fatalf("connected: %v", ids)
	}
}
