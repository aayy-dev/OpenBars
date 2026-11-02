package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aayy-dev/OpenBars/windows/internal/applog"
	"github.com/aayy-dev/OpenBars/windows/internal/hotspot"
	"github.com/aayy-dev/OpenBars/windows/internal/phone"
	"github.com/aayy-dev/OpenBars/windows/internal/wgkey"
)

// Check is one Diagnose step.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	State  string `json:"state"` // ok, warning, failed
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
	Action string `json:"action,omitempty"` // what the UI can offer: pair, connect, auto
}

// Diagnose checks each link the tunnel depends on, in order, and stops at
// the first broken one (later ones can't be checked yet).
func (c *Controller) Diagnose(ctx context.Context) []Check {
	var out []Check
	add := func(ch Check) bool {
		out = append(out, ch)
		lvl := applog.Info
		if ch.State == "failed" {
			lvl = applog.Warn
		}
		c.log.Add(lvl, "app", fmt.Sprintf("Diagnose: %s: %s (%s)", ch.Title, ch.State, ch.Detail), ch.Hint)
		return ch.State != "failed"
	}

	ifaces, err := c.cfg.Net.Snapshot()
	if err != nil {
		add(Check{ID: "hotspot", Title: "On an iPhone hotspot", State: "failed", Detail: "Couldn't read the network adapters: " + err.Error()})
		return out
	}
	hs, on := hotspot.Find(ifaces, c.cfg.Tunnel.LUID())
	if !add(hotspotCheck(hs, on)) {
		return out
	}

	info, err := c.cfg.Phone.Info(ctx)
	if err != nil {
		msg, hint := describePhoneErr(err)
		add(Check{ID: "sharing", Title: "Open Bars sharing on the iPhone", State: "failed", Detail: msg, Hint: hint})
		return out
	}
	detail := "It's answering."
	if info.HotspotName != "" {
		detail = "Sharing as “" + info.HotspotName + "”."
	}
	add(Check{ID: "sharing", Title: "Open Bars sharing on the iPhone", State: "ok", Detail: detail})

	c.mu.Lock()
	st := c.st
	c.mu.Unlock()
	if !st.Paired() {
		add(Check{ID: "paired", Title: "Paired with this iPhone", State: "failed", Detail: "This PC isn't paired yet.", Hint: "Pair with iPhone.", Action: "pair"})
		return out
	}
	pub, _ := wgkey.Public(st.PrivateKey)
	p, err := c.cfg.Phone.Check(ctx, pub)
	switch {
	case errors.Is(err, phone.ErrNotPaired):
		add(Check{ID: "paired", Title: "Paired with this iPhone", State: "failed",
			Detail: "The iPhone doesn't have this PC (removed there, or a different iPhone).", Hint: "Pair again.", Action: "pair"})
		return out
	case err != nil:
		msg, hint := describePhoneErr(err)
		add(Check{ID: "paired", Title: "Paired with this iPhone", State: "failed", Detail: msg, Hint: hint})
		return out
	}
	add(Check{ID: "paired", Title: "Paired with this iPhone", State: "ok", Detail: fmt.Sprintf("Known as “%s”, %s.", p.DeviceName, p.Address)})

	if st.AutoConnect {
		add(Check{ID: "auto", Title: "Connects automatically", State: "ok", Detail: "Whenever this PC joins " + phonePlain(p) + "’s hotspot."})
	} else {
		add(Check{ID: "auto", Title: "Connects automatically", State: "warning", Detail: "Automatic connect is off.", Action: "auto"})
	}

	if !c.cfg.Tunnel.Running() {
		add(Check{ID: "tunnel", Title: "Tunnel working", State: "warning", Detail: "The tunnel isn't connected right now.", Action: "connect"})
		return out
	}
	ts := c.cfg.Tunnel.Stats()
	if ts.LastHandshake == 0 || c.now().Sub(time.Unix(ts.LastHandshake, 0)) > quietAfter {
		add(Check{ID: "tunnel", Title: "Tunnel working", State: "failed",
			Detail: "The tunnel is up, but the iPhone isn't answering on " + p.Endpoint + ".",
			Hint:   "Check that nothing on this PC (firewall, another VPN) blocks UDP to the iPhone."})
		return out
	}
	add(Check{ID: "tunnel", Title: "Tunnel working", State: "ok",
		Detail: fmt.Sprintf("Last handshake %s ago; %s received, %s sent.", c.now().Sub(time.Unix(ts.LastHandshake, 0)).Round(time.Second), bytesLabel(ts.RX), bytesLabel(ts.TX))})

	if c.cfg.Probe == nil {
		return out
	}
	start := c.now()
	if err := c.cfg.Probe.DNS(ctx, p.DNS); err != nil {
		add(Check{ID: "dns", Title: "DNS through the tunnel", State: "failed", Detail: err.Error(),
			Hint: "Check the iPhone's Upstream DNS setting (Settings › Tunnel)."})
		return out
	}
	add(Check{ID: "dns", Title: "DNS through the tunnel", State: "ok", Detail: fmt.Sprintf("%s answered in %d ms.", p.DNS, c.now().Sub(start).Milliseconds())})
	start = c.now()
	if err := c.cfg.Probe.Internet(ctx); err != nil {
		add(Check{ID: "internet", Title: "Internet through the tunnel", State: "failed", Detail: err.Error(),
			Hint: "Check that the iPhone has cellular data and that Open Bars is allowed to use it."})
		return out
	}
	add(Check{ID: "internet", Title: "Internet through the tunnel", State: "ok", Detail: fmt.Sprintf("Reached the internet in %d ms.", c.now().Sub(start).Milliseconds())})
	return out
}

func hotspotCheck(hs hotspot.Iface, on bool) Check {
	if !on {
		return Check{ID: "hotspot", Title: "On an iPhone hotspot", State: "failed",
			Detail: "This PC isn't on an iPhone's hotspot.",
			Hint:   "Join the iPhone's hotspot in Wi‑Fi settings (it's named after the iPhone), or connect with a USB cable."}
	}
	return Check{ID: "hotspot", Title: "On an iPhone hotspot", State: "ok", Detail: "This PC is " + hs.Address().String() + " on " + hs.String() + "."}
}
