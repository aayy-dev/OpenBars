// Package probe checks that DNS and the internet work, for Diagnose. While
// the tunnel is up both go through it (it holds the default route).
package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Prober implements core.Prober.
type Prober struct {
	Name        string // looked up through the DNS server
	InternetURL string
	Expect      string // text the internet check's response must contain
	HTTP        *http.Client
}

// New uses Microsoft's connectivity check, which Windows itself uses.
func New() *Prober {
	return &Prober{
		Name:        "www.msftconnecttest.com",
		InternetURL: "http://www.msftconnecttest.com/connecttest.txt",
		Expect:      "Microsoft Connect Test",
		HTTP:        &http.Client{Timeout: 8 * time.Second},
	}
}

// DNS asks server for an A record and checks it answers.
func (p *Prober) DNS(ctx context.Context, server string) error {
	return exchange(ctx, net.JoinHostPort(server, "53"), p.Name)
}

// exchange sends one A query for name to addr (host:port) and checks the answer.
func exchange(ctx context.Context, addr, name string) error {
	server, _, _ := net.SplitHostPort(addr)
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return fmt.Errorf("couldn't reach %s: %w", server, err)
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	q, id, err := query(name)
	if err != nil {
		return err
	}
	if _, err := c.Write(q); err != nil {
		return fmt.Errorf("couldn't send to %s: %w", server, err)
	}
	buf := make([]byte, 1500)
	for {
		n, err := c.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return fmt.Errorf("%s didn't answer within 4 s", server)
			}
			return fmt.Errorf("no answer from %s: %w", server, err)
		}
		if n < 12 || binary.BigEndian.Uint16(buf) != id {
			continue // not our answer
		}
		rcode := buf[3] & 0x0f
		answers := binary.BigEndian.Uint16(buf[6:])
		switch {
		case rcode != 0:
			return fmt.Errorf("%s answered with error code %d for %s", server, rcode, name)
		case answers == 0:
			return fmt.Errorf("%s had no address for %s", server, name)
		}
		return nil
	}
}

// query builds a recursive A query for name.
func query(name string) ([]byte, uint16, error) {
	var idb [2]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return nil, 0, err
	}
	id := binary.BigEndian.Uint16(idb[:])
	q := []byte{idb[0], idb[1], 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, 0, fmt.Errorf("bad name %q", name)
		}
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	q = append(q, 0, 0, 1, 0, 1) // root, type A, class IN
	return q, id, nil
}

// Internet fetches a small known page.
func (p *Prober) Internet(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.InternetURL, nil)
	if err != nil {
		return err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't load %s: %w", p.InternetURL, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), p.Expect) {
		return fmt.Errorf("unexpected answer from %s (HTTP %d); a captive portal or filter may be in the way", p.InternetURL, resp.StatusCode)
	}
	return nil
}
