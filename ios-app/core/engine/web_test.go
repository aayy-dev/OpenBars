package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"Gaming PC":     "Gaming-PC",
		"laptop":        "laptop",
		"Aay's iPad 2":  "Aays-iPad-2",
		"émoji 🎮_x-y":   "moji-_x-y",
		"":              "device",
		"🎮":             "device",
		"../../etc/pwd": "etcpwd",
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClientConfig(t *testing.T) {
	got := ClientConfig("PRIV=", "10.66.66.2", "PUB=", "172.20.10.1", 51820, "10.66.66.1", 1420)
	want := `[Interface]
PrivateKey = PRIV=
Address = 10.66.66.2/32, fd0b:a75::a42:4202/128
DNS = 10.66.66.1
MTU = 1420

[Peer]
PublicKey = PUB=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 172.20.10.1:51820
PersistentKeepalive = 25
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if ClientConfig("PRIV=", "10.66.66.2", "PUB=", "", 0, "", 0) != want {
		t.Fatal("defaults")
	}
	v6 := ClientConfig("k", "10.66.66.9", "p", "fd00::1", 51000, "10.66.66.1", 1280)
	for _, sub := range []string{"Endpoint = [fd00::1]:51000\n", "MTU = 1280\n", "Address = 10.66.66.9/32, fd0b:a75::a42:4209/128\n"} {
		if !strings.Contains(v6, sub) {
			t.Fatalf("missing %q in\n%s", sub, v6)
		}
	}
	for _, bad := range []string{"bogus", "", "fd00::5", "10.66.66"} {
		if c := ClientConfig("k", bad, "p", "h", 1, "g", 1); c != "" {
			t.Fatalf("%q: a device address that isn't IPv4 must give no config, got\n%s", bad, c)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && (s[:len(sub)] == sub || contains(s[1:], sub))
}

func TestEndpointHost(t *testing.T) {
	s := &Server{gw: netip.MustParseAddr("10.66.66.1")}
	for host, want := range map[string]string{
		"172.20.10.1:8080": "172.20.10.1",
		"172.20.10.5":      "172.20.10.5",
		"10.66.66.1:8080":  "172.20.10.1", // the gateway: use the hotspot address
		"127.0.0.1:8080":   "172.20.10.1",
		"iphone.local":     "172.20.10.1",
		"[fd00::1]:8080":   "fd00::1",
		"[::1]:8080":       "172.20.10.1",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		if got := s.endpointHost(r); got != want {
			t.Errorf("%s: %q, want %q", host, got, want)
		}
	}
	s.netInfo.Store(&NetInfo{HotspotIP: "172.20.10.9"})
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "127.0.0.1:8080"
	if got := s.endpointHost(r); got != "172.20.10.9" {
		t.Fatal(got)
	}
	if !s.isPhoneAddr(netip.MustParseAddr("172.20.10.9")) || !s.isPhoneAddr(netip.MustParseAddr("172.20.10.1")) || s.isPhoneAddr(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("isPhoneAddr")
	}
}

// Turning the config page off (or moving it) must not take pairing away:
// Open Bars Connect always looks on ConnectPort.
func TestPairingWithoutConfigPage(t *testing.T) {
	priv, _ := GenerateKey()
	for _, webPort := range []int{0, 18093} {
		srv, err := Start(Config{PrivateKey: priv, ListenPort: 51994, WebPort: webPort, ConnectPort: 18092}, NewLog(50))
		if err != nil {
			t.Fatal(err)
		}
		get := func(port int, path string) int {
			resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
			if err != nil {
				return 0
			}
			resp.Body.Close()
			return resp.StatusCode
		}
		if c := get(18092, "/client.json"); c != 200 {
			t.Errorf("web %d: /client.json on the Connect port: %d", webPort, c)
		}
		if c := get(18092, "/"); c != 404 {
			t.Errorf("web %d: config page served on the Connect port: %d", webPort, c)
		}
		if webPort > 0 {
			if c := get(webPort, "/"); c != 200 {
				t.Errorf("config page on its own port: %d", c)
			}
			if c := get(webPort, "/api/pair"); c != 404 {
				t.Errorf("pairing on the page port: %d", c)
			}
		}
		srv.Stop()
	}
}
