package engine

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Pairing lets Open Bars on a Mac/iPad add itself as a WireGuard device:
// it makes its own key pair, POSTs the public key here, and the person
// holding the iPhone approves it in the app. Approval is the app adding the
// device (SetPeers with that key), so the device list stays owned by the app
// and survives restarts like any other device. Nothing secret crosses the
// hotspot: the private key never leaves the client.

// PairRequest is a device waiting for approval, shown in the iPhone app.
type PairRequest struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // mac, ipad, iphone, windows, android (for the icon); "" if unknown
	PublicKey string `json:"public_key"`
	From      string `json:"from"` // hotspot address of the requester
	Since     int64  `json:"since"`
}

// PairResponse is everything the client needs to bring its tunnel up.
type PairResponse struct {
	ServerPublicKey string `json:"server_public_key"`
	Endpoint        string `json:"endpoint"` // host:port on the hotspot
	Address         string `json:"address"`  // client's tunnel IPv4
	Address6        string `json:"address6"` // client's tunnel IPv6 (ULA)
	DNS             string `json:"dns"`
	MTU             int    `json:"mtu"`
	HotspotName     string `json:"hotspot_name"`
	DeviceName      string `json:"device_name"`
}

// pairWait is how long a request waits for someone to tap Allow.
var pairWait = 90 * time.Second

type pendingPair struct {
	req    PairRequest
	denied bool
	done   chan struct{}
}

type pairing struct {
	mu      sync.Mutex
	pending map[string]*pendingPair // by public key
}

func (p *pairing) list() []PairRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []PairRequest{}
	for _, pp := range p.pending {
		out = append(out, pp.req)
	}
	return out
}

// add registers (or joins) a pending request for this key.
func (p *pairing) add(req PairRequest) *pendingPair {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil {
		p.pending = map[string]*pendingPair{}
	}
	if pp, ok := p.pending[req.PublicKey]; ok {
		pp.req.Name = req.Name
		return pp
	}
	pp := &pendingPair{req: req, done: make(chan struct{})}
	p.pending[req.PublicKey] = pp
	return pp
}

// resolve finishes requests whose keys are now devices (approved).
func (p *pairing) resolve(peers []PeerConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, peer := range peers {
		if pp, ok := p.pending[peer.PublicKey]; ok {
			close(pp.done)
			delete(p.pending, peer.PublicKey)
		}
	}
}

func (p *pairing) deny(pub string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pp, ok := p.pending[pub]
	if !ok {
		return false
	}
	pp.denied = true
	close(pp.done)
	delete(p.pending, pub)
	return true
}

func (p *pairing) drop(pub string, pp *pendingPair) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending[pub] == pp {
		delete(p.pending, pub)
	}
}

// DenyPairing rejects a waiting request (the app's "Don't Allow").
func (s *Server) DenyPairing(pub string) bool {
	ok := s.pair.deny(pub)
	if ok {
		s.log.Infof("pair", "Request from %s denied in the app", shortKey(pub))
	}
	return ok
}

func validPublicKey(k string) bool {
	b, err := base64.StdEncoding.DecodeString(k)
	return err == nil && len(b) == 32
}

func (s *Server) peerByKey(pub string) (PeerConfig, bool) {
	for _, p := range s.Config().Peers {
		if p.PublicKey == pub {
			return p, true
		}
	}
	return PeerConfig{}, false
}

func (s *Server) pairResponse(r *http.Request, p PeerConfig) (PairResponse, error) {
	cfg := s.Config()
	serverPub, err := PublicKey(cfg.PrivateKey)
	if err != nil {
		return PairResponse{}, err
	}
	resp := PairResponse{
		ServerPublicKey: serverPub,
		Endpoint:        net.JoinHostPort(s.endpointHost(r), strconv.Itoa(cfg.ListenPort)),
		Address:         p.Address,
		DNS:             cfg.Gateway,
		MTU:             cfg.MTU,
		HotspotName:     cfg.HotspotSSID,
		DeviceName:      p.Name,
	}
	if a, err := netip.ParseAddr(p.Address); err == nil {
		if v6, ok := peerAddr6(a); ok {
			resp.Address6 = v6.String()
		}
	}
	return resp, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// webPair: POST {"name","public_key"} asks to become a device and waits for
// approval; GET ?public_key= returns the config if that key is already a
// device (404 if not), without asking.
func (s *Server) webPair(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		pub := strings.TrimSpace(r.URL.Query().Get("public_key"))
		p, ok := s.peerByKey(pub)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not paired"})
			return
		}
		resp, err := s.pairResponse(r, p)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	case http.MethodPost:
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		s.log.Warnf("pair", "Bad pairing request from %s: %v", r.RemoteAddr, err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	req.PublicKey = strings.TrimSpace(req.PublicKey)
	if !validPublicKey(req.PublicKey) {
		s.log.Warnf("pair", "Pairing request from %s has an invalid public key", r.RemoteAddr)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad public key"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Device"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	if p, ok := s.peerByKey(req.PublicKey); ok {
		resp, err := s.pairResponse(r, p)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	from := r.RemoteAddr
	if h, _, err := net.SplitHostPort(from); err == nil {
		from = h
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	switch kind {
	case "mac", "ipad", "iphone", "windows", "android":
	default:
		kind = ""
	}
	pp := s.pair.add(PairRequest{Name: name, Kind: kind, PublicKey: req.PublicKey, From: from, Since: time.Now().Unix()})
	s.log.Infof("pair", "%q (%s) is asking to connect; approve it in the app", name, from)

	t := time.NewTimer(pairWait)
	defer t.Stop()
	select {
	case <-pp.done:
	case <-t.C:
		s.pair.drop(req.PublicKey, pp)
		s.log.Infof("pair", "%q wasn't approved in time", name)
		writeJSON(w, http.StatusRequestTimeout, map[string]string{"error": "not approved in time"})
		return
	case <-r.Context().Done():
		s.pair.drop(req.PublicKey, pp)
		s.log.Infof("pair", "%q stopped waiting for approval", name)
		return
	case <-s.ctx.Done():
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "stopped"})
		return
	}
	if pp.denied {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "denied"})
		return
	}
	p, ok := s.peerByKey(req.PublicKey)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "denied"})
		return
	}
	resp, err := s.pairResponse(r, p)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.log.Infof("pair", "%q approved as %s", p.Name, p.Address)
	writeJSON(w, http.StatusOK, resp)
}

// peerAddr6 gives each device an IPv6 address inside the tunnel, derived
// from its IPv4 one (10.66.66.5 -> fd0b:a75::a42:4205). Clients route all
// IPv6 into the tunnel with it; otherwise their IPv6 traffic would leave
// through the hotspot directly, which is exactly what the tunnel avoids.
func peerAddr6(v4 netip.Addr) (netip.Addr, bool) {
	if !v4.Is4() {
		return netip.Addr{}, false
	}
	b := v4.As4()
	return netip.AddrFrom16([16]byte{0xfd, 0x0b, 0x0a, 0x75, 12: b[0], 13: b[1], 14: b[2], 15: b[3]}), true
}
