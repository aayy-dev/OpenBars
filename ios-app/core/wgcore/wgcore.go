// Package wgcore is the gomobile API of the Open Bars engine, used by the
// iPhone app (sharing in the app) and its packet tunnel extension
// (sharing in the background). Everything goes through plain functions over
// strings and JSON; in Swift they're WgcoreStart(_:_:), WgcoreStop(), ...
package wgcore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"

	"github.com/aayy-dev/OpenBars/ios-app/core/engine"
	"github.com/aayy-dev/OpenBars/ios-app/core/internal/applog"
)

var (
	mu  sync.Mutex
	srv *engine.Server
	// logs lives as long as the process: Start and Stop don't clear it.
	logs = engine.NewLog(3000)
)

// Start starts sharing with a JSON config, stopping any running engine
// first. A config that doesn't decode leaves a running engine alone.
func Start(configJSON string) error {
	var cfg engine.Config
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		err = fmt.Errorf("bad config: %v", err)
		logs.Errorf("share", "Couldn't start sharing: %v", err)
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if srv != nil {
		srv.Stop()
		srv = nil
	}
	s, err := engine.Start(cfg, logs)
	if err != nil {
		logs.ErrorHint("share", startHint(err, cfg), "Couldn't start sharing: %v", err)
		return err
	}
	srv = s
	return nil
}

func startHint(err error, cfg engine.Config) string {
	msg := err.Error()
	switch {
	case errors.Is(err, syscall.EADDRINUSE) || strings.Contains(msg, "address already in use"):
		port := cfg.ListenPort
		if port == 0 {
			port = 51820
		}
		return fmt.Sprintf("Something else is using UDP port %d (maybe Open Bars in the other mode). Change the UDP port in Settings, or restart Open Bars.", port)
	case strings.Contains(msg, "private key"):
		return "Open Bars' own key is damaged. Reinstalling or resetting Open Bars makes a new one (devices must be added again)."
	case strings.HasPrefix(msg, "bad config"):
		return "This is a bug in Open Bars; please report it with this log."
	}
	return "Try again, or restart Open Bars."
}

// Stop stops sharing (no-op if stopped).
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if srv != nil {
		srv.Stop()
		srv = nil
	}
}

// Running reports whether the engine is running.
func Running() bool {
	mu.Lock()
	defer mu.Unlock()
	return srv != nil
}

// SetPeers replaces the device list (JSON array of devices) without
// dropping connected devices whose entry didn't change. Bad JSON is an
// error even when stopped; when stopped it's otherwise a no-op.
func SetPeers(peersJSON string) error {
	var peers []engine.PeerConfig
	if err := json.Unmarshal([]byte(peersJSON), &peers); err != nil {
		return fmt.Errorf("bad devices: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.SetPeers(peers)
}

// StatusJSON returns the engine's status; when stopped, only "net" is
// filled in.
func StatusJSON() string {
	mu.Lock()
	s := srv
	mu.Unlock()
	var st engine.Status
	if s != nil {
		st = s.Status()
	} else {
		st.Net = engine.DetectNetInfo()
	}
	b, _ := json.Marshal(st)
	return string(b)
}

// NetInfoJSON returns the phone's interfaces as the engine sees them.
func NetInfoJSON() string {
	b, _ := json.Marshal(engine.DetectNetInfo())
	return string(b)
}

// LogsJSON returns log entries newer than after (a sequence number; 0 for
// all): {"next","first","boot","entries":[{"seq","t","lvl","src","msg",
// "hint","rep"}]}. t is Unix milliseconds. Pass the returned next as after
// on the next call.
func LogsJSON(after int64) string { return logs.JSON(after) }

// Log adds an entry from the app: level is debug, info, warn or error
// (anything else counts as info), src a short component name, hint an
// optional suggestion.
func Log(level, src, msg, hint string) {
	logs.Add(applog.ParseLevel(level), src, msg, hint)
}

// SetDetailedLogging turns debug entries (per-connection detail) on or off.
func SetDetailedLogging(on bool) {
	if on == logs.Detailed() {
		return
	}
	logs.SetDetailed(on)
	if on {
		logs.Infof("share", "Detailed logging on")
	} else {
		logs.Infof("share", "Detailed logging off")
	}
}

// ClearLogs empties the log. Sequence numbers keep counting.
func ClearLogs() { logs.Clear() }

// GeneratePrivateKey returns a new base64 WireGuard private key ("" on
// failure).
func GeneratePrivateKey() string {
	k, err := engine.GenerateKey()
	if err != nil {
		return ""
	}
	return k
}

// PublicKey returns the base64 public key for a private key, or "" if the
// private key is invalid.
func PublicKey(privateKey string) string {
	k, err := engine.PublicKey(privateKey)
	if err != nil {
		return ""
	}
	return k
}

// ClientConfig returns a wg-quick .conf for a WireGuard-app device.
// Empty or zero arguments get the defaults (port 51820, gateway
// 10.66.66.1, MTU 1420, endpoint host 172.20.10.1).
func ClientConfig(peerPrivateKey, peerAddress, serverPublicKey, endpointHost string, port int, gateway string, mtu int) string {
	return engine.ClientConfig(peerPrivateKey, peerAddress, serverPublicKey, endpointHost, port, gateway, mtu)
}

// HistoryJSON returns per-second traffic samples newer than since (unix
// seconds) as a JSON array ("[]" when stopped).
func HistoryJSON(since int64) string {
	mu.Lock()
	s := srv
	mu.Unlock()
	if s == nil {
		return "[]"
	}
	b, _ := json.Marshal(s.History(since))
	return string(b)
}

// UsageJSON returns hourly usage (last 7 days) and the last n days as JSON
// ("" when stopped).
func UsageJSON(days int) string {
	mu.Lock()
	s := srv
	mu.Unlock()
	if s == nil {
		return ""
	}
	b, _ := json.Marshal(s.Usage(days))
	return string(b)
}

// ReadUsageJSON reads saved usage while sharing is off (same shape as
// UsageJSON).
func ReadUsageJSON(path, timeZone string, days int) string {
	b, _ := json.Marshal(engine.ReadUsage(path, timeZone, days))
	return string(b)
}

// DenyPairing rejects a device waiting for approval (base64 public key).
func DenyPairing(publicKey string) bool {
	mu.Lock()
	s := srv
	mu.Unlock()
	if s == nil {
		return false
	}
	return s.DenyPairing(publicKey)
}
