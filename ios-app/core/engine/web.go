package engine

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// The config page and Open Bars Connect's endpoints, on the hotspot.
// Connect's endpoints are always on ConnectPort (8080, where Open Bars
// Connect looks); the page is optional and on WebPort:
//   /                the page that hands out .conf files (devices with a key)
//   /conf/<i>/...    device i's .conf
//   /client.json     what this iPhone offers (Connect)
//   /hotspot-check   Connect's USB-hotspot probe
//   /api/pair        pairing (pair.go)

// startWeb starts an HTTP server on port with the config page (page) and/or
// Open Bars Connect's endpoints (connect).
func (s *Server) startWeb(port int, page, connect bool) error {
	ln, err := listenTCP(s, port)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	if page {
		mux.HandleFunc("/", s.webIndex)
		mux.HandleFunc("/conf/", s.webConf)
	}
	if connect {
		mux.HandleFunc("/client.json", s.webClientJSON)
		mux.HandleFunc("/hotspot-check", s.webHotspotCheck)
		mux.HandleFunc("/api/pair", s.webPair)
	}
	// No write timeout: pairing waits up to 90 s for approval.
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(webErrorLog{s.log}, "", 0),
	}
	s.webs = append(s.webs, srv)
	go srv.Serve(ln)
	return nil
}

// webErrorLog sends net/http's own complaints to the log at debug.
type webErrorLog struct{ log *Logger }

func (w webErrorLog) Write(p []byte) (int, error) {
	w.log.Debugf("web", "%s", strings.TrimSpace(string(p)))
	return len(p), nil
}

// webIndex lists the devices that have a downloadable config.
func (s *Server) webIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	cfg := s.Config()
	var rows strings.Builder
	now := time.Now()
	for i, p := range cfg.Peers {
		if !s.confAvailable(p, now) {
			continue
		}
		fmt.Fprintf(&rows, "<li><a href=\"/conf/%d/%s.conf\">%s</a><span>%s</span></li>",
			i, safeName(p.Name), html.EscapeString(p.Name), html.EscapeString(p.Address))
	}
	if rows.Len() == 0 {
		rows.WriteString("<li class=muted>Nothing to download right now. Each config can be downloaded once, within a few minutes of adding the device in Open Bars on the iPhone. To download one again, open the device there and tap Allow Download.</li>")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, indexPage, rows.String(), html.EscapeString(s.endpointHost(r)), cfg.ListenPort)
}

// indexPage follows the viewer's light/dark setting (prefers-color-scheme),
// with iOS system colors. Args: device rows, endpoint host, port.
const indexPage = `<!doctype html><html><head>
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Open Bars</title><style>
:root{color-scheme:light dark;--bg:#f2f2f7;--card:#fff;--text:#1c1c1e;--muted:#6c6c70;--link:#007aff;--green:#34c759;--line:#d1d1d6}
@media (prefers-color-scheme:dark){:root{--bg:#000;--card:#1c1c1e;--text:#f2f2f7;--muted:#98989f;--link:#0a84ff;--green:#30d158;--line:#38383a}}
body{font:16px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:var(--bg);color:var(--text);
max-width:560px;margin:0 auto;padding:24px 16px}
h1{font-size:28px;margin:0 0 16px;display:flex;align-items:center;gap:10px}
h1 i{display:inline-block;width:12px;height:12px;border-radius:50%%;background:var(--green)}
h2{font-size:13px;font-weight:600;text-transform:uppercase;letter-spacing:.04em;color:var(--muted);margin:24px 0 6px 16px}
.card{background:var(--card);border-radius:12px;padding:12px 16px}
.card p{margin:.3em 0}ul,ol{margin:0;padding-left:1.2em}li{margin:.45em 0}
a{color:var(--link);font-weight:600;text-decoration:none}a:hover{text-decoration:underline}
span,.muted{color:var(--muted)}span{margin-left:.5em}
footer{color:var(--muted);font-size:13px;margin:20px 16px}
</style></head><body>
<h1><i></i>Open Bars</h1>
<h2>Mac, iPad or iPhone</h2>
<div class=card><p>Install <b>Open Bars</b>, open its <i>Connect</i> tab and tap <i>Pair with iPhone</i>,
then tap <i>Allow</i> on this iPhone. Its VPN turns on by itself whenever it's on this hotspot.</p></div>
<h2>Other devices (WireGuard app)</h2>
<div class=card><p>Download the config for <b>this</b> device:</p><ul>%s</ul></div>
<h2>Windows</h2>
<div class=card><ol><li>Install WireGuard from wireguard.com (one time).</li>
<li>In the WireGuard app, choose <i>Import tunnel(s) from file</i> and open the downloaded .conf.</li>
<li>Click <i>Activate</i> whenever you're on the hotspot.</li></ol></div>
<footer>Endpoint: %s:%d</footer>
</body></html>`

// webClientJSON tells Open Bars Connect what this iPhone offers.
func (s *Server) webClientJSON(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"hotspot_name":   cfg.HotspotSSID,
		"pairing":        true, // POST /api/pair
		"wireguard_port": cfg.ListenPort,
	})
}

// webHotspotCheck answers Open Bars Connect's on-demand probe on a USB
// hotspot (no SSID to match): 200 means this is an Open Bars iPhone that is
// sharing, so the tunnel should come up.
func (s *Server) webHotspotCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte("ok"))
}

// webConf serves /conf/<index>[/<name>.conf]: the .conf of the device at
// that position in the device list, if it has a private key.
func (s *Server) webConf(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/conf/")
	idxStr, _, _ := strings.Cut(rest, "/")
	i, err := strconv.Atoi(idxStr)
	cfg := s.Config()
	if err != nil || i < 0 || i >= len(cfg.Peers) || !s.confAvailable(cfg.Peers[i], time.Now()) {
		http.NotFound(w, r)
		return
	}
	p := cfg.Peers[i]
	serverPub, err := PublicKey(cfg.PrivateKey)
	if err != nil {
		http.Error(w, "server key unavailable", http.StatusInternalServerError)
		return
	}
	conf := ClientConfig(p.PrivateKey, p.Address, serverPub, s.endpointHost(r), cfg.ListenPort, cfg.Gateway, cfg.MTU)
	if conf == "" {
		http.Error(w, "this device has no valid tunnel address", http.StatusInternalServerError)
		return
	}
	name := safeName(p.Name)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".conf"))
	w.Write([]byte(conf))
	s.confMu.Lock()
	if s.confTaken == nil {
		s.confTaken = map[string]int64{}
	}
	s.confTaken[p.PublicKey] = p.ConfUntil
	s.confMu.Unlock()
	s.log.Infof("web", "Config for %q downloaded from %s; the config page won't offer it again", p.Name, remoteHost(r))
}

// confAvailable: a WireGuard-app device's .conf is on the config page once,
// until its ConfUntil. Otherwise anyone on the hotspot could take another
// device's key, and with it that device's (maybe unlimited) access.
func (s *Server) confAvailable(p PeerConfig, now time.Time) bool {
	if p.PrivateKey == "" || p.ConfUntil <= now.Unix() {
		return false
	}
	s.confMu.Lock()
	defer s.confMu.Unlock()
	return s.confTaken[p.PublicKey] != p.ConfUntil
}

// safeName keeps ASCII letters, digits, '-' and '_', turns spaces into
// '-', and drops the rest ("device" if nothing is left).
func safeName(name string) string {
	var b strings.Builder
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteRune(c)
		case c == ' ':
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "device"
	}
	return b.String()
}

// endpointHost is the address a device should use as its WireGuard
// endpoint: the address it reached this page at, unless that was the
// gateway, loopback or a name; then the hotspot address.
func (s *Server) endpointHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if a, err := netip.ParseAddr(host); err == nil {
		a = a.Unmap()
		if a != s.gw && !a.IsLoopback() {
			return a.WithZone("").String()
		}
	}
	if ni := s.netInfo.Load(); ni != nil && ni.HotspotIP != "" {
		return ni.HotspotIP
	}
	return defaultHotspot
}

func remoteHost(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}
