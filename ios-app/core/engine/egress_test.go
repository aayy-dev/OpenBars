package engine

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNAT64(t *testing.T) {
	p := netip.MustParsePrefix("64:ff9b::/96")
	v6 := synthNAT64(p, netip.MustParseAddr("1.2.3.4"))
	if v6.String() != "64:ff9b::102:304" {
		t.Fatal(v6)
	}
	if v4, ok := unsynthNAT64(p, v6); !ok || v4.String() != "1.2.3.4" {
		t.Fatal(v4, ok)
	}
	for _, s := range []string{"2001:db8::102:304", "::ffff:1.2.3.4", "1.2.3.4"} {
		if _, ok := unsynthNAT64(p, netip.MustParseAddr(s)); ok {
			t.Errorf("%s should not unsynth", s)
		}
	}
	if _, ok := unsynthNAT64(netip.Prefix{}, v6); ok {
		t.Fatal("no prefix")
	}
	if pre, ok := nat64PrefixOf(netip.MustParseAddr("2600:1700:abcd::c000:aa")); !ok || pre.String() != "2600:1700:abcd::/96" {
		t.Fatal(pre, ok)
	}
	if _, ok := nat64PrefixOf(netip.MustParseAddr("64:ff9b::808:808")); ok {
		t.Fatal("not a well-known answer")
	}
}

// synth never waits for discovery; a later call benefits.
func TestNAT64DiscoveryNeverBlocks(t *testing.T) {
	log := NewLog(50)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	calls := make(chan struct{}, 10)
	e := &egress{ctx: ctx, log: log}
	e.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		calls <- struct{}{}
		if host != "ipv4only.arpa" {
			t.Errorf("looked up %q", host)
		}
		<-release
		return []netip.Addr{netip.MustParseAddr("64:ff9b::c000:aa")}, nil
	}
	start := time.Now()
	if _, ok := e.synth(netip.MustParseAddr("1.2.3.4")); ok {
		t.Fatal("no prefix yet")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("synth blocked %v", d)
	}
	<-calls
	e.synth(netip.MustParseAddr("1.2.3.4")) // lookup in flight: no second one
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if v6, ok := e.synth(netip.MustParseAddr("1.2.3.4")); ok {
			if v6.String() != "64:ff9b::102:304" {
				t.Fatal(v6)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("prefix never learned")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(calls) != 0 {
		t.Fatal("a second lookup started while one was running")
	}
	if !strings.Contains(log.Tail(), "NAT64 prefix 64:ff9b::/96 found") {
		t.Fatal(log.Tail())
	}
}

func TestNAT64Outcomes(t *testing.T) {
	log := NewLog(50)
	e := &egress{ctx: context.Background(), log: log}
	results := []error{&net.DNSError{IsNotFound: true}, &net.DNSError{IsNotFound: true}, errors.New("server misbehaving")}
	for _, err := range results {
		err := err
		e.lookup = func(context.Context, string) ([]netip.Addr, error) { return nil, err }
		e.lookupNAT64()
	}
	tail := log.Tail()
	if strings.Count(tail, "No NAT64") != 1 || !strings.Contains(tail, "warn nat64 NAT64 discovery failed: server misbehaving") {
		t.Fatalf("log:\n%s", tail)
	}
}

func TestEgressDialAndListen(t *testing.T) {
	log := NewLog(50)
	e := newEgress(context.Background(), false, log)
	if e.ifName() != "default route" || e.index() != 0 {
		t.Fatal(e.ifName())
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := e.DialTCP(context.Background(), ln.Addr().(*net.TCPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	for _, v6 := range []bool{false, true} {
		u, err := e.ListenUDP(v6)
		if err != nil {
			t.Fatal(err)
		}
		u.Close()
	}
	if !isUnreachable(&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}) ||
		isUnreachable(syscall.ECONNREFUSED) || isUnreachable(nil) {
		t.Fatal("isUnreachable")
	}
}
