// Package core is Open Bars Connect's brain on Windows: it notices when the
// PC is on the paired iPhone's hotspot, checks with Open Bars there, and
// brings the tunnel up or down. Everything platform-specific (adapters, the
// tunnel, probes) comes in through interfaces so the logic is testable.
package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aayy-dev/OpenBars/windows/internal/applog"
	"github.com/aayy-dev/OpenBars/windows/internal/hotspot"
	"github.com/aayy-dev/OpenBars/windows/internal/phone"
	"github.com/aayy-dev/OpenBars/windows/internal/state"
	"github.com/aayy-dev/OpenBars/windows/internal/wgkey"
)

// Phone is Open Bars on the iPhone.
type Phone interface {
	Info(ctx context.Context) (phone.Info, error)
	Pair(ctx context.Context, name, kind, publicKey string) (phone.Pairing, error)
	Check(ctx context.Context, publicKey string) (phone.Pairing, error)
}

// Network lists the PC's adapters.
type Network interface {
	Snapshot() ([]hotspot.Iface, error)
}

// TunnelConfig is everything needed to bring the tunnel up.
type TunnelConfig struct {
	PrivateKey string
	Pairing    phone.Pairing
	Hotspot    hotspot.Iface // the adapter the encrypted traffic must leave through
}

// TunnelStats describes a running tunnel.
type TunnelStats struct {
	Endpoint      string `json:"endpoint"`
	LastHandshake int64  `json:"last_handshake"` // Unix seconds, 0 = none yet
	RX            uint64 `json:"rx"`
	TX            uint64 `json:"tx"`
	Since         int64  `json:"since"` // Unix seconds
}

// Tunnel is the WireGuard tunnel.
type Tunnel interface {
	Up(TunnelConfig) error
	Down() error
	Running() bool
	Stats() TunnelStats
	LUID() uint64              // the tunnel's own adapter, 0 when down
	BindTo(index uint32) error // send encrypted traffic out this adapter
}

// Prober checks that DNS and the internet work through the tunnel.
type Prober interface {
	DNS(ctx context.Context, server string) error
	Internet(ctx context.Context) error
}

// Config wires a Controller up.
type Config struct {
	StatePath  string
	Sealer     state.Sealer
	Log        *applog.Ring
	ClearLog   func() // also empties the saved log
	Phone      Phone
	Net        Network
	Tunnel     Tunnel
	Probe      Prober
	DeviceName string // what the iPhone shows, e.g. the computer name
	About      string // "Open Bars 1.0.0 (3) on Windows 11 Pro 25H2 (26200)"
	Now        func() time.Time
}

// Phase is where the controller is, for the UI.
type Phase string

const (
	Unpaired     Phase = "unpaired"
	NoHotspot    Phase = "no_hotspot"
	Off          Phase = "off"
	Checking     Phase = "checking"
	NotSharing   Phase = "not_sharing"
	NotThisPhone Phase = "not_this_phone"
	Connecting   Phase = "connecting"
	Connected    Phase = "connected"
	Problem      Phase = "problem"
)

// Status is what the tray and window show.
type Status struct {
	Phase         Phase        `json:"phase"`
	Title         string       `json:"title"`
	Detail        string       `json:"detail"`
	Hint          string       `json:"hint,omitempty"`
	Paired        bool         `json:"paired"`
	DeviceName    string       `json:"device_name"`
	PhoneName     string       `json:"phone_name"`
	Address       string       `json:"address"`
	Address6      string       `json:"address6"`
	Endpoint      string       `json:"endpoint"`
	AutoConnect   bool         `json:"auto_connect"`
	ManualConnect bool         `json:"manual_connect"`
	Hotspot       string       `json:"hotspot"`
	Tunnel        *TunnelStats `json:"tunnel,omitempty"`
	Pairing       bool         `json:"pairing"`
	DetailedUntil int64        `json:"detailed_until"` // Unix ms, 0 = off
	About         string       `json:"about"`
}

// Timings (variables so tests can shrink them).
var (
	tick              = 3 * time.Second
	handshakeGrace    = 30 * time.Second
	quietAfter        = 3 * time.Minute
	recheckQuietEvery = 30 * time.Second
	detailedFor       = 30 * time.Minute
)

// Controller runs the show.
type Controller struct {
	cfg  Config
	log  *applog.Ring
	now  func() time.Time
	wake chan struct{}

	mu            sync.Mutex
	st            state.State
	status        Status
	manual        bool // "Connect now" while automatic connect is off
	userOff       bool // "Disconnect" while automatic connect is on; cleared when the hotspot goes away
	pairing       bool
	detailedUntil time.Time

	failures int       // failed attempts since the last success, for backoff
	nextTry  time.Time // don't ask the iPhone again before this

	// Only touched by the loop goroutine.
	hotspotSeen    hotspot.Iface
	onHotspot      bool
	upAt           time.Time
	boundIndex     uint32
	handshakeSeen  bool
	handshakeWarn  bool
	quietWarned    bool
	lastQuietCheck time.Time
	lastSnapErr    string
}

// New loads saved state and returns a controller (call Run to start it).
func New(cfg Config) *Controller {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	c := &Controller{cfg: cfg, log: cfg.Log, now: cfg.Now, wake: make(chan struct{}, 1)}
	st, err := state.Load(cfg.StatePath, cfg.Sealer)
	if err != nil {
		c.log.Add(applog.Warn, "svc", "Couldn't read the saved pairing: "+err.Error(), "Pair with the iPhone again.")
	}
	c.st = st
	c.status = Status{Phase: Unpaired}
	return c
}

// Run evaluates the situation every few seconds, and right away whenever
// something changes, until ctx ends. It brings the tunnel down on return.
func (c *Controller) Run(ctx context.Context) {
	c.mu.Lock()
	paired := c.st.Paired()
	phoneName := c.phoneNameLocked()
	auto := c.st.AutoConnect
	c.mu.Unlock()
	if paired {
		c.log.Infof("svc", "%s; paired with %s, automatic connect %s", c.cfg.About, phoneName, onOff(auto))
	} else {
		c.log.Infof("svc", "%s; not paired yet", c.cfg.About)
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		c.evaluate(ctx)
		select {
		case <-ctx.Done():
			c.down("Open Bars is stopping")
			return
		case <-t.C:
		case <-c.wake:
		}
	}
}

// Poke makes the loop look again now (e.g. on a network change).
func (c *Controller) Poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Status returns the current status.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.status
	s.Paired = c.st.Paired()
	s.AutoConnect = c.st.AutoConnect
	s.ManualConnect = c.manual
	s.Pairing = c.pairing
	s.DeviceName = c.cfg.DeviceName
	s.About = c.cfg.About
	if p := c.st.Pairing; p != nil {
		s.PhoneName, s.Address, s.Address6, s.Endpoint = c.phonePlainLocked(), p.Address, p.Address6, p.Endpoint
		if p.DeviceName != "" {
			s.DeviceName = p.DeviceName
		}
	}
	if !c.detailedUntil.IsZero() {
		s.DetailedUntil = c.detailedUntil.UnixMilli()
	}
	if c.cfg.Tunnel.Running() {
		ts := c.cfg.Tunnel.Stats()
		s.Tunnel = &ts
	}
	return s
}

// SetAutoConnect turns automatic connecting on or off.
func (c *Controller) SetAutoConnect(on bool) error {
	c.mu.Lock()
	c.st.AutoConnect = on
	c.userOff, c.manual = false, false
	err := c.saveLocked()
	c.mu.Unlock()
	c.log.Infof("app", "Automatic connect turned %s", onOff(on))
	c.Poke()
	return err
}

// ConnectNow connects while on the hotspot even if automatic connect is off.
func (c *Controller) ConnectNow() {
	c.mu.Lock()
	c.manual, c.userOff = true, false
	c.nextTry = time.Time{}
	c.failures = 0
	c.mu.Unlock()
	c.log.Infof("app", "Connect now")
	c.Poke()
}

// Disconnect stays disconnected until the PC leaves and rejoins the hotspot.
func (c *Controller) Disconnect() {
	c.mu.Lock()
	c.manual, c.userOff = false, true
	c.mu.Unlock()
	c.log.Infof("app", "Disconnected by you; reconnects next time this PC joins the hotspot")
	c.Poke()
}

// Forget removes the pairing (the key stays, so pairing again keeps the
// same identity on the iPhone if it still has this PC).
func (c *Controller) Forget() error {
	c.mu.Lock()
	c.st.Pairing = nil
	c.manual, c.userOff = false, false
	err := c.saveLocked()
	c.mu.Unlock()
	c.log.Infof("app", "Forgot the iPhone. To remove this PC there too: Share › the device › Remove Device.")
	c.Poke()
	return err
}

// SetDetailed turns detailed (debug) logging on for 30 minutes, or off.
func (c *Controller) SetDetailed(on bool) {
	c.mu.Lock()
	if on {
		c.detailedUntil = c.now().Add(detailedFor)
	} else {
		c.detailedUntil = time.Time{}
	}
	c.mu.Unlock()
	c.log.SetDetailed(on)
	if on {
		c.log.Infof("app", "Detailed logging on for %d minutes (includes the addresses this PC connects to)", int(detailedFor/time.Minute))
	} else {
		c.log.Infof("app", "Detailed logging off")
	}
}

// Logs returns log entries after seq.
func (c *Controller) Logs(after int64) applog.Page { return c.log.Since(after) }

// ClearLogs empties the log.
func (c *Controller) ClearLogs() {
	c.log.Clear()
	if c.cfg.ClearLog != nil {
		c.cfg.ClearLog()
	}
}

// Pair asks the iPhone to add this PC and waits for someone to tap Allow.
// It returns a message for the person (and a hint when it failed).
func (c *Controller) Pair(ctx context.Context) (msg, hint string, err error) {
	c.mu.Lock()
	if c.pairing {
		c.mu.Unlock()
		return "", "", errors.New("already pairing")
	}
	c.pairing = true
	if c.st.PrivateKey == "" {
		k, err := wgkey.Generate()
		if err != nil {
			c.pairing = false
			c.mu.Unlock()
			return "", "", err
		}
		c.st.PrivateKey = k
		c.saveLocked()
	}
	priv := c.st.PrivateKey
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.pairing = false
		c.mu.Unlock()
		c.Poke()
	}()

	pub, _ := wgkey.Public(priv)
	info, err := c.cfg.Phone.Info(ctx)
	if err != nil {
		msg, hint := describePhoneErr(err)
		c.log.Add(applog.Warn, "pair", "Can't pair: "+msg, hint)
		return "", hint, errors.New(msg)
	}
	if !info.Pairing {
		c.log.Add(applog.Warn, "pair", "Can't pair: Open Bars on the iPhone is too old", "Update Open Bars on the iPhone.")
		return "", "Update Open Bars on the iPhone.", phone.ErrOldIPhone
	}
	c.log.Infof("pair", "Asking %q to allow %q (key %s); tap Allow on the iPhone", info.HotspotName, c.cfg.DeviceName, wgkey.Short(pub))
	p, err := c.cfg.Phone.Pair(ctx, c.cfg.DeviceName, "windows", pub)
	if err != nil {
		msg, hint := describePhoneErr(err)
		c.log.Add(applog.Warn, "pair", "Pairing failed: "+msg, hint)
		return "", hint, errors.New(msg)
	}
	c.mu.Lock()
	c.st.Pairing = &p
	c.st.AutoConnect = true
	c.manual, c.userOff = false, false
	c.failures, c.nextTry = 0, time.Time{}
	err = c.saveLocked()
	c.mu.Unlock()
	addr := p.Address
	if p.Address6 != "" {
		addr += ", " + p.Address6
	}
	c.log.Infof("pair", "Paired as %q with %s: tunnel address %s, iPhone at %s", p.DeviceName, phoneLabel(p), addr, p.Endpoint)
	return fmt.Sprintf("Paired with %s.", phonePlain(p)), "", err
}

// evaluate looks at the network and decides what the tunnel should do.
func (c *Controller) evaluate(ctx context.Context) {
	now := c.now()
	c.expireDetailed(now)

	ifaces, err := c.cfg.Net.Snapshot()
	if err != nil {
		if err.Error() != c.lastSnapErr {
			c.log.Add(applog.Warn, "net", "Couldn't read the network adapters: "+err.Error(), "")
			c.lastSnapErr = err.Error()
		}
		return
	}
	c.lastSnapErr = ""
	hs, on := hotspot.Find(ifaces, c.cfg.Tunnel.LUID())
	c.noteHotspot(hs, on)

	c.mu.Lock()
	st := c.st
	want := (st.AutoConnect && !c.userOff) || c.manual
	phoneName := c.phonePlainLocked()
	nextTry := c.nextTry
	c.mu.Unlock()

	switch {
	case !st.Paired():
		c.down("not paired")
		c.setPhase(Unpaired, "Not paired", "Pair this PC with Open Bars on your iPhone.", "")
	case !on:
		c.down("left the hotspot")
		c.setPhase(NoHotspot, "Waiting for "+phoneName+"’s hotspot",
			"Open Bars connects when this PC joins it.", "")
	case !want:
		reason, title := "automatic connect is off", "Automatic connect is off"
		if st.AutoConnect {
			reason, title = "you disconnected", "Disconnected"
		}
		c.down(reason)
		c.setPhase(Off, title, "On "+phoneName+"’s hotspot. Choose Connect now to use it.", "")
	case c.cfg.Tunnel.Running():
		c.watchTunnel(ctx, hs, st, now)
	case now.Before(nextTry):
		// Waiting before asking the iPhone again; keep the current status.
	default:
		c.connect(ctx, hs, st, now)
	}
}

func (c *Controller) noteHotspot(hs hotspot.Iface, on bool) {
	switch {
	case on && !c.onHotspot:
		c.log.Infof("net", "Joined an iPhone hotspot: %s", hs)
		c.mu.Lock()
		c.failures, c.nextTry = 0, time.Time{}
		c.mu.Unlock()
	case !on && c.onHotspot:
		c.log.Infof("net", "Left the iPhone hotspot (%s)", c.hotspotSeen)
		c.mu.Lock()
		c.userOff, c.manual = false, false
		c.mu.Unlock()
	case on && (hs.Index != c.hotspotSeen.Index || hs.Address() != c.hotspotSeen.Address()):
		c.log.Infof("net", "The hotspot is now on %s", hs)
	}
	c.onHotspot, c.hotspotSeen = on, hs
	c.mu.Lock()
	if on {
		c.status.Hotspot = hs.String()
	} else {
		c.status.Hotspot = ""
	}
	c.mu.Unlock()
}

// connect asks the iPhone whether it still has this PC and brings the tunnel up.
func (c *Controller) connect(ctx context.Context, hs hotspot.Iface, st state.State, now time.Time) {
	pub, _ := wgkey.Public(st.PrivateKey)
	c.setPhase(Checking, "Checking the iPhone…", "", "")
	p, err := c.cfg.Phone.Check(ctx, pub)
	switch {
	case err == nil:
	case errors.Is(err, phone.ErrNotPaired):
		c.retryLater(now, time.Minute)
		c.setPhase(NotThisPhone, "This iPhone doesn't know this PC",
			"It may be a different iPhone, or this PC was removed there.", "Pair again.")
		c.log.Add(applog.Warn, "pair", "The iPhone on this hotspot doesn't have this PC (different iPhone, or removed there)", "Pair again.")
		return
	default:
		msg, hint := describePhoneErr(err)
		c.retryLater(now, 0)
		phase := Problem
		title := "Couldn't reach the iPhone"
		if errors.Is(err, phone.ErrUnreachable) {
			phase, title = NotSharing, "Open Bars isn't sharing on "+phonePlain(*st.Pairing)
		}
		c.setPhase(phase, title, msg, hint)
		c.log.Add(applog.Warn, "pair", "Checking with the iPhone failed: "+msg, hint)
		return
	}

	if !p.SameTunnel(*st.Pairing) || p.HotspotName != st.Pairing.HotspotName || p.DeviceName != st.Pairing.DeviceName {
		c.log.Infof("pair", "The iPhone's settings changed since pairing; using the new ones")
		c.mu.Lock()
		c.st.Pairing = &p
		c.saveLocked()
		c.mu.Unlock()
	}

	c.setPhase(Connecting, "Connecting to "+phonePlain(p)+"…", "", "")
	err = c.cfg.Tunnel.Up(TunnelConfig{PrivateKey: st.PrivateKey, Pairing: p, Hotspot: hs})
	if err != nil {
		c.retryLater(now, 30*time.Second)
		hint := "Try Connect now again. If it keeps failing, reinstall Open Bars (the network driver may be missing)."
		c.setPhase(Problem, "Couldn't start the tunnel", err.Error(), hint)
		c.log.Add(applog.Error, "vpn", "Couldn't start the tunnel: "+err.Error(), hint)
		return
	}
	c.mu.Lock()
	c.failures, c.nextTry = 0, time.Time{}
	c.mu.Unlock()
	c.upAt, c.boundIndex = now, hs.Index
	c.handshakeSeen, c.handshakeWarn, c.quietWarned = false, false, false
	addr := p.Address
	if p.Address6 != "" {
		addr += ", " + p.Address6
	}
	c.log.Infof("vpn", "Tunnel up to %s at %s through %s; this PC is %s, DNS %s, MTU %d",
		phoneLabel(p), p.Endpoint, hs, addr, p.DNS, p.MTU)
}

// watchTunnel keeps an eye on a running tunnel.
func (c *Controller) watchTunnel(ctx context.Context, hs hotspot.Iface, st state.State, now time.Time) {
	if hs.Index != c.boundIndex {
		if err := c.cfg.Tunnel.BindTo(hs.Index); err != nil {
			c.log.Add(applog.Warn, "vpn", "Couldn't move the tunnel to "+hs.String()+": "+err.Error(), "")
		} else {
			c.log.Infof("vpn", "Tunnel now sends through %s", hs)
		}
		c.boundIndex = hs.Index
	}
	ts := c.cfg.Tunnel.Stats()
	name := phonePlain(*st.Pairing)
	if ts.LastHandshake == 0 {
		if now.Sub(c.upAt) > handshakeGrace && !c.handshakeWarn {
			c.handshakeWarn = true
			c.log.Add(applog.Warn, "wg", fmt.Sprintf("No handshake with the iPhone %d s after connecting (%s)", int(handshakeGrace/time.Second), st.Pairing.Endpoint),
				"Check that Open Bars is still sharing on the iPhone, and that nothing on this PC (firewall, another VPN) blocks UDP to it.")
		}
		c.setPhase(Connecting, "Connecting to "+name+"…", "Waiting for the iPhone to answer.", "")
		return
	}
	last := time.Unix(ts.LastHandshake, 0)
	if !c.handshakeSeen {
		c.handshakeSeen = true
		c.log.Infof("wg", "Connected: first handshake with %s after %.1f s", name, last.Sub(c.upAt).Seconds())
	}
	if now.Sub(last) < quietAfter {
		c.quietWarned = false
		c.setPhase(Connected, "Connected through "+name, "This PC's traffic goes through the iPhone's cellular data.", "")
		return
	}
	if !c.quietWarned {
		c.quietWarned = true
		c.log.Add(applog.Warn, "wg", fmt.Sprintf("The iPhone went quiet: no handshake for %d minutes", int(now.Sub(last)/time.Minute)),
			"Check that Open Bars is still sharing on the iPhone.")
	}
	c.setPhase(Problem, "No answer from "+name, "The tunnel is up but the iPhone stopped answering.", "Check that Open Bars is still sharing on the iPhone.")
	if now.Sub(c.lastQuietCheck) < recheckQuietEvery {
		return
	}
	c.lastQuietCheck = now
	pub, _ := wgkey.Public(st.PrivateKey)
	if _, err := c.cfg.Phone.Check(ctx, pub); err != nil {
		reason := "the iPhone stopped sharing"
		if errors.Is(err, phone.ErrNotPaired) {
			reason = "the iPhone no longer has this PC"
		}
		c.down(reason)
		c.retryLater(now, 0)
	}
}

// down stops the tunnel if it's running.
func (c *Controller) down(reason string) {
	if !c.cfg.Tunnel.Running() {
		return
	}
	ts := c.cfg.Tunnel.Stats()
	dur := time.Duration(0)
	if ts.Since > 0 {
		dur = c.now().Sub(time.Unix(ts.Since, 0)).Round(time.Second)
	}
	if err := c.cfg.Tunnel.Down(); err != nil {
		c.log.Add(applog.Warn, "vpn", "Problem stopping the tunnel: "+err.Error(), "")
	}
	c.log.Infof("vpn", "Tunnel down (%s) after %s; received %s, sent %s", reason, dur, bytesLabel(ts.RX), bytesLabel(ts.TX))
}

// retryLater backs off before asking the iPhone again: fixed if d > 0,
// otherwise 5 s doubling up to a minute.
func (c *Controller) retryLater(now time.Time, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures++
	if d == 0 {
		d = 5 * time.Second << min(c.failures-1, 4)
		if d > time.Minute {
			d = time.Minute
		}
	}
	c.nextTry = now.Add(d)
}

func (c *Controller) expireDetailed(now time.Time) {
	c.mu.Lock()
	expired := !c.detailedUntil.IsZero() && now.After(c.detailedUntil)
	if expired {
		c.detailedUntil = time.Time{}
	}
	c.mu.Unlock()
	if expired {
		c.log.SetDetailed(false)
		c.log.Infof("app", "Detailed logging turned itself off after %d minutes", int(detailedFor/time.Minute))
	}
}

func (c *Controller) setPhase(p Phase, title, detail, hint string) {
	c.mu.Lock()
	c.status.Phase, c.status.Title, c.status.Detail, c.status.Hint = p, title, detail, hint
	c.mu.Unlock()
}

func (c *Controller) saveLocked() error {
	if c.cfg.StatePath == "" {
		return nil
	}
	err := state.Save(c.cfg.StatePath, c.st, c.cfg.Sealer)
	if err != nil {
		c.log.Add(applog.Error, "svc", "Couldn't save settings: "+err.Error(), "")
	}
	return err
}

func (c *Controller) phoneNameLocked() string {
	if c.st.Pairing == nil {
		return "the iPhone"
	}
	return phoneLabel(*c.st.Pairing)
}

// phonePlain is the iPhone's name for titles ("Aayush’s iPhone").
func phonePlain(p phone.Pairing) string {
	if p.HotspotName == "" {
		return "the iPhone"
	}
	return p.HotspotName
}

func (c *Controller) phonePlainLocked() string {
	if c.st.Pairing == nil {
		return "the iPhone"
	}
	return phonePlain(*c.st.Pairing)
}

// phoneLabel is the iPhone's name in quotes, for log lines.
func phoneLabel(p phone.Pairing) string {
	if p.HotspotName == "" {
		return "the iPhone"
	}
	return "“" + p.HotspotName + "”"
}

// describePhoneErr turns a phone error into a sentence and a hint.
func describePhoneErr(err error) (msg, hint string) {
	var ue *phone.UnreachableError
	switch {
	case errors.As(err, &ue):
		return "Open Bars on the iPhone isn't answering (" + ue.Reason + ")",
			"Open Open Bars on the iPhone and tap Start. Personal Hotspot must be on and this PC on it."
	case errors.Is(err, phone.ErrDenied):
		return err.Error(), "Pair again and tap Allow on the iPhone."
	case errors.Is(err, phone.ErrTimedOut):
		return err.Error(), "Keep Open Bars open on the iPhone, pair again and tap Allow within 90 seconds."
	case errors.Is(err, phone.ErrOldIPhone):
		return err.Error(), "Update Open Bars on the iPhone."
	}
	return err.Error(), ""
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func bytesLabel(n uint64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
