// Package engine is Open Bars' sharing engine: a WireGuard server for the
// devices on the iPhone's Personal Hotspot whose traffic leaves from
// sockets on the phone itself (pinned to cellular), so it looks like the
// phone's own traffic.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

const (
	defaultPort = 51820
	// defaultConnectPort is where Open Bars Connect reaches the iPhone.
	defaultConnectPort = 8080
	defaultGateway     = "10.66.66.1"
	defaultMTU         = 1420
	defaultHotspot     = "172.20.10.1" // where iOS puts the phone on its hotspot
	housekeepEvery     = 15 * time.Second
	tcpMaxInFlight     = 1024
)

var defaultDNS = []string{"1.1.1.1", "8.8.8.8"}

// Config is the JSON the app starts the engine with.
type Config struct {
	PrivateKey   string   `json:"private_key"`   // the phone's WireGuard key, base64
	ListenPort   int      `json:"listen_port"`   // WireGuard UDP port (0 = 51820)
	Gateway      string   `json:"gateway"`       // the phone inside the tunnel (DNS, config page)
	MTU          int      `json:"mtu"`           // 0 = 1420
	BindCellular bool     `json:"bind_cellular"` // pin outgoing sockets to cellular
	DNS          []string `json:"dns"`           // upstream resolvers, "ip" or "ip:port"
	WebPort      int      `json:"web_port"`      // config page; 0 = off
	// ConnectPort serves Open Bars Connect (pairing, /client.json,
	// /hotspot-check) whenever sharing runs; 0 = 8080, where Connect looks.
	ConnectPort int          `json:"connect_port"`
	Peers       []PeerConfig `json:"peers"`

	// The hotspot's Wi-Fi name (the iPhone's name). Open Bars Connect gets it
	// when pairing and turns its VPN on only on that network.
	HotspotSSID string `json:"hotspot_ssid"`

	// Where hourly/daily usage is saved ("" = keep it in memory only), and
	// the IANA time zone that decides where a day starts.
	UsageFile string `json:"usage_file"`
	TimeZone  string `json:"time_zone"`

	// LowMemory is set when running inside the Network Extension (iOS caps
	// its memory): smaller TCP buffers and a Go memory limit.
	LowMemory bool `json:"low_memory"`
}

// PeerConfig is one device.
type PeerConfig struct {
	Name       string `json:"name"`
	PublicKey  string `json:"public_key"`            // base64
	PrivateKey string `json:"private_key,omitempty"` // only for WireGuard-app devices (for their .conf)
	Address    string `json:"address"`               // tunnel IPv4, e.g. 10.66.66.2
}

// Server is a running engine.
type Server struct {
	cfgMu sync.RWMutex
	cfg   Config // defaults applied; only Peers changes after Start

	log     *Logger
	ctx     context.Context
	cancel  context.CancelFunc
	started time.Time
	gw      netip.Addr
	dnsUp   []netip.AddrPort
	// dnsSlots limits DNS queries in flight.
	dnsSlots chan struct{}

	eg      *egress
	dev     *netDev
	wg      *device.Device
	udp     *udpNAT
	frags   *reassembler
	ping    *pinger
	webs    []*http.Server
	webErr  string
	netInfo atomic.Pointer[NetInfo] // last seen, for endpointHost and isPhoneAddr
	fails   failTally
	pair    pairing
	stats   *stats

	tcpActive, tcpTotal atomic.Int64
	bytesUp, bytesDown  atomic.Int64

	wgSeenMu sync.Mutex
	wgSeen   map[string]bool // peers whose first handshake line was logged
	watch    map[string]*peerWatch
	stopOnce sync.Once
}

// Start brings the engine up. log receives its log entries.
func Start(cfg Config, log *Logger) (*Server, error) {
	if cfg.ListenPort == 0 {
		cfg.ListenPort = defaultPort
	}
	if cfg.ConnectPort == 0 {
		cfg.ConnectPort = defaultConnectPort
	}
	if cfg.Gateway == "" {
		cfg.Gateway = defaultGateway
	}
	if cfg.MTU == 0 {
		cfg.MTU = defaultMTU
	}
	if len(cfg.DNS) == 0 {
		cfg.DNS = append([]string(nil), defaultDNS...)
	}
	gw, err := netip.ParseAddr(strings.TrimSpace(cfg.Gateway))
	if err != nil || !gw.Is4() {
		return nil, fmt.Errorf("bad gateway %q: must be an IPv4 address", cfg.Gateway)
	}
	cfg.Peers = append([]PeerConfig(nil), cfg.Peers...)

	s := &Server{
		cfg:      cfg,
		log:      log,
		started:  time.Now(),
		gw:       gw,
		dnsUp:    parseDNS(cfg.DNS),
		dnsSlots: make(chan struct{}, dnsInFlight),
		wgSeen:   map[string]bool{},
		watch:    map[string]*peerWatch{},
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.eg = newEgress(s.ctx, cfg.BindCellular, log)
	ni := s.eg.refresh()
	s.netInfo.Store(&ni)

	if cfg.LowMemory {
		debug.SetMemoryLimit(30 << 20) // GC hard before the extension's cap
		log.Infof("share", "low-memory mode (running as a Network Extension)")
	}
	s.dev, err = newNetDev(cfg.MTU, gw, cfg.LowMemory)
	if err != nil {
		s.cancel()
		return nil, err
	}
	s.udp = newUDPNAT(s)
	s.frags = newReassembler()
	s.ping = newPinger(s)
	s.dev.intercept = s.intercept
	fwd := tcp.NewForwarder(s.dev.stack, 0, tcpMaxInFlight, s.handleTCP)
	s.dev.stack.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)

	s.wg = device.NewDevice(s.dev, conn.NewDefaultBind(), s.wgLogger())
	ipc, err := s.baseUAPI()
	if err == nil {
		err = s.wg.IpcSet(ipc)
		if err != nil {
			err = fmt.Errorf("configure wireguard: %w", err)
		}
	}
	// The device may already be up (netDev reports "up" at once), so a
	// busy port can surface from IpcSet as well as from Up.
	if err == nil {
		err = s.wg.Up()
		if err != nil {
			err = portError(cfg.ListenPort, err)
		}
	} else if errors.Is(err, syscall.EADDRINUSE) {
		err = portError(cfg.ListenPort, err)
	}
	if err != nil {
		s.wg.Close()
		s.cancel()
		return nil, err
	}
	where := "all interfaces"
	if cfg.LowMemory {
		where = "bound to the hotspot interface when there is one"
	}
	log.Infof("wg", "WireGuard listening on UDP %d (%s) with %d device(s)", cfg.ListenPort, where, len(cfg.Peers))
	if cfg.LowMemory {
		go s.scopeWireGuardToBridge()
	}
	if ni.HotspotIP != "" {
		log.Infof("share", "Hotspot address %s", ni.HotspotIP)
	} else {
		log.Infof("share", "No hotspot interface yet (iOS adds it when a device joins); devices will reach the phone at %s", defaultHotspot)
	}
	s.startWebs()
	s.stats = newStats(cfg.UsageFile, cfg.TimeZone, log)
	go s.housekeeping()
	go s.statsLoop()
	s.logSession(ni)
	return s, nil
}

// startWebs starts Connect's endpoints on ConnectPort, and the config page
// (if on) with them or on its own port.
func (s *Server) startWebs() {
	cfg := s.cfg
	where := "all interfaces"
	if cfg.LowMemory {
		where = "127.0.0.1 and the hotspot interface"
	}
	start := func(port int, page, connect bool, what string) {
		if err := s.startWeb(port, page, connect); err != nil {
			s.webErr = err.Error()
			hint := "Is Personal Hotspot on? Restarting Open Bars may help."
			if errors.Is(err, syscall.EADDRINUSE) {
				hint = fmt.Sprintf("Something else on the iPhone is using TCP port %d. Restart Open Bars, or move the config page to another port in Settings.", port)
			}
			s.log.ErrorHint("web", hint, "Couldn't start the %s on TCP %d: %v", what, port, err)
			return
		}
		s.log.Infof("web", "%s on TCP %d (%s)", strings.ToUpper(what[:1])+what[1:], port, where)
	}
	switch {
	case cfg.WebPort == cfg.ConnectPort:
		start(cfg.ConnectPort, true, true, "config page and pairing API")
	case cfg.WebPort > 0:
		start(cfg.ConnectPort, false, true, "pairing API")
		start(cfg.WebPort, true, false, "config page")
	default:
		start(cfg.ConnectPort, false, true, "pairing API")
	}
}

// portError names the UDP port WireGuard couldn't listen on.
func portError(port int, err error) error {
	var oe *net.OpError
	if errors.As(err, &oe) {
		err = oe
	}
	return fmt.Errorf("listen on UDP %d: %w", port, err)
}

// logSession writes the one entry that sums up a new session.
func (s *Server) logSession(ni NetInfo) {
	cfg := s.cfg
	dns := make([]string, len(s.dnsUp))
	for i, a := range s.dnsUp {
		dns[i] = a.String()
	}
	page := "off"
	if cfg.WebPort > 0 {
		page = "TCP " + strconv.Itoa(cfg.WebPort)
		if s.webErr != "" {
			page += " (failed)"
		}
	}
	mode := "in-app"
	if cfg.LowMemory {
		mode = "background (Network Extension)"
	}
	vpn := "none"
	if ni.VPNActive {
		vpn = ni.VPNIf
	}
	s.log.Infof("share", "Sharing started on %s (%s): mode %s, WireGuard UDP %d, %d device(s), DNS %s, cellular-only %s, config page %s, hotspot name %q; egress %s, VPN on the phone: %s",
		sysInfo(), runtime.Version(), mode, cfg.ListenPort, len(cfg.Peers), orNone(strings.Join(dns, ", ")), onOff(cfg.BindCellular),
		page, cfg.HotspotSSID, s.eg.ifName(), vpn)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// baseUAPI is the device's initial configuration.
func (s *Server) baseUAPI() (string, error) {
	priv, err := keyToHex(s.cfg.PrivateKey)
	if err != nil {
		return "", errors.New("server private key: invalid key (must be 32 bytes, base64)")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\nlisten_port=%d\nreplace_peers=true\n", priv, s.cfg.ListenPort)
	for _, p := range s.cfg.Peers {
		s.peerUAPI(&b, p, false)
	}
	return b.String(), nil
}

// peerUAPI writes one device's UAPI block; false (and a log entry) if the
// device is invalid and was skipped.
func (s *Server) peerUAPI(b *strings.Builder, p PeerConfig, replace bool) bool {
	pub, addr, ok := s.validPeer(p)
	if !ok {
		return false
	}
	fmt.Fprintf(b, "public_key=%s\n", pub)
	if replace {
		b.WriteString("replace_allowed_ips=true\n")
	}
	bits := 32
	if addr.Is6() {
		bits = 128
	}
	fmt.Fprintf(b, "allowed_ip=%s\n", netip.PrefixFrom(addr, bits))
	if v6, ok := peerAddr6(addr); ok {
		fmt.Fprintf(b, "allowed_ip=%s\n", netip.PrefixFrom(v6, 128))
	}
	return true
}

// validPeer checks a device's key and address, logging why it's skipped.
func (s *Server) validPeer(p PeerConfig) (hexKey string, addr netip.Addr, ok bool) {
	hexKey, err := keyToHex(p.PublicKey)
	if err != nil {
		s.log.WarnHint("wg", "Remove the device in Open Bars and add it again.", "Skipping device %q: its public key is invalid", p.Name)
		return "", netip.Addr{}, false
	}
	addr, err = netip.ParseAddr(strings.TrimSpace(p.Address))
	if err != nil {
		s.log.WarnHint("wg", "Remove the device in Open Bars and add it again.", "Skipping device %q: its address %q isn't an IP address", p.Name, p.Address)
		return "", netip.Addr{}, false
	}
	return hexKey, addr.Unmap(), true
}

// SetPeers replaces the device list without disconnecting devices whose
// entry didn't change: only added, re-addressed and removed devices are
// sent to WireGuard.
func (s *Server) SetPeers(peers []PeerConfig) error {
	type old struct {
		addr netip.Addr
		p    PeerConfig
	}
	olds := map[string]old{}
	for _, p := range s.Config().Peers {
		if pub, err := keyToHex(p.PublicKey); err == nil {
			a, _ := netip.ParseAddr(strings.TrimSpace(p.Address))
			olds[pub] = old{a.Unmap(), p}
		}
	}
	// With duplicate keys the last entry wins.
	lastIdx := map[string]int{}
	for i, p := range peers {
		if pub, err := keyToHex(p.PublicKey); err == nil {
			lastIdx[pub] = i
		}
	}

	var b strings.Builder
	var added, moved []PeerConfig
	keep := map[string]bool{}
	for i, p := range peers {
		pub, addr, ok := s.validPeer(p)
		if !ok || lastIdx[pub] != i {
			continue
		}
		keep[pub] = true
		o, had := olds[pub]
		if had && o.addr == addr {
			continue // unchanged (maybe renamed): keep its session
		}
		s.peerUAPI(&b, p, true)
		if had {
			moved = append(moved, p)
		} else {
			added = append(added, p)
		}
	}
	var removed []PeerConfig
	for pub, o := range olds {
		if !keep[pub] {
			fmt.Fprintf(&b, "public_key=%s\nremove=true\n", pub)
			removed = append(removed, o.p)
		}
	}
	if b.Len() > 0 {
		if err := s.wg.IpcSet(b.String()); err != nil {
			s.log.Errorf("wg", "Couldn't update the device list: %v", err)
			return fmt.Errorf("configure wireguard: %w", err)
		}
	}
	s.cfgMu.Lock()
	s.cfg.Peers = append([]PeerConfig(nil), peers...)
	s.cfgMu.Unlock()
	s.pair.resolve(peers) // adding a waiting device's key is how the app approves it
	for _, p := range added {
		s.log.Infof("wg", "Device added: %s (%s) at %s", p.Name, shortKey(p.PublicKey), p.Address)
	}
	for _, p := range moved {
		s.log.Infof("wg", "Device %s (%s) moved to %s", p.Name, shortKey(p.PublicKey), p.Address)
	}
	for _, p := range removed {
		s.log.Infof("wg", "Device removed: %s (%s)", p.Name, shortKey(p.PublicKey))
	}
	s.log.Infof("wg", "Device list updated: %d device(s)", len(peers))
	return nil
}

// Stop shuts everything down. The UDP and web ports are free when it
// returns. Safe to call more than once.
func (s *Server) Stop() {
	s.stopOnce.Do(s.stop)
}

func (s *Server) stop() {
	s.cancel()
	if err := s.stats.save(time.Now(), true); err != nil {
		s.log.WarnHint("usage", "Usage history may be missing the last minute.", "Couldn't save usage: %v", err)
	}
	for _, w := range s.webs {
		w.Close()
	}
	s.wg.Close()
	s.udp.reap(0)
	s.ping.reap(0)
	s.fails.flush(s.log, time.Now(), true)
	s.log.Infof("share", "Sharing stopped after %s (↑%s ↓%s, %d TCP connections)",
		time.Since(s.started).Round(time.Second), fmtBytes(uint64(s.bytesUp.Load())), fmtBytes(uint64(s.bytesDown.Load())), s.tcpTotal.Load())
}

// Config returns the current configuration (defaults applied).
func (s *Server) Config() Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	c := s.cfg
	c.Peers = append([]PeerConfig(nil), s.cfg.Peers...)
	c.DNS = append([]string(nil), s.cfg.DNS...)
	return c
}

// housekeeping re-detects interfaces, reaps idle flows, logs failure
// summaries and runs the diagnostics every 15 s.
func (s *Server) housekeeping() {
	var d diag
	d.tick(s.log)
	t := time.NewTicker(housekeepEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-t.C:
			ni := s.eg.refresh()
			s.netInfo.Store(&ni)
			s.udp.reap(udpIdle)
			s.ping.reap(pingIdle)
			s.frags.reap()
			s.fails.flush(s.log, now, false)
			d.tick(s.log)
		}
	}
}

// intercept routes a client packet: UDP to the NAT (fragments reassembled
// first), pings to the pinger, unparseable packets to nowhere, the rest
// (TCP, other ICMPv6, ...) to the userspace stack.
func (s *Server) intercept(pkt []byte) bool {
	p, ok := parsePacket(pkt)
	if !ok {
		return true
	}
	switch {
	case p.proto == protoUDP:
		if p.frag {
			if p, ok = s.frags.add(&p); !ok {
				return true // waiting for the rest
			}
		}
		s.udp.handle(&p)
		return true
	case p.proto == protoICMP && p.version == 4:
		s.ping.handle(&p)
		return true
	case p.proto == protoICMPv6 && !p.frag && len(p.payload) >= 8 && p.payload[0] == icmpv6EchoRequest:
		// Without this the userspace stack would answer every IPv6 ping itself.
		s.ping.handle6(&p)
		return true
	}
	return false
}

// isPhoneAddr reports the phone's own hotspot addresses.
func (s *Server) isPhoneAddr(a netip.Addr) bool {
	if a == netip.MustParseAddr(defaultHotspot) {
		return true
	}
	if ni := s.netInfo.Load(); ni != nil && ni.HotspotIP != "" {
		return a.String() == ni.HotspotIP
	}
	return false
}

// Status is what the app shows. Every key is always present.
type Status struct {
	Running    bool         `json:"running"`
	ListenPort int          `json:"listen_port"`
	Net        NetInfo      `json:"net"`
	EgressIf   string       `json:"egress_if"` // pinned interface, or "default route"
	TCPActive  int64        `json:"tcp_active"`
	TCPTotal   int64        `json:"tcp_total"`
	UDPFlows   int          `json:"udp_flows"`
	BytesUp    int64        `json:"bytes_up"`   // devices -> internet
	BytesDown  int64        `json:"bytes_down"` // internet -> devices
	Uptime     int64        `json:"uptime"`     // seconds
	WebError   string       `json:"web_error"`
	Peers      []PeerStatus `json:"peers"`

	// Devices waiting for someone to tap Allow in the app.
	Pairing []PairRequest `json:"pairing"`
}

// PeerStatus is one device, merged with WireGuard's live state.
type PeerStatus struct {
	Name          string `json:"name"`
	PublicKey     string `json:"public_key"`
	Address       string `json:"address"`
	Endpoint      string `json:"endpoint"`       // ip:port it was last seen at
	LastHandshake int64  `json:"last_handshake"` // when WireGuard last completed one (Unix time); 0 before any
	RxBytes       int64  `json:"rx_bytes"`       // bytes the device sent
	TxBytes       int64  `json:"tx_bytes"`       // bytes sent to the device
	// LastSeen is when the device last sent anything (unix seconds, 0 =
	// never), kept across restarts. Devices send keepalives at least every
	// 25 s, so "seen in the last 40 s" means connected; WireGuard handshakes
	// only happen every 2 minutes and reset when the device list changes.
	LastSeen int64 `json:"last_seen"`
}

// Status reports counters, interfaces and devices.
func (s *Server) Status() Status {
	cfg := s.Config()
	st := Status{
		Running:    s.ctx.Err() == nil,
		ListenPort: cfg.ListenPort,
		Net:        DetectNetInfo(),
		EgressIf:   s.eg.ifName(),
		TCPActive:  s.tcpActive.Load(),
		TCPTotal:   s.tcpTotal.Load(),
		UDPFlows:   s.udp.count(),
		BytesUp:    s.bytesUp.Load(),
		BytesDown:  s.bytesDown.Load(),
		Uptime:     int64(time.Since(s.started).Seconds()),
		WebError:   s.webErr,
		Pairing:    s.pair.list(),
	}
	// Each configured device, with WireGuard's live numbers when it has any.
	live := s.livePeers()
	st.Peers = make([]PeerStatus, 0, len(cfg.Peers))
	for _, dev := range cfg.Peers {
		row := PeerStatus{Name: dev.Name, PublicKey: dev.PublicKey, Address: dev.Address,
			LastSeen: s.stats.lastSeen(dev.PublicKey)}
		if wg := live[dev.PublicKey]; wg != nil {
			row.Endpoint = wg.Endpoint
			row.LastHandshake = wg.LastHandshake
			row.RxBytes, row.TxBytes = wg.RxBytes, wg.TxBytes
		}
		st.Peers = append(st.Peers, row)
	}
	if len(st.Peers) == 0 {
		st.Peers = nil // "peers": null with no devices, as the app expects
	}
	return st
}

// livePeers reads WireGuard's per-device state (endpoint, last handshake,
// byte counters), keyed by base64 public key.
func (s *Server) livePeers() map[string]*PeerStatus {
	live := map[string]*PeerStatus{}
	ipc, err := s.wg.IpcGet()
	if err != nil {
		return live
	}
	var cur *PeerStatus
	for _, line := range strings.Split(ipc, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if k == "public_key" {
			cur = &PeerStatus{PublicKey: hexToKey(v)}
			live[cur.PublicKey] = cur
			continue
		}
		if cur == nil {
			continue
		}
		switch k {
		case "endpoint":
			// "[::ffff:172.20.10.2]:51820" (how an IPv4 peer looks on a
			// dual-stack socket) becomes "172.20.10.2:51820".
			if ap, err := netip.ParseAddrPort(v); err == nil {
				v = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()).String()
			}
			cur.Endpoint = v
		case "last_handshake_time_sec":
			cur.LastHandshake, _ = strconv.ParseInt(v, 10, 64)
		case "rx_bytes":
			cur.RxBytes, _ = strconv.ParseInt(v, 10, 64)
		case "tx_bytes":
			cur.TxBytes, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return live
}

// statsLoop samples per-device traffic every second for the charts.
func (s *Server) statsLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-t.C:
			counters := map[string]Rates{}
			live := s.livePeers()
			for k, p := range live {
				counters[k] = Rates{p.TxBytes, p.RxBytes} // tx = down to the device
			}
			names := map[string]string{}
			for _, p := range s.Config().Peers {
				names[p.PublicKey] = p.Name
			}
			s.stats.record(now, counters, names)
			if err := s.stats.save(now, false); err != nil {
				s.log.WarnHint("usage", "Usage history may be incomplete; free up space on the iPhone if it's full.", "Couldn't save usage: %v", err)
			}
			s.watchPeers(now, live, names)
		}
	}
}

// History returns per-second traffic samples newer than since (unix seconds).
func (s *Server) History(since int64) []Sample { return s.stats.since(since) }

// Usage returns hourly usage (last 7 days) and the last n days.
func (s *Server) Usage(days int) UsageView { return s.stats.view(time.Now(), days) }

// ClientConfig builds a wg-quick config for a device (the format the
// WireGuard apps import). Empty or zero arguments get the defaults.
func ClientConfig(peerPrivateKey, peerAddress, serverPublicKey, endpointHost string, port int, gateway string, mtu int) string {
	if gateway == "" {
		gateway = defaultGateway
	}
	if mtu == 0 {
		mtu = defaultMTU
	}
	if port == 0 {
		port = defaultPort
	}
	if endpointHost == "" {
		endpointHost = defaultHotspot
	}
	a, err := netip.ParseAddr(strings.TrimSpace(peerAddress))
	if err != nil || !a.Is4() {
		return "" // not a device address: no config rather than a broken one
	}
	address := a.String() + "/32"
	if v6, ok := peerAddr6(a); ok {
		address += ", " + v6.String() + "/128"
	}
	return fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s
DNS = %s
MTU = %d

[Peer]
PublicKey = %s
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = %s
PersistentKeepalive = 25
`, peerPrivateKey, address, gateway, mtu, serverPublicKey, net.JoinHostPort(endpointHost, strconv.Itoa(port)))
}

// copyCounting is io.Copy that adds to total as data flows rather than when
// the connection ends, so the app's live rate and totals include long
// downloads and kept-alive connections. It returns the bytes copied.
func copyCounting(dst io.Writer, src io.Reader, total *atomic.Int64) int64 {
	buf := make([]byte, 32<<10)
	var sum int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			w, werr := dst.Write(buf[:n])
			total.Add(int64(w))
			sum += int64(w)
			if werr != nil {
				return sum
			}
		}
		if err != nil {
			return sum
		}
	}
}
