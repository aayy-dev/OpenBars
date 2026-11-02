package wgclient

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/device"
)

// wireguard-go's log for the Connect tunnel: verbose lines are debug, the
// first handshake line of a tunnel is info, "stopped hearing back" is a
// warning, and errors are warnings.

func newWGLogger() *device.Logger {
	var mu sync.Mutex
	seen := false
	return &device.Logger{
		Verbosef: func(format string, args ...any) {
			switch {
			case strings.Contains(format, "stopped hearing back"):
				logs.WarnHint("wg", "Is the iPhone still sharing, and is this device on its hotspot?",
					"No reply from the iPhone: %s", stripPeer(fmt.Sprintf(format, args...)))
			case strings.Contains(format, "andshake"):
				mu.Lock()
				first := !seen
				seen = true
				mu.Unlock()
				if first {
					logs.Infof("wg", "%s", stripPeer(fmt.Sprintf(format, args...)))
				} else {
					logs.Debugf("wg", "%s", stripPeer(fmt.Sprintf(format, args...)))
				}
			default:
				if logs.Detailed() {
					logs.Debugf("wg", "%s", fmt.Sprintf(format, args...))
				}
			}
		},
		Errorf: func(format string, args ...any) {
			logs.Warnf("wg", "%s", fmt.Sprintf(format, args...))
		},
	}
}

// stripPeer drops wireguard-go's "peer(AbCd…WxYz) - " prefix: there is
// only one peer, the iPhone.
func stripPeer(line string) string {
	if strings.HasPrefix(line, "peer(") {
		if i := strings.Index(line, ") - "); i > 0 {
			return line[i+4:]
		}
	}
	return line
}

const firstHandshakeWait = 30 * time.Second

// watchHandshake logs the first handshake with the iPhone, or a warning if
// there is none within 30 s of start.
func watchHandshake(d *device.Device, stop <-chan struct{}, start time.Time) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	warned := false
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			ep, hs, _, _ := peerState(d)
			if hs > 0 {
				logs.Infof("wg", "Connected to the iPhone at %s (first handshake after %s)", ep, now.Sub(start).Round(time.Second))
				return
			}
			if !warned && now.Sub(start) >= firstHandshakeWait {
				warned = true
				logs.WarnHint("wg", "Check that the iPhone is sharing in Open Bars and that the UDP port matches (pair again if it changed).",
					"No handshake with the iPhone after 30 s (endpoint %s)", orNone(ep))
			}
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
