// Package phone talks to Open Bars on the iPhone over its hotspot: the
// settings it publishes, pairing, and checking that this PC is still paired.
package phone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Host and Port are where Open Bars on the iPhone answers on its hotspot.
const (
	Host = "172.20.10.1"
	Port = 8080
)

// Info is what the iPhone publishes at /client.json.
type Info struct {
	HotspotName   string `json:"hotspot_name"`
	Pairing       bool   `json:"pairing"`
	WireGuardPort int    `json:"wireguard_port"`
}

// Pairing is what the iPhone sends back once this PC is approved.
type Pairing struct {
	ServerPublicKey string `json:"server_public_key"`
	Endpoint        string `json:"endpoint"` // 172.20.10.1:51820
	Address         string `json:"address"`  // this PC's tunnel IPv4
	Address6        string `json:"address6"` // this PC's tunnel IPv6 (may be empty)
	DNS             string `json:"dns"`
	MTU             int    `json:"mtu"`
	HotspotName     string `json:"hotspot_name"`
	DeviceName      string `json:"device_name"`
}

// SameTunnel reports whether two pairings give the same tunnel (names aside).
func (p Pairing) SameTunnel(o Pairing) bool {
	return p.ServerPublicKey == o.ServerPublicKey && p.Endpoint == o.Endpoint &&
		p.Address == o.Address && p.Address6 == o.Address6 && p.DNS == o.DNS && p.MTU == o.MTU
}

// Errors the caller turns into messages and hints.
var (
	ErrDenied      = errors.New("the iPhone didn't allow this PC")
	ErrTimedOut    = errors.New("nobody tapped Allow on the iPhone in time")
	ErrNotPaired   = errors.New("the iPhone doesn't have this PC")
	ErrOldIPhone   = errors.New("Open Bars on the iPhone is too old to pair")
	ErrUnreachable = errors.New("the iPhone isn't answering")
)

// Client talks to one iPhone.
type Client struct {
	Base string // "http://172.20.10.1:8080"
	HTTP *http.Client
}

// New returns a client for the iPhone at the usual hotspot address.
func New() *Client {
	return &Client{Base: fmt.Sprintf("http://%s:%d", Host, Port), HTTP: &http.Client{}}
}

// Info fetches the iPhone's published settings.
func (c *Client) Info(ctx context.Context) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var info Info
	code, body, err := c.do(ctx, http.MethodGet, "/client.json", nil)
	if err != nil {
		return info, err
	}
	if code != http.StatusOK {
		return info, fmt.Errorf("the iPhone answered HTTP %d for /client.json", code)
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return info, fmt.Errorf("unexpected answer from the iPhone: %w", err)
	}
	return info, nil
}

// Pair asks to become a device and waits (up to about 90 s on the iPhone's
// side) for someone to tap Allow.
func (c *Client) Pair(ctx context.Context, name, kind, publicKey string) (Pairing, error) {
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	req, _ := json.Marshal(map[string]string{"name": name, "kind": kind, "public_key": publicKey})
	code, body, err := c.do(ctx, http.MethodPost, "/api/pair", req)
	if err != nil {
		return Pairing{}, err
	}
	switch code {
	case http.StatusOK:
		return decodePairing(body)
	case http.StatusForbidden:
		return Pairing{}, ErrDenied
	case http.StatusRequestTimeout:
		return Pairing{}, ErrTimedOut
	case http.StatusNotFound:
		return Pairing{}, ErrOldIPhone
	}
	return Pairing{}, fmt.Errorf("pairing failed: the iPhone answered HTTP %d%s", code, errorText(body))
}

// Check asks whether this iPhone has the device with this public key, and
// returns its current settings. ErrNotPaired means it doesn't (a different
// iPhone, or the PC was removed there).
func (c *Client) Check(ctx context.Context, publicKey string) (Pairing, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	code, body, err := c.do(ctx, http.MethodGet, "/api/pair?public_key="+url.QueryEscape(publicKey), nil)
	if err != nil {
		return Pairing{}, err
	}
	switch code {
	case http.StatusOK:
		return decodePairing(body)
	case http.StatusNotFound:
		return Pairing{}, ErrNotPaired
	}
	return Pairing{}, fmt.Errorf("the iPhone answered HTTP %d%s", code, errorText(body))
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, unreachable(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return 0, nil, unreachable(err)
	}
	return resp.StatusCode, b, nil
}

func decodePairing(b []byte) (Pairing, error) {
	var p Pairing
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("unexpected answer from the iPhone: %w", err)
	}
	if p.ServerPublicKey == "" || p.Endpoint == "" || p.Address == "" {
		return p, errors.New("the iPhone's answer is missing tunnel settings")
	}
	if p.MTU <= 0 {
		p.MTU = 1280
	}
	return p, nil
}

func errorText(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return " (" + e.Error + ")"
	}
	return ""
}

// UnreachableError wraps a network failure with a plain-language reason.
type UnreachableError struct {
	Reason string // "connection refused", "timed out", ...
	Err    error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("%s: %s", ErrUnreachable, e.Reason)
}
func (e *UnreachableError) Unwrap() []error { return []error{ErrUnreachable, e.Err} }

func unreachable(err error) error {
	reason := err.Error()
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		reason = "timed out"
	case errors.Is(err, syscall.ECONNREFUSED), strings.Contains(reason, "refused"):
		reason = "connection refused"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH),
		strings.Contains(reason, "unreachable"):
		reason = "no route to the iPhone"
	case errors.Is(err, context.Canceled):
		reason = "cancelled"
	}
	return &UnreachableError{Reason: reason, Err: err}
}
