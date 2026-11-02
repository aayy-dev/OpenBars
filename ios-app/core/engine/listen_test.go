package engine

import (
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// acceptOne dials addr and checks the listener hands over the connection.
func acceptOne(t *testing.T, ln net.Listener, addr string) {
	t.Helper()
	got := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- err
			return
		}
		defer c.Close()
		_, err = c.Write([]byte("hi"))
		got <- err
	}()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 2)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "hi" {
		t.Fatalf("read via %s: %q %v", addr, b, err)
	}
	if err := <-got; err != nil {
		t.Fatalf("accept: %v", err)
	}
}

// The in-app listener is the plain wildcard one.
func TestListenTCPInApp(t *testing.T) {
	s := &Server{cfg: Config{}, log: NewLog(50)}
	port := freePort(t)
	ln, err := listenTCP(s, port)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, ok := ln.(*scopedListener); ok {
		t.Fatal("in-app mode should not use the scoped listener")
	}
	acceptOne(t, ln, fmt.Sprintf("127.0.0.1:%d", port))
}

// In the extension, loopback still works and each matching interface gets
// its own bound listener; Close stops everything.
func TestScopedListener(t *testing.T) {
	// Use a real interface with an IPv4 address in place of bridge100.
	var name string
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if _, ok := firstUsable(ifaceAddrs(ifc), true); ok {
			name = ifc.Name
			break
		}
	}
	log := NewLog(50)
	port := freePort(t)
	prefix := name
	if prefix == "" {
		prefix = "nonexistent-iface"
	}
	ln, err := newScopedListener(port, log, prefix)
	if err != nil {
		t.Fatal(err)
	}
	acceptOne(t, ln, fmt.Sprintf("127.0.0.1:%d", port))

	if name != "" {
		if !strings.Contains(log.Tail(), "listening on "+name) {
			t.Fatalf("expected a bound listener on %s, log:\n%s", name, log.Tail())
		}
		ln.mu.Lock()
		n := len(ln.lns)
		ln.mu.Unlock()
		if n != 1 {
			t.Fatalf("want 1 interface listener, have %d", n)
		}
	}

	ln.Close()
	if _, err := ln.Accept(); err != net.ErrClosed {
		t.Fatalf("Accept after Close: %v", err)
	}
	// The port must be free again.
	again, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("port still held after Close: %v", err)
	}
	again.Close()
}
