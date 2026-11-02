package engine

import (
	"fmt"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/device"
)

// wireguard-go's own log: verbose lines are debug, except that the first
// handshake line per device per session is info; its errors are warnings.
// Handshakes from devices whose key we don't know (or that were set up for
// another key of this phone) are counted and summarized.

const quietAfter = 3 * time.Minute

func (s *Server) wgLogger() *device.Logger {
	return &device.Logger{Verbosef: s.wgVerbose, Errorf: s.wgError}
}

func (s *Server) wgVerbose(format string, args ...any) {
	switch {
	case strings.HasPrefix(format, "Received invalid initiation message from"):
		from := ""
		if len(args) > 0 {
			from = fmt.Sprint(args[0])
		}
		s.fails.add(s.log, "wg", reasonUnknownKey, from, nil)
		return
	case format == "Received packet with invalid mac1":
		s.fails.add(s.log, "wg", reasonWrongServer, "", nil)
		return
	case strings.Contains(format, "andshake"):
		line := fmt.Sprintf(format, args...)
		tok := peerToken(line)
		s.wgSeenMu.Lock()
		first := tok != "" && !s.wgSeen[tok]
		if first {
			s.wgSeen[tok] = true
		}
		s.wgSeenMu.Unlock()
		if first {
			s.log.Infof("wg", "%s", s.namePeers(line))
		} else {
			s.log.Debugf("wg", "%s", s.namePeers(line))
		}
		return
	}
	if s.log.Detailed() {
		s.log.Debugf("wg", "%s", s.namePeers(fmt.Sprintf(format, args...)))
	}
}

func (s *Server) wgError(format string, args ...any) {
	s.log.Warnf("wg", "%s", s.namePeers(fmt.Sprintf(format, args...)))
}

// peerToken finds wireguard-go's "peer(AbCd…WxYz)" in a line.
func peerToken(line string) string {
	i := strings.Index(line, "peer(")
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(line[i:], ')')
	if j < 0 {
		return ""
	}
	return line[i : i+j+1]
}

// wgToken is how wireguard-go abbreviates a base64 public key.
func wgToken(pub string) string {
	if len(pub) != 44 {
		return ""
	}
	return "peer(" + pub[:4] + "…" + pub[39:43] + ")"
}

// namePeers replaces wireguard-go's peer token with the device's name.
func (s *Server) namePeers(line string) string {
	tok := peerToken(line)
	if tok == "" {
		return line
	}
	for _, p := range s.Config().Peers {
		if wgToken(strings.TrimSpace(p.PublicKey)) == tok {
			return strings.Replace(line, tok, fmt.Sprintf("%s (%s)", p.Name, shortKey(p.PublicKey)), 1)
		}
	}
	return line
}

// peerWatch tracks one device's handshakes to notice it going quiet.
type peerWatch struct {
	hs    int64 // last handshake seen, unix seconds
	quiet bool
}

// watchPeers runs every second from statsLoop (its only caller, so no
// lock): a device that had a handshake and then none for 3 minutes went
// quiet; its next handshake brings it back.
func (s *Server) watchPeers(now time.Time, live map[string]*PeerStatus, names map[string]string) {
	for key, name := range names {
		w := s.watch[key]
		if w == nil {
			w = &peerWatch{}
			s.watch[key] = w
		}
		var hs int64
		var endpoint string
		if l, ok := live[key]; ok {
			hs, endpoint = l.LastHandshake, l.Endpoint
		}
		if hs > 0 && hs != w.hs {
			if w.quiet {
				w.quiet = false
				s.log.Infof("wg", "%s (%s) is back, from %s", name, shortKey(key), orNone(endpoint))
			}
			w.hs = hs
		}
		if w.hs > 0 && !w.quiet && now.Unix()-w.hs > int64(quietAfter/time.Second) {
			w.quiet = true
			s.log.Infof("wg", "%s (%s) went quiet: no handshake for 3 minutes", name, shortKey(key))
		}
	}
	for key := range s.watch {
		if _, ok := names[key]; !ok {
			delete(s.watch, key)
		}
	}
}
