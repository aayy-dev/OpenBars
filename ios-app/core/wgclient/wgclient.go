// Package wgclient is the gomobile API for Open Bars Connect's tunnel
// extension: a Mac, iPad or iPhone using an Open Bars hotspot. It runs
// wireguard-go on the utun interface that the packet tunnel provider made,
// talking to the WireGuard server in Open Bars on the hotspot iPhone.
//
// In Swift: WgclientStart(_:_:), WgclientStop(), WgclientStatusJSON(), ...
package wgclient

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/aayy-dev/OpenBars/ios-app/core/internal/applog"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// Settings is what the app stores after pairing (JSON).
type Settings struct {
	PrivateKey      string `json:"private_key"`       // this device, base64
	ServerPublicKey string `json:"server_public_key"` // the iPhone, base64
	Endpoint        string `json:"endpoint"`          // e.g. 172.20.10.1:51820
	Keepalive       int    `json:"keepalive"`         // seconds, default 25
}

var (
	mu      sync.Mutex
	dev     *device.Device
	started time.Time
	// logs lives as long as the extension process.
	logs = applog.New(1000)
	// watchStop ends the current tunnel's handshake watchdog.
	watchStop chan struct{}
)

// keyHex turns a base64 WireGuard key (44 characters, 32 bytes) into the
// hex form wireguard-go's configuration protocol uses.
func keyHex(key string) (string, error) {
	key = strings.TrimSpace(key)
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(key) != 44 {
		return "", errors.New("invalid key")
	}
	return hex.EncodeToString(raw), nil
}

// Start brings WireGuard up on the provider's utun. Call it after
// setTunnelNetworkSettings has completed.
func Start(settingsJSON string) error {
	var s Settings
	if err := json.Unmarshal([]byte(settingsJSON), &s); err != nil {
		return fmt.Errorf("bad settings: %w", err)
	}
	priv, err := keyHex(s.PrivateKey)
	if err != nil {
		return errors.New("this device's key is invalid; pair again")
	}
	pub, err := keyHex(s.ServerPublicKey)
	if err != nil {
		return errors.New("the iPhone's key is invalid; pair again")
	}
	ep, err := netip.ParseAddrPort(strings.TrimSpace(s.Endpoint))
	if err != nil {
		return fmt.Errorf("bad endpoint %q", s.Endpoint)
	}
	if s.Keepalive <= 0 {
		s.Keepalive = 25
	}

	mu.Lock()
	defer mu.Unlock()
	if dev != nil {
		stopLocked()
	}
	if runtime.GOOS == "ios" {
		// iOS caps Network Extension memory; GC before reaching it.
		debug.SetMemoryLimit(20 << 20)
	}

	fd, err := tunFD()
	if err != nil {
		logs.ErrorHint("wg", "Turn the VPN off and on again.", "Can't find the tunnel interface: %v", err)
		return err
	}
	dup, err := unix.Dup(fd)
	if err != nil {
		return fmt.Errorf("dup tunnel fd: %w", err)
	}
	if err := unix.SetNonblock(dup, true); err != nil {
		unix.Close(dup)
		return fmt.Errorf("tunnel fd: %w", err)
	}
	t, err := tun.CreateTUNFromFile(os.NewFile(uintptr(dup), "/dev/tun"), 0)
	if err != nil {
		unix.Close(dup)
		return fmt.Errorf("open tunnel: %w", err)
	}
	name, _ := t.Name()

	d := device.NewDevice(t, conn.NewStdNetBind(), newWGLogger())
	ipc := fmt.Sprintf("private_key=%s\nreplace_peers=true\npublic_key=%s\nendpoint=%s\n"+
		"persistent_keepalive_interval=%d\nreplace_allowed_ips=true\nallowed_ip=0.0.0.0/0\nallowed_ip=::/0\n",
		priv, pub, ep, s.Keepalive)
	if err := d.IpcSet(ipc); err != nil {
		d.Close()
		return fmt.Errorf("configure WireGuard: %w", err)
	}
	if err := d.Up(); err != nil {
		d.Close()
		return fmt.Errorf("start WireGuard: %w", err)
	}
	if runtime.GOOS == "ios" {
		d.DisableSomeRoamingForBrokenMobileSemantics()
	}
	dev = d
	started = time.Now()
	hist.start(func() (int64, int64) {
		_, _, rx, tx := peerState(d)
		return rx, tx
	})
	logs.Infof("wg", "WireGuard up on %s → iPhone at %s (keepalive %d s)", name, ep, s.Keepalive)
	watchStop = make(chan struct{})
	go watchHandshake(d, watchStop, time.Now())
	return nil
}

// Stop tears the tunnel down (no-op if it isn't running).
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if dev != nil {
		stopLocked()
		logs.Infof("wg", "WireGuard stopped")
	}
}

// stopLocked closes the device. Caller holds mu.
func stopLocked() {
	if watchStop != nil {
		close(watchStop)
		watchStop = nil
	}
	hist.halt()
	dev.Close()
	dev = nil
}

// Bump rebinds the UDP socket and pokes the iPhone after a network change
// or wake from sleep, so the tunnel recovers right away.
func Bump() {
	mu.Lock()
	d := dev
	mu.Unlock()
	if d == nil {
		return
	}
	if err := d.BindUpdate(); err != nil {
		logs.WarnHint("wg", "The tunnel may stall until the network settles.", "Rebind after network change failed: %v", err)
	}
	d.SendKeepalivesToPeersWithCurrentKeypair()
}

// Status describes the tunnel for the Connect app (ConnectController's
// TunnelStatus decodes these keys).
type Status struct {
	Running bool  `json:"running"`
	Uptime  int64 `json:"uptime"` // seconds since Start
	// Where the iPhone answers from, and when the last handshake with it
	// completed (Unix time; 0 before the first).
	Endpoint      string `json:"endpoint"`
	LastHandshake int64  `json:"last_handshake"`
	// Bytes received from and sent to the iPhone.
	RxBytes int64 `json:"rx_bytes"`
	TxBytes int64 `json:"tx_bytes"`
}

// StatusJSON returns Status as JSON.
func StatusJSON() string {
	mu.Lock()
	d := dev
	st := Status{Running: d != nil}
	if d != nil {
		st.Uptime = int64(time.Since(started).Seconds())
	}
	mu.Unlock()
	if d != nil {
		st.Endpoint, st.LastHandshake, st.RxBytes, st.TxBytes = peerState(d)
	}
	b, _ := json.Marshal(st)
	return string(b)
}

// LogsJSON returns log entries newer than after (a sequence number; 0 for
// all): {"next","first","boot","entries":[{"seq","t","lvl","src","msg",
// "hint","rep"}]}. t is Unix milliseconds. Pass the returned next as after
// on the next call.
func LogsJSON(after int64) string { return logs.JSON(after) }

// Log adds an entry from the Swift side: level is debug, info, warn or
// error (anything else counts as info), src a short component name, hint
// an optional suggestion.
func Log(level, src, msg, hint string) {
	logs.Add(applog.ParseLevel(level), src, msg, hint)
}

// SetDetailedLogging turns debug entries on or off.
func SetDetailedLogging(on bool) {
	if on == logs.Detailed() {
		return
	}
	logs.SetDetailed(on)
	if on {
		logs.Infof("wg", "Detailed logging on")
	} else {
		logs.Infof("wg", "Detailed logging off")
	}
}

// ClearLogs empties the log. Sequence numbers keep counting.
func ClearLogs() { logs.Clear() }

// tunFD finds the utun control socket NEPacketTunnelProvider opened in this
// process (the same scan WireGuard's own Apple apps do).
func tunFD() (int, error) {
	var info unix.CtlInfo
	copy(info.Name[:], "com.apple.net.utun_control")
	for fd := 0; fd < 1024; fd++ {
		sa, err := unix.Getpeername(fd)
		if err != nil {
			continue
		}
		ctl, ok := sa.(*unix.SockaddrCtl)
		if !ok {
			continue
		}
		if info.Id == 0 {
			if err := unix.IoctlCtlInfo(fd, &info); err != nil {
				continue
			}
		}
		if ctl.ID == info.Id {
			return fd, nil
		}
	}
	return -1, errors.New("no utun socket in this process")
}
