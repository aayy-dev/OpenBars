package probe

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeDNS answers every query with the given rcode and answer count; it
// sends a stray reply with the wrong ID first, which must be ignored.
func fakeDNS(t *testing.T, rcode byte, answers uint16) (port string) {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			resp := append([]byte(nil), buf[:n]...)
			stray := append([]byte(nil), resp...)
			stray[0] ^= 0xff
			c.WriteTo(stray, from)
			resp[2], resp[3] = 0x81, 0x80|rcode
			binary.BigEndian.PutUint16(resp[6:], answers)
			c.WriteTo(resp, from)
		}
	}()
	_, p, _ := net.SplitHostPort(c.LocalAddr().String())
	return p
}

func TestQuery(t *testing.T) {
	q, id, err := query("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(q) != id || !strings.Contains(string(q), "\x07example\x03com\x00") {
		t.Fatalf("query %x", q)
	}
}

func TestDNSAgainstFake(t *testing.T) {
	// DNS() always uses port 53; exercise the exchange through a helper port.
	for _, tc := range []struct {
		rcode   byte
		answers uint16
		ok      bool
	}{{0, 1, true}, {3, 0, false}, {0, 0, false}} {
		port := fakeDNS(t, tc.rcode, tc.answers)
		err := exchange(context.Background(), "127.0.0.1:"+port, "example.com")
		if (err == nil) != tc.ok {
			t.Errorf("rcode %d answers %d: err %v", tc.rcode, tc.answers, err)
		}
	}
}

func TestInternet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Microsoft Connect Test"))
	}))
	defer srv.Close()
	p := New()
	p.InternetURL = srv.URL
	if err := p.Internet(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.Expect = "something else"
	if err := p.Internet(context.Background()); err == nil {
		t.Fatal("accepted a wrong page")
	}
}
