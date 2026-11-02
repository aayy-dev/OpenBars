package wgclient

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLogAPI(t *testing.T) {
	ClearLogs()
	var p struct {
		Next    int64 `json:"next"`
		First   int64 `json:"first"`
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
	start := time.Now().UnixMilli()
	Log("warn", "vpn", "on-demand off", "turn it on")
	Log("debug", "net", "hidden", "")
	Log("whatever", "", "defaults", "")
	if err := json.Unmarshal([]byte(LogsJSON(0)), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 2 {
		t.Fatalf("entries %+v", p.Entries)
	}
	e := p.Entries[0]
	if e.Lvl != "warn" || e.Src != "vpn" || e.Msg != "on-demand off" || e.Hint != "turn it on" || e.T < start {
		t.Fatalf("entry %+v", e)
	}
	if p.Entries[1].Lvl != "info" || p.Entries[1].Src != "app" {
		t.Fatalf("defaults %+v", p.Entries[1])
	}
	next := p.Next
	SetDetailedLogging(true)
	defer SetDetailedLogging(false)
	Log("debug", "net", "shown", "")
	if err := json.Unmarshal([]byte(LogsJSON(next)), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 2 || p.Entries[1].Msg != "shown" || p.Entries[0].Seq != next+1 {
		t.Fatalf("after %d: %+v", next, p.Entries)
	}
	next = p.Next
	Log("warn", "wg", "No reply from the iPhone", "")
	Log("warn", "wg", "No reply from the iPhone", "")
	Log("info", "net", "path changed", "")
	p.Entries = nil
	if err := json.Unmarshal([]byte(LogsJSON(next)), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 3 || p.Entries[0].Rep != 0 || p.Entries[1].Rep != 1 || p.Entries[1].Msg != "No reply from the iPhone" || p.Entries[2].Msg != "path changed" {
		t.Fatalf("repeats: %+v", p.Entries)
	}
	if stripPeer("peer(AbCd…WxYz) - Received handshake response") != "Received handshake response" {
		t.Fatal("stripPeer")
	}
}

func TestStartRejectsBadSettings(t *testing.T) {
	for _, s := range []string{
		`nope`,
		`{"private_key":"x","server_public_key":"x","endpoint":"172.20.10.1:51820"}`,
		`{"private_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","server_public_key":"x","endpoint":"172.20.10.1:51820"}`,
		`{"private_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","server_public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","endpoint":"iphone:51820"}`,
	} {
		if err := Start(s); err == nil {
			t.Fatalf("Start(%s) should fail", s)
		}
	}
	var st Status
	if err := json.Unmarshal([]byte(StatusJSON()), &st); err != nil || st.Running {
		t.Fatalf("status %+v %v", st, err)
	}
}
