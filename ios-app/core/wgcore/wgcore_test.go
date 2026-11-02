package wgcore

import (
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Ports used here: 51971-51973, 18071.

func keys(t *testing.T, s string) []string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%v: %s", err, s)
	}
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type logPage struct {
	Next    int64 `json:"next"`
	First   int64 `json:"first"`
	Boot    int64 `json:"boot"`
	Entries []struct {
		Seq  int64  `json:"seq"`
		T    int64  `json:"t"`
		Lvl  string `json:"lvl"`
		Src  string `json:"src"`
		Msg  string `json:"msg"`
		Hint string `json:"hint"`
		Rep  int    `json:"rep"`
	} `json:"entries"`
}

func readLogs(t *testing.T, after int64) logPage {
	t.Helper()
	var p logPage
	if err := json.Unmarshal([]byte(LogsJSON(after)), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStopped(t *testing.T) {
	Stop()
	if Running() {
		t.Fatal("running")
	}
	if err := SetPeers(`[]`); err != nil {
		t.Fatalf("SetPeers while stopped: %v", err)
	}
	if err := SetPeers(`{`); err == nil {
		t.Fatal("bad JSON must fail even when stopped")
	}
	if UsageJSON(31) != "" || HistoryJSON(0) != "[]" || DenyPairing("x") {
		t.Fatal("stopped stubs")
	}
	st := StatusJSON()
	want := []string{"bytes_down", "bytes_up", "egress_if", "listen_port", "net", "pairing", "peers", "running", "tcp_active", "tcp_total", "udp_flows", "uptime", "web_error"}
	if got := keys(t, st); !reflect.DeepEqual(got, want) {
		t.Fatalf("stopped status keys %v", got)
	}
	if !strings.Contains(st, `"running":false`) || !strings.Contains(st, `"peers":null`) || !strings.Contains(st, `"pairing":null`) {
		t.Fatal(st)
	}
	netKeys := []string{"cellular_if", "cellular_ip", "default_if", "hotspot_ip", "vpn_active", "vpn_if", "wifi_ip"}
	if got := keys(t, NetInfoJSON()); !reflect.DeepEqual(got, netKeys) {
		t.Fatalf("net keys %v", got)
	}
	if got := ReadUsageJSON(t.TempDir()+"/missing.json", "UTC", 31); got != `{"hours":[],"days":[],"names":{},"seen":{}}` {
		t.Fatal(got)
	}
}

func TestKeysAndConfig(t *testing.T) {
	priv := GeneratePrivateKey()
	pub := PublicKey(priv)
	if len(priv) != 44 || len(pub) != 44 || PublicKey("abc") != "" {
		t.Fatal("keys")
	}
	conf := ClientConfig(priv, "10.66.66.2", pub, "172.20.10.1", 51820, "10.66.66.1", 1420)
	if !strings.Contains(conf, "PublicKey = "+pub) || !strings.Contains(conf, "AllowedIPs = 0.0.0.0/0") {
		t.Fatal(conf)
	}
}

func TestLifecycle(t *testing.T) {
	defer Stop()
	priv := GeneratePrivateKey()
	devPriv := GeneratePrivateKey()
	devPub := PublicKey(devPriv)
	cfg := fmt.Sprintf(`{"private_key":%q,"listen_port":51971,"web_port":18071,"bind_cellular":true,
		"peers":[{"name":"Gaming PC","public_key":%q,"private_key":%q,"address":"10.66.66.2"}],"hotspot_ssid":"Test"}`, priv, devPub, devPriv)
	ClearLogs()
	mark := readLogs(t, 0).Next
	if err := Start(cfg); err != nil {
		t.Fatal(err)
	}
	if !Running() {
		t.Fatal("not running")
	}
	st := StatusJSON()
	var status struct {
		Running bool `json:"running"`
		Port    int  `json:"listen_port"`
		Peers   []map[string]any
		Pairing []any `json:"pairing"`
	}
	json.Unmarshal([]byte(st), &status)
	if !status.Running || status.Port != 51971 || len(status.Peers) != 1 || status.Pairing == nil {
		t.Fatal(st)
	}
	peerKeys := []string{"address", "endpoint", "last_handshake", "last_seen", "name", "public_key", "rx_bytes", "tx_bytes"}
	var full struct {
		Peers []json.RawMessage `json:"peers"`
	}
	json.Unmarshal([]byte(st), &full)
	if got := keys(t, string(full.Peers[0])); !reflect.DeepEqual(got, peerKeys) {
		t.Fatalf("peer keys %v", got)
	}

	// Bad JSON doesn't stop a running server.
	if err := Start(`{"listen_port":`); err == nil || !strings.HasPrefix(err.Error(), "bad config: ") {
		t.Fatalf("bad config: %v", err)
	}
	if !Running() {
		t.Fatal("bad JSON stopped the engine")
	}
	if err := SetPeers(`[]`); err != nil {
		t.Fatal(err)
	}
	if UsageJSON(31) == "" || HistoryJSON(0) == "" {
		t.Fatal("running stubs")
	}

	// Restart on the same ports.
	if err := Start(cfg); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// An engine start failure leaves it stopped and is logged with a hint.
	busy, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 51972})
	if err != nil {
		t.Skip(err)
	}
	defer busy.Close()
	err = Start(fmt.Sprintf(`{"private_key":%q,"listen_port":51972}`, priv))
	if err == nil || Running() {
		t.Fatalf("start on a busy port: %v, running %v", err, Running())
	}
	p := readLogs(t, mark)
	var sawStart, sawFail, sawStop bool
	for _, e := range p.Entries {
		if e.T == 0 || e.Seq <= mark {
			t.Fatalf("entry %+v", e)
		}
		switch {
		case e.Src == "share" && strings.HasPrefix(e.Msg, "Sharing started"):
			sawStart = true
		case e.Lvl == "error" && e.Src == "share" && strings.Contains(e.Msg, "listen on UDP 51972") && strings.Contains(e.Hint, "UDP port 51972"):
			sawFail = true
		case strings.HasPrefix(e.Msg, "Sharing stopped"):
			sawStop = true
		}
		if strings.Contains(e.Msg, priv) || strings.Contains(e.Msg, devPriv) {
			t.Fatal("private key logged")
		}
	}
	if !sawStart || !sawFail || !sawStop {
		t.Fatalf("start %v fail %v stop %v in %+v", sawStart, sawFail, sawStop, p.Entries)
	}
}

func TestLogAPI(t *testing.T) {
	ClearLogs()
	p := readLogs(t, 0)
	if len(p.Entries) != 0 || p.Next != 0 || p.First < 1 || p.Boot == 0 {
		t.Fatalf("after clear %+v", p)
	}
	before := p.First - 1 // the last seq handed out before the clear
	Log("WARN", "app", "line one", "do this")
	Log("debug", "app", "hidden", "")
	SetDetailedLogging(true)
	Log("debug", "ext", "shown", "")
	SetDetailedLogging(false)
	Log("nonsense", "", "info by default", "")
	p = readLogs(t, before)
	var got []string
	for _, e := range p.Entries {
		got = append(got, e.Lvl+" "+e.Src+" "+e.Msg+" "+e.Hint)
	}
	want := []string{
		"warn app line one do this",
		"info share Detailed logging on ",
		"debug ext shown ",
		"info share Detailed logging off ",
		"info app info by default ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if p.Next != p.Entries[len(p.Entries)-1].Seq {
		t.Fatal("next")
	}
	if q := readLogs(t, p.Next); len(q.Entries) != 0 || q.Next != p.Next {
		t.Fatalf("nothing new: %+v", q)
	}

	// Repeats: counted, then one summary entry when something else comes.
	for i := 0; i < 4; i++ {
		Log("error", "web", "Couldn't listen on 172.20.10.1:8080: address in use", "Is Personal Hotspot on?")
	}
	Log("info", "app", "something else", "")
	q := readLogs(t, p.Next)
	if len(q.Entries) != 3 || q.Entries[0].Rep != 0 || q.Entries[1].Rep != 3 || q.Entries[1].Hint != "Is Personal Hotspot on?" ||
		q.Entries[1].Msg != q.Entries[0].Msg || q.Entries[2].Msg != "something else" {
		t.Fatalf("repeats: %+v", q.Entries)
	}
}
