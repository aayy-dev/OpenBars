package engine

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestPairing(t *testing.T) {
	log := NewLog(200)
	log.SetEcho(func(s string) { t.Log(s) })
	serverPriv, _ := GenerateKey()
	serverPub, _ := PublicKey(serverPriv)
	srv, err := Start(Config{PrivateKey: serverPriv, ListenPort: 51990, WebPort: 18090, ConnectPort: 18090, HotspotSSID: "Test Phone"}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	const base = "http://127.0.0.1:18090/api/pair"

	post := func(name, pub string) (int, PairResponse) {
		body, _ := json.Marshal(map[string]string{"name": name, "public_key": pub})
		resp, err := http.Post(base, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Error(err)
			return 0, PairResponse{}
		}
		defer resp.Body.Close()
		var pr PairResponse
		json.NewDecoder(resp.Body).Decode(&pr)
		return resp.StatusCode, pr
	}
	waitPending := func(pub string) {
		for i := 0; i < 100; i++ {
			for _, p := range srv.Status().Pairing {
				if p.PublicKey == pub {
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("request never showed up in Status().Pairing")
	}

	// Approve: the app adds the device with that key.
	priv, _ := GenerateKey()
	pub, _ := PublicKey(priv)
	type result struct {
		code int
		pr   PairResponse
	}
	done := make(chan result, 1)
	go func() { c, pr := post("MacBook", pub); done <- result{c, pr} }()
	waitPending(pub)
	if err := srv.SetPeers([]PeerConfig{{Name: "MacBook", PublicKey: pub, Address: "10.66.66.7"}}); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.code != 200 {
		t.Fatalf("approve: status %d", r.code)
	}
	// Requests here come over loopback, so the endpoint is the phone's
	// hotspot address: the default, or a bridge address if this test host
	// has one (macOS Internet Sharing, a VM bridge).
	host := defaultHotspot
	if ni := DetectNetInfo(); ni.HotspotIP != "" {
		host = ni.HotspotIP
	}
	want := PairResponse{
		ServerPublicKey: serverPub, Endpoint: host + ":51990", Address: "10.66.66.7",
		Address6: "fd0b:a75::a42:4207", DNS: "10.66.66.1", MTU: 1420,
		HotspotName: "Test Phone", DeviceName: "MacBook",
	}
	if r.pr != want {
		t.Fatalf("got %+v\nwant %+v", r.pr, want)
	}
	if n := len(srv.Status().Pairing); n != 0 {
		t.Fatalf("%d requests still pending", n)
	}

	// Already paired: answered at once, by POST and GET.
	if c, pr := post("MacBook", pub); c != 200 || pr.Address != "10.66.66.7" {
		t.Fatalf("repeat pair: %d %+v", c, pr)
	}
	resp, err := http.Get(base + "?public_key=" + url.QueryEscape(pub))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET paired: %v %v", resp.StatusCode, err)
	}
	resp.Body.Close()

	// Deny.
	priv2, _ := GenerateKey()
	pub2, _ := PublicKey(priv2)
	go func() { c, pr := post("Stranger", pub2); done <- result{c, pr} }()
	waitPending(pub2)
	if !srv.DenyPairing(pub2) {
		t.Fatal("deny found nothing")
	}
	if r := <-done; r.code != http.StatusForbidden {
		t.Fatalf("deny: status %d", r.code)
	}
	resp, _ = http.Get(base + "?public_key=" + url.QueryEscape(pub2))
	if resp.StatusCode != 404 {
		t.Fatalf("GET unpaired: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Timeout.
	old := pairWait
	pairWait = 200 * time.Millisecond
	defer func() { pairWait = old }()
	priv3, _ := GenerateKey()
	pub3, _ := PublicKey(priv3)
	if c, _ := post("Slow", pub3); c != http.StatusRequestTimeout {
		t.Fatalf("timeout: status %d", c)
	}

	// Garbage key.
	if c, _ := post("Bad", "nope"); c != http.StatusBadRequest {
		t.Fatalf("bad key: status %d", c)
	}
}
