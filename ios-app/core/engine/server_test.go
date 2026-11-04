package engine

import (
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// Ports used here: 51981-51985, 18081-18082 (see the spec's list of fixed
// ports already taken by other tests).

func newKeyPair(t *testing.T) (priv, pub string) {
	t.Helper()
	priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub, err = PublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func TestStartErrors(t *testing.T) {
	priv, _ := newKeyPair(t)
	if _, err := Start(Config{PrivateKey: priv, ListenPort: 51981, Gateway: "fd00::1"}, NewLog(20)); err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Fatalf("v6 gateway: %v", err)
	}
	if _, err := Start(Config{PrivateKey: "nope", ListenPort: 51981}, NewLog(20)); err == nil || !strings.HasPrefix(err.Error(), "server private key: ") || strings.Contains(err.Error(), "nope") {
		t.Fatalf("bad key: %v", err)
	}
	busy, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 51982})
	if err != nil {
		t.Skip(err)
	}
	defer busy.Close()
	if _, err := Start(Config{PrivateKey: priv, ListenPort: 51982}, NewLog(20)); err == nil || !strings.HasPrefix(err.Error(), "listen on UDP 51982: ") {
		t.Fatalf("port in use: %v", err)
	}
}

// The app restarts the engine on every settings change, on the same ports.
func TestRestartSamePorts(t *testing.T) {
	priv, _ := newKeyPair(t)
	cfg := Config{PrivateKey: priv, ListenPort: 51983, WebPort: 18081, ConnectPort: 18081}
	for i := 0; i < 3; i++ {
		srv, err := Start(cfg, NewLog(50))
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		if st := srv.Status(); st.WebError != "" {
			t.Fatalf("web: %s", st.WebError)
		}
		srv.Stop()
		srv.Stop() // idempotent
	}
	ln, err := net.Listen("tcp", ":18081")
	if err != nil {
		t.Fatalf("web port held after Stop: %v", err)
	}
	ln.Close()
	u, err := net.ListenUDP("udp", &net.UDPAddr{Port: 51983})
	if err != nil {
		t.Fatalf("UDP port held after Stop: %v", err)
	}
	u.Close()
}

func uapiPeers(t *testing.T, s *Server) map[string][]string {
	t.Helper()
	ipc, err := s.wg.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	var cur string
	for _, line := range strings.Split(ipc, "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "public_key":
			cur = hexToKey(v)
			out[cur] = nil
		case "allowed_ip":
			out[cur] = append(out[cur], v)
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

func TestSetPeersDiff(t *testing.T) {
	priv, _ := newKeyPair(t)
	_, pa := newKeyPair(t)
	_, pb := newKeyPair(t)
	log := NewLog(100)
	srv, err := Start(Config{PrivateKey: priv, ListenPort: 51984, Peers: []PeerConfig{
		{Name: "a", PublicKey: pa, Address: "10.66.66.2"},
		{Name: "bad key", PublicKey: "xyz", Address: "10.66.66.9"},
		{Name: "bad addr", PublicKey: pb, Address: "not-an-ip"},
	}}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	got := uapiPeers(t, srv)
	want := map[string][]string{pa: {"10.66.66.2/32", "fd0b:a75::a42:4202/128"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("start: %v", got)
	}
	if !strings.Contains(log.Tail(), `Skipping device "bad key"`) || !strings.Contains(log.Tail(), `Skipping device "bad addr"`) {
		t.Fatal(log.Tail())
	}

	// Rename only: nothing sent to WireGuard, but the stored config changes.
	before, _ := srv.wg.IpcGet()
	if err := srv.SetPeers([]PeerConfig{{Name: "renamed", PublicKey: pa, Address: "10.66.66.2"}}); err != nil {
		t.Fatal(err)
	}
	after, _ := srv.wg.IpcGet()
	if before != after || srv.Config().Peers[0].Name != "renamed" {
		t.Fatal("rename touched WireGuard")
	}

	// Re-address one, add another (duplicate key: the last entry wins),
	// and an invalid one is skipped without an error.
	err = srv.SetPeers([]PeerConfig{
		{Name: "renamed", PublicKey: pa, Address: "10.66.66.5"},
		{Name: "b", PublicKey: pb, Address: "10.66.66.3"},
		{Name: "b", PublicKey: pb, Address: "10.66.66.4"},
		{Name: "junk", PublicKey: "", Address: "10.66.66.6"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got = uapiPeers(t, srv)
	want = map[string][]string{
		pa: {"10.66.66.5/32", "fd0b:a75::a42:4205/128"},
		pb: {"10.66.66.4/32", "fd0b:a75::a42:4204/128"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diff: %v", got)
	}
	if n := len(srv.Config().Peers); n != 4 {
		t.Fatalf("stored %d peers; the app's list is stored as given", n)
	}

	// Remove all.
	if err := srv.SetPeers(nil); err != nil {
		t.Fatal(err)
	}
	if got := uapiPeers(t, srv); len(got) != 0 {
		t.Fatalf("left: %v", got)
	}
	if st := srv.Status(); st.Peers != nil {
		t.Fatalf("no devices: peers %v", st.Peers)
	}
}

// A waiting pairing request is woken by SetPeers and must find its key in
// Config().Peers at once, or it answers 403.
func TestSetPeersStoresBeforeResolving(t *testing.T) {
	priv, _ := newKeyPair(t)
	srv, err := Start(Config{PrivateKey: priv, ListenPort: 51985}, NewLog(500))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	var peers []PeerConfig
	for i := 0; i < 30; i++ {
		_, pub := newKeyPair(t)
		pp, _ := srv.pair.add(PairRequest{Name: "x", PublicKey: pub})
		found := make(chan bool, 1)
		go func() {
			<-pp.done
			_, ok := srv.peerByKey(pub)
			found <- ok
		}()
		peers = append(peers, PeerConfig{Name: fmt.Sprint(i), PublicKey: pub, Address: fmt.Sprintf("10.66.66.%d", i+2)})
		if err := srv.SetPeers(peers); err != nil {
			t.Fatal(err)
		}
		if !<-found {
			t.Fatalf("round %d: woken before the device was stored", i)
		}
	}
}

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// The app's decoder fails on any missing key, so every key must always be
// there, even when empty.
func TestJSONKeysAlwaysPresent(t *testing.T) {
	status := []string{"asks", "bytes_down", "bytes_up", "cap", "egress_if", "listen_port", "net", "pairing", "peers", "running", "tcp_active", "tcp_total", "udp_flows", "uptime", "web_error"}
	if got := jsonKeys(t, Status{}); !reflect.DeepEqual(got, status) {
		t.Fatalf("Status keys %v", got)
	}
	netKeys := []string{"cellular_if", "cellular_ip", "default_if", "hotspot_ip", "vpn_active", "vpn_if", "wifi_ip"}
	if got := jsonKeys(t, NetInfo{CellularIdx: 3}); !reflect.DeepEqual(got, netKeys) {
		t.Fatalf("NetInfo keys %v", got)
	}
	peer := []string{"address", "endpoint", "last_handshake", "last_seen", "limit", "name", "public_key", "rx_bytes", "tx_bytes"}
	if got := jsonKeys(t, PeerStatus{}); !reflect.DeepEqual(got, peer) {
		t.Fatalf("PeerStatus keys %v", got)
	}
	pr := []string{"from", "kind", "name", "public_key", "since"}
	if got := jsonKeys(t, PairRequest{}); !reflect.DeepEqual(got, pr) {
		t.Fatalf("PairRequest keys %v", got)
	}
	// A device's private key is only written when it has one.
	if got := jsonKeys(t, PeerConfig{}); !reflect.DeepEqual(got, []string{"address", "name", "public_key"}) {
		t.Fatalf("PeerConfig keys %v", got)
	}
	var cfg Config
	if err := json.Unmarshal([]byte(`{"private_key":"k","listen_port":1,"bind_cellular":true,"dns":["1.1.1.1"],"web_port":8080,
		"peers":[{"name":"n","public_key":"p","private_key":"q","address":"10.66.66.2"}],"hotspot_ssid":"s","low_memory":true,
		"usage_file":"/u","time_zone":"UTC","gateway":"10.66.66.1","mtu":1400,"unknown":1}`), &cfg); err != nil {
		t.Fatal(err)
	}
	want := Config{PrivateKey: "k", ListenPort: 1, Gateway: "10.66.66.1", MTU: 1400, BindCellular: true, DNS: []string{"1.1.1.1"}, WebPort: 8080,
		Peers:       []PeerConfig{{Name: "n", PublicKey: "p", PrivateKey: "q", Address: "10.66.66.2"}},
		HotspotSSID: "s", UsageFile: "/u", TimeZone: "UTC", LowMemory: true}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("config %+v", cfg)
	}
}

func TestParseDNS(t *testing.T) {
	got := parseDNS([]string{" 1.1.1.1 ", "9.9.9.9:5353", "[2606:4700::1111]:53", "2001:db8::1", "bogus", "", "::ffff:8.8.8.8"})
	var s []string
	for _, a := range got {
		s = append(s, a.String())
	}
	want := []string{"1.1.1.1:53", "9.9.9.9:5353", "[2606:4700::1111]:53", "[2001:db8::1]:53", "8.8.8.8:53"}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("%v", s)
	}
}

func TestWatchPeers(t *testing.T) {
	log := NewLog(50)
	s := &Server{log: log, watch: map[string]*peerWatch{}}
	names := map[string]string{"KEY1234567890": "Gaming PC"}
	t0 := time.Unix(1_800_000_000, 0)
	live := func(hs int64) map[string]*PeerStatus {
		return map[string]*PeerStatus{"KEY1234567890": {LastHandshake: hs, Endpoint: "172.20.10.3:51820"}}
	}
	s.watchPeers(t0, live(0), names) // never connected: nothing
	s.watchPeers(t0.Add(10*time.Minute), live(0), names)
	if log.Tail() != "" {
		t.Fatal(log.Tail())
	}
	s.watchPeers(t0, live(t0.Unix()), names)
	s.watchPeers(t0.Add(179*time.Second), live(t0.Unix()), names)
	if log.Tail() != "" {
		t.Fatal("quiet too early")
	}
	s.watchPeers(t0.Add(181*time.Second), live(t0.Unix()), names)
	s.watchPeers(t0.Add(300*time.Second), live(t0.Unix()), names)
	if strings.Count(log.Tail(), "went quiet") != 1 {
		t.Fatal(log.Tail())
	}
	s.watchPeers(t0.Add(400*time.Second), live(t0.Unix()+400), names)
	if !strings.Contains(log.Tail(), "Gaming PC (KEY12345…) is back, from 172.20.10.3:51820") {
		t.Fatal(log.Tail())
	}
	s.watchPeers(t0.Add(401*time.Second), live(t0.Unix()+400), nil)
	if len(s.watch) != 0 {
		t.Fatal("removed device still watched")
	}
}

func TestWGLogLines(t *testing.T) {
	_, pub := newKeyPair(t)
	log := NewLog(50)
	s := &Server{log: log, cfg: Config{Peers: []PeerConfig{{Name: "Mac", PublicKey: pub}}}, wgSeen: map[string]bool{}}
	tok := wgToken(pub)
	if tok == "" || peerToken("x "+tok+" - y") != tok {
		t.Fatal(tok)
	}
	s.wgVerbose("%v - Received handshake initiation", tok)
	s.wgVerbose("%v - Sending handshake response", tok)
	s.wgVerbose("Routine: event worker - started")
	s.wgVerbose("Received invalid initiation message from %s", "172.20.10.4:5000")
	s.wgError("%v - Failed to send data packets: %v", tok, "boom")
	tail := log.Tail()
	if !strings.Contains(tail, "info wg Mac ("+shortKey(pub)+") - Received handshake initiation") ||
		strings.Contains(tail, "Sending handshake response") || strings.Contains(tail, "Routine") ||
		!strings.Contains(tail, "warn wg Mac ("+shortKey(pub)+") - Failed to send data packets: boom") {
		t.Fatalf("log:\n%s", tail)
	}
	s.fails.flush(log, time.Now(), true)
	if !strings.Contains(log.Tail(), "WireGuard: 1 handshake was rejected in the last") || !strings.Contains(log.Tail(), "last from 172.20.10.4:5000") {
		t.Fatal(log.Tail())
	}
}
