// Package tunnel runs the WireGuard tunnel to the iPhone on Windows:
// wireguard-go on a Wintun adapter named "Open Bars", with its addresses,
// default routes and DNS set through the IP Helper API.
package tunnel

import (
	"bufio"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"github.com/aayy-dev/OpenBars/windows/internal/applog"
	"github.com/aayy-dev/OpenBars/windows/internal/core"
	"github.com/aayy-dev/OpenBars/windows/internal/wgkey"
)

// AdapterName is what the adapter is called in Windows' network settings.
const AdapterName = "Open Bars"

// adapterGUID stays the same across runs, so Windows keeps one network
// profile for the tunnel instead of "Network 2, 3, …".
var adapterGUID = windows.GUID{Data1: 0x0b5a7e11, Data2: 0x0b5a, Data3: 0x4c0e,
	Data4: [8]byte{0x8a, 0x75, 0x0f, 0xe1, 0x2c, 0x0a, 0x75, 0x01}}

// Tunnel implements core.Tunnel.
type Tunnel struct {
	log *applog.Ring

	mu    sync.Mutex
	dev   *device.Device
	bind  conn.Bind
	luid  winipcfg.LUID
	since time.Time
}

// New returns a stopped tunnel that logs to log.
func New(log *applog.Ring) *Tunnel {
	tun.WintunTunnelType = AdapterName
	return &Tunnel{log: log}
}

// Up creates the adapter and starts WireGuard.
func (t *Tunnel) Up(cfg core.TunnelConfig) (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dev != nil {
		t.closeLocked()
	}
	p := cfg.Pairing
	priv, err := wgkey.Hex(cfg.PrivateKey)
	if err != nil {
		return errors.New("this PC's key is invalid; pair again")
	}
	pub, err := wgkey.Hex(p.ServerPublicKey)
	if err != nil {
		return errors.New("the iPhone's key is invalid; pair again")
	}
	ep, err := netip.ParseAddrPort(p.Endpoint)
	if err != nil {
		return fmt.Errorf("bad iPhone address %q", p.Endpoint)
	}
	a4, err := netip.ParseAddr(p.Address)
	if err != nil || !a4.Is4() {
		return fmt.Errorf("bad tunnel address %q", p.Address)
	}
	a6, _ := netip.ParseAddr(p.Address6)
	dns, err := netip.ParseAddr(p.DNS)
	if err != nil {
		return fmt.Errorf("bad DNS server %q", p.DNS)
	}
	mtu := p.MTU
	if mtu < 1280 {
		mtu = 1280
	}

	td, err := tun.CreateTUNWithRequestedGUID(AdapterName, &adapterGUID, mtu)
	if err != nil {
		return fmt.Errorf("couldn't create the network adapter (is wintun.dll next to OpenBars.exe?): %w", err)
	}
	nt := td.(*tun.NativeTun)
	luid := winipcfg.LUID(nt.LUID())
	bind := conn.NewDefaultBind()
	logger := &device.Logger{
		Verbosef: func(f string, a ...any) { t.log.Debugf("wg", f, a...) },
		Errorf:   func(f string, a ...any) { t.log.Warnf("wg", f, a...) },
	}
	dev := device.NewDevice(td, bind, logger)
	defer func() {
		if err != nil {
			dev.Close()
		}
	}()

	ipc := fmt.Sprintf("private_key=%s\nreplace_peers=true\npublic_key=%s\nendpoint=%s\n"+
		"persistent_keepalive_interval=25\nreplace_allowed_ips=true\nallowed_ip=0.0.0.0/0\nallowed_ip=::/0\n",
		priv, pub, ep)
	if err = dev.IpcSet(ipc); err != nil {
		return fmt.Errorf("configure WireGuard: %w", err)
	}
	if err = configureAdapter(luid, a4, a6, dns, mtu); err != nil {
		return err
	}
	if err = dev.Up(); err != nil {
		return fmt.Errorf("start WireGuard: %w", err)
	}
	t.dev, t.bind, t.luid, t.since = dev, bind, luid, time.Now()
	if err = t.bindLocked(cfg.Hotspot.Index); err != nil {
		t.log.Add(applog.Warn, "vpn", "Couldn't pin the tunnel to "+cfg.Hotspot.Name+": "+err.Error(),
			"Traffic to the iPhone may loop. Reconnect; if it keeps happening, share the log.")
		err = nil
	}
	return nil
}

// configureAdapter gives the adapter its addresses, sends all traffic into
// it and points DNS at the iPhone.
func configureAdapter(luid winipcfg.LUID, a4, a6, dns netip.Addr, mtu int) error {
	addrs := []netip.Prefix{netip.PrefixFrom(a4, 32)}
	routes := []*winipcfg.RouteData{{Destination: netip.MustParsePrefix("0.0.0.0/0"), NextHop: netip.IPv4Unspecified()}}
	if a6.Is6() {
		addrs = append(addrs, netip.PrefixFrom(a6, 128))
		routes = append(routes, &winipcfg.RouteData{Destination: netip.MustParsePrefix("::/0"), NextHop: netip.IPv6Unspecified()})
	}
	if err := luid.SetIPAddresses(addrs); err != nil {
		return fmt.Errorf("set tunnel addresses: %w", err)
	}
	if err := luid.SetRoutes(routes); err != nil {
		return fmt.Errorf("set routes: %w", err)
	}
	fam := winipcfg.AddressFamily(windows.AF_INET)
	if dns.Is6() {
		fam = windows.AF_INET6
	}
	if err := luid.SetDNS(fam, []netip.Addr{dns}, nil); err != nil {
		return fmt.Errorf("set DNS: %w", err)
	}
	for _, f := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
		iface, err := luid.IPInterface(f)
		if err != nil {
			if f == windows.AF_INET6 {
				continue // IPv6 can be turned off on the PC
			}
			return fmt.Errorf("read adapter settings: %w", err)
		}
		// Lowest metric so the tunnel's default route and DNS win over the
		// hotspot's own; no duplicate-address probing on a point-to-point link.
		iface.UseAutomaticMetric = false
		iface.Metric = 0
		iface.NLMTU = uint32(mtu)
		iface.DadTransmits = 0
		if f == windows.AF_INET6 {
			iface.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
		}
		if err := iface.Set(); err != nil {
			return fmt.Errorf("apply adapter settings: %w", err)
		}
	}
	return nil
}

// BindTo makes the encrypted traffic leave through the given adapter (the
// one on the hotspot), so it never loops into the tunnel.
func (t *Tunnel) BindTo(index uint32) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bindLocked(index)
}

func (t *Tunnel) bindLocked(index uint32) error {
	b, ok := t.bind.(conn.BindSocketToInterface)
	if !ok || t.dev == nil {
		return nil
	}
	if err := b.BindSocketToInterface4(index, false); err != nil {
		return err
	}
	if err := b.BindSocketToInterface6(index, false); err != nil {
		t.log.Debugf("vpn", "IPv6 socket not pinned: %v", err)
	}
	return nil
}

// Down stops WireGuard and removes the adapter (with its routes and DNS).
func (t *Tunnel) Down() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeLocked()
	return nil
}

func (t *Tunnel) closeLocked() {
	if t.dev != nil {
		t.dev.Close()
	}
	t.dev, t.bind, t.luid = nil, nil, 0
}

// Running reports whether the tunnel is up.
func (t *Tunnel) Running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dev != nil
}

// LUID is the adapter's LUID while up.
func (t *Tunnel) LUID() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return uint64(t.luid)
}

// Stats reads WireGuard's counters.
func (t *Tunnel) Stats() core.TunnelStats {
	t.mu.Lock()
	dev, since := t.dev, t.since
	t.mu.Unlock()
	if dev == nil {
		return core.TunnelStats{}
	}
	s := core.TunnelStats{Since: since.Unix()}
	cfg, err := dev.IpcGet()
	if err != nil {
		return s
	}
	sc := bufio.NewScanner(strings.NewReader(cfg))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		n, _ := strconv.ParseUint(v, 10, 64)
		switch k {
		case "endpoint":
			s.Endpoint = v
		case "last_handshake_time_sec":
			s.LastHandshake = int64(n)
		case "rx_bytes":
			s.RX = n
		case "tx_bytes":
			s.TX = n
		}
	}
	return s
}
