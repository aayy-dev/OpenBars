package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// The end-to-end tests run a real WireGuard client (wireguard-go on a
// userspace netstack) in this process against the engine over loopback.
// The "internet" is a non-loopback IPv4 address of this machine: the engine
// never forwards to loopback.

// hostIPv4 finds a usable non-loopback IPv4 address of this machine.
func hostIPv4(t *testing.T) netip.Addr {
	t.Helper()
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		for _, a := range ifaceAddrs(ifc) {
			if a.Is4() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !dropDestination(a) {
				// It must be reachable from this machine.
				ln, err := net.Listen("tcp4", netip.AddrPortFrom(a, 0).String())
				if err == nil {
					ln.Close()
					return a
				}
			}
		}
	}
	t.Skip("no usable non-loopback IPv4 address")
	return netip.Addr{}
}

// fakeDNS answers every A query with answer (TTL 60) and other queries
// with no records, over UDP and TCP on the same port.
type fakeDNS struct {
	addr    netip.AddrPort
	queries sync.Map // name -> count
	udp     net.PacketConn
	tcp     net.Listener
}

func startFakeDNS(t *testing.T, ip netip.Addr, answer [4]byte) *fakeDNS {
	t.Helper()
	f := &fakeDNS{}
	for i := 0; i < 20; i++ {
		ln, err := net.Listen("tcp4", netip.AddrPortFrom(ip, 0).String())
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		pc, err := net.ListenPacket("udp4", netip.AddrPortFrom(ip, uint16(port)).String())
		if err != nil {
			ln.Close()
			continue
		}
		f.udp, f.tcp, f.addr = pc, ln, netip.AddrPortFrom(ip, uint16(port))
		break
	}
	if f.udp == nil {
		t.Fatal("no port free for both UDP and TCP")
	}
	reply := func(q []byte) []byte {
		var p dnsmessage.Parser
		h, err := p.Start(q)
		if err != nil {
			return nil
		}
		qs, err := p.AllQuestions()
		if err != nil || len(qs) == 0 {
			return nil
		}
		n, _ := f.queries.LoadOrStore(qs[0].Name.String(), new(int))
		*(n.(*int))++
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true})
		b.EnableCompression()
		b.StartQuestions()
		b.Question(qs[0])
		b.StartAnswers()
		if qs[0].Type == dnsmessage.TypeA {
			b.AResource(dnsmessage.ResourceHeader{Name: qs[0].Name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: answer})
		}
		out, _ := b.Finish()
		return out
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := f.udp.ReadFrom(buf)
			if err != nil {
				return
			}
			if r := reply(buf[:n]); r != nil {
				f.udp.WriteTo(r, from)
			}
		}
	}()
	go func() {
		for {
			c, err := f.tcp.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				var l [2]byte
				if _, err := io.ReadFull(c, l[:]); err != nil {
					return
				}
				q := make([]byte, binary.BigEndian.Uint16(l[:]))
				if _, err := io.ReadFull(c, q); err != nil {
					return
				}
				if r := reply(q); r != nil {
					binary.BigEndian.PutUint16(l[:], uint16(len(r)))
					c.Write(append(l[:], r...))
				}
			}()
		}
	}()
	t.Cleanup(func() { f.udp.Close(); f.tcp.Close() })
	return f
}

// wgClient is a WireGuard client with its own userspace network.
type wgClient struct {
	dev  *device.Device
	net  *netstack.Net
	http *http.Client
}

func startClient(t *testing.T, priv, serverPub string, addr netip.Addr, serverPort int) *wgClient {
	t.Helper()
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{addr}, []netip.Addr{netip.MustParseAddr("10.66.66.1")}, 1420)
	if err != nil {
		t.Fatal(err)
	}
	logger := device.NewLogger(device.LogLevelError, "client: ")
	dev := device.NewDevice(tdev, conn.NewDefaultBind(), logger)
	privHex, _ := keyToHex(priv)
	pubHex, _ := keyToHex(serverPub)
	err = dev.IpcSet(fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=127.0.0.1:%d\nallowed_ip=0.0.0.0/0\npersistent_keepalive_interval=1\n",
		privHex, pubHex, serverPort))
	if err != nil {
		t.Fatal(err)
	}
	if err := dev.Up(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dev.Close)
	c := &wgClient{dev: dev, net: tnet}
	timeout := 20 * time.Second
	if raceEnabled {
		timeout = 90 * time.Second
	}
	c.http = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:       tnet.DialContext,
			DisableKeepAlives: true,
		},
	}
	return c
}

func TestEndToEnd(t *testing.T) {
	host := hostIPv4(t)
	dns := startFakeDNS(t, host, [4]byte{203, 0, 113, 7})

	serverPriv, _ := GenerateKey()
	serverPub, _ := PublicKey(serverPriv)
	clientPriv, _ := GenerateKey()
	clientPub, _ := PublicKey(clientPriv)
	log := NewLog(400)
	log.SetEcho(func(s string) { t.Log(s) })
	srv, err := Start(Config{
		PrivateKey: serverPriv, ListenPort: 51999, WebPort: 18080, ConnectPort: 18080,
		DNS:   []string{dns.addr.String()},
		Peers: []PeerConfig{{Name: "laptop", PublicKey: clientPub, PrivateKey: clientPriv, Address: "10.66.66.2"}},
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	c := startClient(t, clientPriv, serverPub, netip.MustParseAddr("10.66.66.2"), 51999)

	t.Run("tcp_large_download", func(t *testing.T) {
		// The race detector slows every packet through two WireGuard
		// devices and two userspace stacks ~50x; keep that run short.
		down, up := 24<<20, 8<<20
		if raceEnabled {
			down, up = 4<<20, 2<<20
		}
		blob := make([]byte, down)
		rand.Read(blob)
		want := sha256.Sum256(blob)
		var posted int64
		ln, err := net.Listen("tcp4", netip.AddrPortFrom(host, 0).String())
		if err != nil {
			t.Fatal(err)
		}
		hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				posted, _ = io.Copy(io.Discard, r.Body)
				fmt.Fprint(w, posted)
				return
			}
			w.Write(blob)
		})}
		go hs.Serve(ln)
		defer hs.Close()
		url := "http://" + ln.Addr().String() + "/"

		start := time.Now()
		resp, err := c.http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		n, err := io.Copy(h, resp.Body)
		resp.Body.Close()
		if err != nil || n != int64(len(blob)) || !bytes.Equal(h.Sum(nil), want[:]) {
			t.Fatalf("download: %d bytes, %v, hash match %v", n, err, bytes.Equal(h.Sum(nil), want[:]))
		}
		t.Logf("%d MiB in %v (%.0f Mbit/s)", n>>20, time.Since(start), float64(n*8)/time.Since(start).Seconds()/1e6)

		resp, err = c.http.Post(url, "application/octet-stream", bytes.NewReader(blob[:up]))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != fmt.Sprint(up) {
			t.Fatalf("server received %q bytes", body)
		}
		if st := srv.Status(); st.BytesUp < int64(up) || st.BytesDown < int64(down) || st.TCPTotal < 2 {
			t.Fatalf("counters: up %d down %d total %d", st.BytesUp, st.BytesDown, st.TCPTotal)
		}
	})

	t.Run("tcp_refused_gets_rst", func(t *testing.T) {
		ln, _ := net.Listen("tcp4", netip.AddrPortFrom(host, 0).String())
		addr := ln.Addr().(*net.TCPAddr).AddrPort()
		ln.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		start := time.Now()
		cn, err := c.net.DialContextTCPAddrPort(ctx, addr)
		if err == nil {
			cn.Close()
			t.Fatal("dial to a closed port succeeded")
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("refusal took %v", d)
		}
	})

	t.Run("tcp_half_close", func(t *testing.T) {
		// The device sends, then shuts its write side; the server still
		// gets EOF and its reply still arrives.
		ln, err := net.Listen("tcp4", netip.AddrPortFrom(host, 0).String())
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			cn, err := ln.Accept()
			if err != nil {
				return
			}
			defer cn.Close()
			b, _ := io.ReadAll(cn) // until the device half-closes
			cn.Write(append([]byte("got:"), b...))
		}()
		cn, err := c.net.DialContextTCPAddrPort(context.Background(), ln.Addr().(*net.TCPAddr).AddrPort())
		if err != nil {
			t.Fatal(err)
		}
		defer cn.Close()
		cn.Write([]byte("request"))
		cn.CloseWrite()
		cn.SetDeadline(time.Now().Add(5 * time.Second))
		b, err := io.ReadAll(cn)
		if string(b) != "got:request" {
			t.Fatalf("reply %q %v", b, err)
		}
	})

	t.Run("udp_full_cone", func(t *testing.T) {
		a, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(host, 0)))
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		b, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(host, 0)))
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close()
		go func() {
			buf := make([]byte, 2000)
			for {
				n, from, err := a.ReadFromUDPAddrPort(buf)
				if err != nil {
					return
				}
				a.WriteToUDPAddrPort(buf[:n], from)
				if n < 100 { // B answers to the mapping A saw
					b.WriteToUDPAddrPort(append([]byte("other:"), buf[:n]...), from)
				}
			}
		}()
		uc, err := c.net.ListenUDPAddrPort(netip.MustParseAddrPort("10.66.66.2:40000"))
		if err != nil {
			t.Fatal(err)
		}
		defer uc.Close()
		aAddr := net.UDPAddrFromAddrPort(a.LocalAddr().(*net.UDPAddr).AddrPort())
		got := map[string]bool{}
		buf := make([]byte, 2000)
		for i := 0; i < 5 && !(got["hello"] && got["other:hello"]); i++ {
			uc.WriteTo([]byte("hello"), aAddr)
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) && !(got["hello"] && got["other:hello"]) {
				uc.SetReadDeadline(deadline)
				n, _, err := uc.ReadFrom(buf)
				if err != nil {
					break
				}
				got[string(buf[:n])] = true
			}
		}
		if !got["hello"] || !got["other:hello"] {
			t.Fatalf("got %v", got)
		}
		big := make([]byte, 1300)
		rand.Read(big)
		uc.WriteTo(big, aAddr)
		uc.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			n, _, err := uc.ReadFrom(buf)
			if err != nil {
				t.Fatalf("1300-byte echo: %v", err)
			}
			if bytes.Equal(buf[:n], big) {
				break
			}
		}
		if n := srv.Status().UDPFlows; n < 1 {
			t.Fatalf("udp_flows %d", n)
		}
	})

	t.Run("dns_via_gateway", func(t *testing.T) {
		addrs, err := c.net.LookupHost("example.test")
		if err != nil || len(addrs) == 0 || addrs[0] != "203.0.113.7" {
			t.Fatalf("lookup: %v %v", addrs, err)
		}
	})

	t.Run("dns_over_tcp_via_gateway", func(t *testing.T) {
		cn, err := c.net.DialContextTCPAddrPort(context.Background(), netip.MustParseAddrPort("10.66.66.1:53"))
		if err != nil {
			t.Fatal(err)
		}
		defer cn.Close()
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
		b.StartQuestions()
		b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName("tcp.test."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
		q, _ := b.Finish()
		cn.Write(append([]byte{byte(len(q) >> 8), byte(len(q))}, q...))
		cn.SetDeadline(time.Now().Add(5 * time.Second))
		var l [2]byte
		if _, err := io.ReadFull(cn, l[:]); err != nil {
			t.Fatal(err)
		}
		r := make([]byte, binary.BigEndian.Uint16(l[:]))
		if _, err := io.ReadFull(cn, r); err != nil {
			t.Fatal(err)
		}
		var m dnsmessage.Message
		if err := m.Unpack(r); err != nil || len(m.Answers) != 1 || m.Header.ID != 7 {
			t.Fatalf("answer %+v %v", m, err)
		}
	})

	t.Run("ping_gateway", func(t *testing.T) {
		if err := ping(c, netip.MustParseAddr("10.66.66.1")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ping_forwarded", func(t *testing.T) {
		if err := ping(c, host); err != nil {
			if srv.ping.warned[0].Load() {
				t.Skip("ICMP sockets unavailable on this host")
			}
			t.Fatal(err)
		}
	})

	t.Run("config_page_through_tunnel", func(t *testing.T) {
		for _, base := range []string{"http://10.66.66.1:18080", "http://172.20.10.1:18080"} {
			resp, err := c.http.Get(base + "/")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !strings.Contains(string(body), "/conf/0/laptop.conf") {
				t.Fatalf("%s: index:\n%s", base, body)
			}
		}
		resp, err := c.http.Get("http://10.66.66.1:18080/conf/0/laptop.conf")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), "PublicKey = "+serverPub) || !strings.Contains(string(body), "AllowedIPs = 0.0.0.0/0") {
			t.Fatalf("conf:\n%s", body)
		}
		if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="laptop.conf"` {
			t.Fatalf("Content-Disposition %q", cd)
		}
		resp, err = c.http.Get("http://10.66.66.1:18080/nope")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("unknown path: %d", resp.StatusCode)
		}
	})

	t.Run("gateway_closed_port_rst", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cn, err := c.net.DialContextTCPAddrPort(ctx, netip.MustParseAddrPort("10.66.66.1:9")); err == nil {
			cn.Close()
			t.Fatal("gateway port 9 accepted")
		}
	})

	t.Run("status", func(t *testing.T) {
		st := srv.Status()
		if len(st.Peers) != 1 || st.Peers[0].LastHandshake == 0 || st.Peers[0].RxBytes == 0 || st.Peers[0].Endpoint == "" {
			t.Fatalf("peers %+v", st.Peers)
		}
		if !st.Running || st.ListenPort != 51999 || st.EgressIf != "default route" || st.Pairing == nil {
			t.Fatalf("status %+v", st)
		}
	})

	t.Run("set_peers_live", func(t *testing.T) {
		ipadPriv, _ := GenerateKey()
		ipadPub, _ := PublicKey(ipadPriv)
		peers := append(srv.Config().Peers, PeerConfig{Name: "ipad", PublicKey: ipadPub, Address: "10.66.66.3"})
		hsBefore := srv.Status().Peers[0].LastHandshake
		if err := srv.SetPeers(peers); err != nil {
			t.Fatal(err)
		}
		addrs, err := c.net.LookupHost("after-set-peers.test")
		if err != nil || len(addrs) == 0 || addrs[0] != "203.0.113.7" {
			t.Fatalf("lookup after SetPeers: %v %v", addrs, err)
		}
		st := srv.Status()
		if len(st.Peers) != 2 || st.Peers[0].LastHandshake != hsBefore {
			t.Fatalf("existing session disturbed: %+v (handshake was %d)", st.Peers, hsBefore)
		}
	})

	t.Run("udp_reap", func(t *testing.T) {
		if srv.udp.count() == 0 {
			t.Skip("no flows")
		}
		srv.udp.reap(udpIdle) // nothing is 3 minutes old
		if srv.udp.count() == 0 {
			t.Fatal("reaped live flows")
		}
		srv.udp.mu.Lock()
		for _, f := range srv.udp.flows {
			f.last.Store(time.Now().Add(-udpIdle - time.Second).UnixNano())
		}
		srv.udp.mu.Unlock()
		srv.udp.reap(udpIdle)
		if n := srv.udp.count(); n != 0 {
			t.Fatalf("%d flows left", n)
		}
	})

	if fl := srv.Config().Peers; len(fl) == 0 || strings.Contains(log.Tail(), clientPriv) || strings.Contains(log.Tail(), serverPriv) {
		t.Fatal("a private key reached the log")
	}
}

// ping sends one ICMP echo (id 0x1234, seq 1, "hi") from the client and
// waits up to 3 s for the reply.
func ping(c *wgClient, dst netip.Addr) error {
	pc, err := c.net.DialPingAddr(netip.MustParseAddr("10.66.66.2"), dst)
	if err != nil {
		return err
	}
	defer pc.Close()
	req := []byte{8, 0, 0, 0, 0x12, 0x34, 0, 1, 'h', 'i'}
	binary.BigEndian.PutUint16(req[2:], csumFold(csumAdd(0, req)))
	if _, err := pc.Write(req); err != nil {
		return err
	}
	pc.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	for {
		n, err := pc.Read(buf)
		if err != nil {
			return err
		}
		if n >= 8 && buf[0] == 0 && string(buf[8:n]) == "hi" {
			return nil
		}
	}
}

// Removing a device leaves exactly the remaining one in WireGuard.
func TestSetPeersRemove(t *testing.T) {
	priv, _ := GenerateKey()
	ka, _ := GenerateKey()
	kb, _ := GenerateKey()
	pa, _ := PublicKey(ka)
	pb, _ := PublicKey(kb)
	a := PeerConfig{Name: "a", PublicKey: pa, Address: "10.66.66.2"}
	b := PeerConfig{Name: "b", PublicKey: pb, Address: "10.66.66.3"}
	srv, err := Start(Config{PrivateKey: priv, ListenPort: 51998, Peers: []PeerConfig{a, b}}, NewLog(50))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := srv.SetPeers([]PeerConfig{b}); err != nil {
		t.Fatal(err)
	}
	ipc, _ := srv.wg.IpcGet()
	if n := strings.Count(ipc, "public_key="); n != 1 {
		t.Fatalf("%d peers left:\n%s", n, ipc)
	}
	bHex, _ := keyToHex(pb)
	if !strings.Contains(ipc, "public_key="+bHex) {
		t.Fatal("the wrong device was removed")
	}
}

func TestDetectNetInfoDefaultIf(t *testing.T) {
	ni := DetectNetInfo()
	t.Logf("%+v", ni)
	if ni.DefaultIf == "" {
		t.Skip("no default route on this machine")
	}
}
