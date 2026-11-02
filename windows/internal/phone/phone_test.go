package phone

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeIPhone behaves like Open Bars' pair.go: POST waits for a decision,
// GET ?public_key= answers 200 for known devices and 404 otherwise.
type fakeIPhone struct {
	known    map[string]bool
	decision int // status for the next POST from an unknown key
}

func (f *fakeIPhone) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/client.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"hotspot_name": "Aayush’s iPhone", "pairing": true, "wireguard_port": 51820})
	})
	mux.HandleFunc("/api/pair", func(w http.ResponseWriter, r *http.Request) {
		cfg := map[string]any{"server_public_key": "c2VydmVy", "endpoint": "172.20.10.1:51820",
			"address": "10.66.66.5", "address6": "fd0b:a75::a42:4205", "dns": "10.66.66.1",
			"mtu": 1280, "hotspot_name": "Aayush’s iPhone", "device_name": "AAYUSH-PC"}
		switch r.Method {
		case http.MethodGet:
			if !f.known[r.URL.Query().Get("public_key")] {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"error": "not paired"})
				return
			}
			json.NewEncoder(w).Encode(cfg)
		case http.MethodPost:
			var req map[string]string
			json.NewDecoder(r.Body).Decode(&req)
			if req["kind"] != "windows" || req["name"] == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if f.decision != http.StatusOK {
				w.WriteHeader(f.decision)
				json.NewEncoder(w).Encode(map[string]string{"error": "x"})
				return
			}
			f.known[req["public_key"]] = true
			json.NewEncoder(w).Encode(cfg)
		}
	})
	return mux
}

func TestPairAndCheck(t *testing.T) {
	f := &fakeIPhone{known: map[string]bool{}, decision: http.StatusOK}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c := &Client{Base: srv.URL, HTTP: srv.Client()}
	ctx := context.Background()

	info, err := c.Info(ctx)
	if err != nil || !info.Pairing || info.WireGuardPort != 51820 {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if _, err := c.Check(ctx, "pub"); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("check before pairing: %v", err)
	}
	p, err := c.Pair(ctx, "AAYUSH-PC", "windows", "pub")
	if err != nil || p.Address != "10.66.66.5" || p.MTU != 1280 {
		t.Fatalf("pair = %+v, %v", p, err)
	}
	q, err := c.Check(ctx, "pub")
	if err != nil || !q.SameTunnel(p) {
		t.Fatalf("check after pairing = %+v, %v", q, err)
	}
}

func TestPairOutcomes(t *testing.T) {
	for code, want := range map[int]error{
		http.StatusForbidden:      ErrDenied,
		http.StatusRequestTimeout: ErrTimedOut,
	} {
		f := &fakeIPhone{known: map[string]bool{}, decision: code}
		srv := httptest.NewServer(f.handler())
		c := &Client{Base: srv.URL, HTTP: srv.Client()}
		if _, err := c.Pair(context.Background(), "PC", "windows", "k"); !errors.Is(err, want) {
			t.Errorf("HTTP %d: got %v, want %v", code, err, want)
		}
		srv.Close()
	}
}

func TestUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now
	c := &Client{Base: "http://" + addr, HTTP: &http.Client{Timeout: 2 * time.Second}}
	_, err = c.Info(context.Background())
	var ue *UnreachableError
	if !errors.As(err, &ue) || !errors.Is(err, ErrUnreachable) || ue.Reason != "connection refused" {
		t.Fatalf("got %v", err)
	}
}
