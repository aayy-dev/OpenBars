// Command fakedevice acts as a device connected to Open Bars, for testing
// sharing without phones: run Open Bars in the iOS Simulator (sharing in the
// app, Settings › Use cellular data only off), then from the same Mac:
//
//	go run ./cmd/fakedevice pair              # ask to pair; tap Allow in the Simulator
//	go run ./cmd/fakedevice download 50MB     # pull data through the tunnel, with speed
//	go run ./cmd/fakedevice status            # its usage and limits (the status page)
//	go run ./cmd/fakedevice ask 15m "homework"
//	go run ./cmd/fakedevice watch             # print changes to its limits
//
// It runs a real WireGuard client (wireguard-go on a userspace network) in
// this process, so nothing on the Mac changes. Its key and pairing are kept
// in a state file (-state), so it stays the same device between runs.
//
// The Simulator shares the Mac's network, so the engine is at 127.0.0.1.
// Downloads come from a small server this command starts on the Mac's own
// LAN address (the engine never forwards to loopback), so they use no
// internet data.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

var (
	phone    = flag.String("phone", "127.0.0.1", "the sharing iPhone's (or Simulator's) address for pairing")
	apiPort  = flag.Int("port", 8080, "its pairing API port")
	endpoint = flag.String("endpoint", "", "WireGuard endpoint, host:port (default: -phone and the port the iPhone gives when pairing)")
	stateArg = flag.String("state", "fakedevice.json", "file keeping this device's key and pairing")
	name     = flag.String("name", "Test Device", "name to pair with")
	kind     = flag.String("kind", "ipad", "kind to pair as: mac, ipad, iphone, windows or android")
)

// state is what's kept between runs.
type state struct {
	PrivateKey string        `json:"private_key"`
	Pairing    *pairResponse `json:"pairing,omitempty"`
}

// pairResponse is the engine's PairResponse.
type pairResponse struct {
	ServerPublicKey string `json:"server_public_key"`
	Endpoint        string `json:"endpoint"`
	Address         string `json:"address"`
	DNS             string `json:"dns"`
	MTU             int    `json:"mtu"`
	DeviceName      string `json:"device_name"`
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: fakedevice [flags] command

commands:
  key                 print this device's public key (made on first use)
  pair                ask the iPhone to pair, waiting for Allow
  download SIZE       download SIZE (e.g. 50MB, 1GB) through the tunnel
  status              this device's usage and limits (from the status page)
  ask WHAT [MESSAGE]  ask for more: 15m, 1h, today, 250mb or 1gb
  page                the status page, as text
  watch               print changes to its limits until Ctrl-C
  forget              delete the state file

flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "key":
		err = cmdKey()
	case "pair":
		err = cmdPair()
	case "download":
		if len(args) < 2 {
			err = errors.New("download needs a size, e.g. 50MB")
		} else {
			err = cmdDownload(args[1])
		}
	case "status":
		err = cmdStatus()
	case "ask":
		if len(args) < 2 {
			err = errors.New("ask needs what: 15m, 1h, today, 250mb or 1gb")
		} else {
			err = cmdAsk(args[1], strings.Join(args[2:], " "))
		}
	case "page":
		err = cmdPage()
	case "watch":
		err = cmdWatch()
	case "forget":
		err = os.Remove(*stateArg)
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakedevice:", err)
		os.Exit(1)
	}
}

// MARK: state and keys

func load() (*state, error) {
	var st state
	b, err := os.ReadFile(*stateArg)
	if err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			return nil, fmt.Errorf("%s: %v", *stateArg, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if st.PrivateKey == "" {
		var k [32]byte
		rand.Read(k[:])
		k[0] &= 248
		k[31] = (k[31] & 127) | 64
		st.PrivateKey = base64.StdEncoding.EncodeToString(k[:])
		if err := save(&st); err != nil {
			return nil, err
		}
	}
	return &st, nil
}

func save(st *state) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(*stateArg, b, 0o600)
}

func publicKey(priv string) string {
	k, _ := base64.StdEncoding.DecodeString(priv)
	pub, _ := curve25519.X25519(k, curve25519.Basepoint)
	return base64.StdEncoding.EncodeToString(pub)
}

func hexKey(b64 string) string {
	b, _ := base64.StdEncoding.DecodeString(b64)
	return hex.EncodeToString(b)
}

func cmdKey() error {
	st, err := load()
	if err != nil {
		return err
	}
	fmt.Println(publicKey(st.PrivateKey))
	return nil
}

// MARK: pairing

func apiURL(path string) string {
	return "http://" + net.JoinHostPort(*phone, strconv.Itoa(*apiPort)) + path
}

// cmdPair asks to pair (or picks up a pairing already allowed).
func cmdPair() error {
	st, err := load()
	if err != nil {
		return err
	}
	pub := publicKey(st.PrivateKey)
	if p, err := pairedAlready(pub); err == nil && p != nil {
		st.Pairing = p
		fmt.Printf("Already paired as %q at %s.\n", p.DeviceName, p.Address)
		return save(st)
	}
	body, _ := json.Marshal(map[string]string{"name": *name, "kind": *kind, "public_key": pub})
	fmt.Printf("Asking to pair as %q (%s). Tap Allow on the iPhone...\n", *name, short(pub))
	c := &http.Client{Timeout: 100 * time.Second}
	resp, err := c.Post(apiURL("/api/pair"), "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("pairing request: %v (is sharing on, and -phone/-port right?)", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusRequestTimeout:
		return errors.New("not allowed yet. The request stays open for 10 minutes: tap Allow, then run pair again")
	case http.StatusForbidden:
		return errors.New("the iPhone didn't allow it")
	default:
		return fmt.Errorf("pairing: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var p pairResponse
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	st.Pairing = &p
	fmt.Printf("Paired as %q at %s.\n", p.DeviceName, p.Address)
	return save(st)
}

func pairedAlready(pub string) (*pairResponse, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(apiURL("/api/pair?public_key=" + url.QueryEscape(pub)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	var p pairResponse
	return &p, json.NewDecoder(resp.Body).Decode(&p)
}

func short(k string) string {
	if len(k) > 8 {
		return k[:8] + "…"
	}
	return k
}

// MARK: the tunnel

type tunnel struct {
	dev  *device.Device
	net  *netstack.Net
	http *http.Client
}

// up brings the WireGuard client up, pairing first if needed.
func up() (*tunnel, error) {
	st, err := load()
	if err != nil {
		return nil, err
	}
	if p, err := pairedAlready(publicKey(st.PrivateKey)); err == nil && p != nil {
		st.Pairing = p // the address may have changed
		save(st)
	}
	p := st.Pairing
	if p == nil {
		return nil, errors.New("not paired yet: run fakedevice pair first")
	}
	addr, err := netip.ParseAddr(p.Address)
	if err != nil {
		return nil, fmt.Errorf("pairing address %q: %v", p.Address, err)
	}
	dns, _ := netip.ParseAddr(p.DNS)
	mtu := p.MTU
	if mtu == 0 {
		mtu = 1420
	}
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{addr}, []netip.Addr{dns}, mtu)
	if err != nil {
		return nil, err
	}
	ep := *endpoint
	if ep == "" {
		_, port, _ := net.SplitHostPort(p.Endpoint)
		if port == "" {
			port = "51820"
		}
		ep = net.JoinHostPort(*phone, port)
	}
	dev := device.NewDevice(tdev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "wireguard: "))
	err = dev.IpcSet(fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\nallowed_ip=::/0\npersistent_keepalive_interval=5\n",
		hexKey(st.PrivateKey), hexKey(p.ServerPublicKey), ep))
	if err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, err
	}
	return &tunnel{dev: dev, net: tnet, http: &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{DialContext: tnet.DialContext, DisableKeepAlives: true},
		// The status page answers a request with a redirect back to itself.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (t *tunnel) close() { t.dev.Close() }

const gateway = "http://10.66.66.1"

// MARK: download

// cmdDownload pulls size bytes through the tunnel from a server on this Mac,
// printing progress every second. It stops early if the engine cuts the
// device off (blocked) and shows the speed it settles at (slowed).
func cmdDownload(sizeArg string) error {
	size, err := parseSize(sizeArg)
	if err != nil {
		return err
	}
	host, err := lanIPv4()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp4", netip.AddrPortFrom(host, 0).String())
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.ParseInt(r.URL.Query().Get("n"), 10, 64)
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		buf := make([]byte, 64<<10)
		for n > 0 {
			k := int64(len(buf))
			if k > n {
				k = n
			}
			if _, err := w.Write(buf[:k]); err != nil {
				return
			}
			n -= k
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	t, err := up()
	if err != nil {
		return err
	}
	defer t.close()
	client := &http.Client{Transport: &http.Transport{DialContext: t.net.DialContext, DisableKeepAlives: true}}
	src := fmt.Sprintf("http://%s/?n=%d", ln.Addr(), size)
	fmt.Printf("Downloading %s through the tunnel from %s...\n", human(size), ln.Addr())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	start := time.Now()
	type result struct {
		n   int64
		err error
	}
	var got int64
	progress := make(chan int64, 64)
	done := make(chan result, 1)
	go func() {
		resp, err := client.Do(req)
		if err != nil {
			done <- result{0, err}
			return
		}
		defer resp.Body.Close()
		buf := make([]byte, 64<<10)
		var n int64
		for {
			k, err := resp.Body.Read(buf)
			n += int64(k)
			progress <- n
			if err == io.EOF {
				done <- result{n, nil}
				return
			}
			if err != nil {
				done <- result{n, err}
				return
			}
		}
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var last int64
	stalled := 0
	for {
		select {
		case got = <-progress:
		case r := <-done:
			el := time.Since(start)
			if r.err != nil {
				fmt.Printf("Stopped after %s in %s: %v\n", human(r.n), el.Round(100*time.Millisecond), r.err)
				fmt.Println("(Blocked by a limit? Run: fakedevice status)")
				return nil
			}
			fmt.Printf("Done: %s in %s (%s)\n", human(r.n), el.Round(100*time.Millisecond), rate(float64(r.n)/el.Seconds()))
			return nil
		case <-tick.C:
			fmt.Printf("  %s of %s  %s\n", human(got), human(size), rate(float64(got-last)))
			if got == last {
				stalled++
				if stalled >= 10 {
					cancel()
					fmt.Printf("Nothing for 10 seconds after %s: the device looks blocked. Run: fakedevice status\n", human(got))
					return nil
				}
			} else {
				stalled = 0
			}
			last = got
		}
	}
}

// lanIPv4 is a non-loopback IPv4 address of this Mac.
func lanIPv4() (netip.Addr, error) {
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
					ip = ip.Unmap()
					if ip.Is4() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
						return ip, nil
					}
				}
			}
		}
	}
	return netip.Addr{}, errors.New("no non-loopback IPv4 address on this Mac")
}

func parseSize(s string) (int64, error) {
	m := regexp.MustCompile(`(?i)^\s*([\d.]+)\s*(b|kb|mb|gb)?\s*$`).FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("size %q: use e.g. 500KB, 50MB, 1.5GB", s)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	mult := map[string]float64{"": 1, "b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9}[strings.ToLower(m[2])]
	return int64(v * mult), nil
}

func human(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.2f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.0f KB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d B", n)
}

func rate(bps float64) string {
	return fmt.Sprintf("%.2f Mbps", bps*8/1e6)
}

// MARK: status page

// deviceView mirrors the engine's DeviceView (status.json).
type deviceView struct {
	Name  string `json:"name"`
	Limit struct {
		State  string `json:"state"`
		Action string `json:"action"`
		Cap    bool   `json:"cap"`
		Rules  []struct {
			Period string `json:"period"`
			Bytes  int64  `json:"bytes"`
			Used   int64  `json:"used"`
			Resets int64  `json:"resets"`
		} `json:"rules"`
		Grants []struct {
			Bytes  int64 `json:"bytes"`
			Left   int64 `json:"left"`
			Until  int64 `json:"until"`
			Active bool  `json:"active"`
		} `json:"grants"`
	} `json:"limit"`
	Ask *struct {
		ID      string `json:"id"`
		State   string `json:"state"`
		Minutes int    `json:"minutes"`
		Today   bool   `json:"today"`
		Bytes   int64  `json:"bytes"`
		Message string `json:"message"`
	} `json:"ask"`
	Cap []struct {
		Period string `json:"period"`
		Bytes  int64  `json:"bytes"`
		Used   int64  `json:"used"`
	} `json:"cap"`
}

func fetchView(t *tunnel) (*deviceView, error) {
	resp, err := t.http.Get(gateway + "/status.json")
	if err != nil {
		return nil, fmt.Errorf("status page: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status page: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var v deviceView
	return &v, json.NewDecoder(resp.Body).Decode(&v)
}

func describe(v *deviceView) string {
	var b strings.Builder
	state := v.Limit.State
	if state == "" {
		state = "ok"
	}
	fmt.Fprintf(&b, "%s: %s", v.Name, state)
	if v.Limit.Action != "" {
		fmt.Fprintf(&b, " (%s)", v.Limit.Action)
	}
	if v.Limit.Cap {
		b.WriteString(", all-devices limit reached")
	}
	b.WriteString("\n")
	for _, r := range v.Limit.Rules {
		resets := "never resets"
		if r.Resets > 0 {
			resets = "resets " + time.Unix(r.Resets, 0).Format("Mon 3:04 PM")
		}
		fmt.Fprintf(&b, "  %-6s %s of %s, %s\n", r.Period, human(r.Used), human(r.Bytes), resets)
	}
	for _, r := range v.Cap {
		fmt.Fprintf(&b, "  all devices, %s: %s of %s\n", r.Period, human(r.Used), human(r.Bytes))
	}
	for _, g := range v.Limit.Grants {
		if !g.Active {
			continue
		}
		if g.Until > 0 {
			fmt.Fprintf(&b, "  extra: no limit until %s\n", time.Unix(g.Until, 0).Format("3:04 PM"))
		} else {
			fmt.Fprintf(&b, "  extra: %s left\n", human(g.Left))
		}
	}
	if a := v.Ask; a != nil {
		what := human(a.Bytes) + " more"
		switch {
		case a.Today:
			what = "the rest of today"
		case a.Minutes > 0:
			what = fmt.Sprintf("%d more minutes", a.Minutes)
		}
		fmt.Fprintf(&b, "  request for %s: %s\n", what, a.State)
	}
	return b.String()
}

func cmdStatus() error {
	t, err := up()
	if err != nil {
		return err
	}
	defer t.close()
	v, err := fetchView(t)
	if err != nil {
		return err
	}
	fmt.Print(describe(v))
	return nil
}

func cmdAsk(what, message string) error {
	t, err := up()
	if err != nil {
		return err
	}
	defer t.close()
	resp, err := t.http.PostForm(gateway+"/ask", url.Values{"what": {strings.ToLower(what)}, "message": {message}})
	if err != nil {
		return err
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); strings.Contains(loc, "e=") {
		u, _ := url.Parse(loc)
		return errors.New(u.Query().Get("e"))
	}
	v, err := fetchView(t)
	if err != nil {
		return err
	}
	fmt.Print(describe(v))
	return nil
}

func cmdPage() error {
	t, err := up()
	if err != nil {
		return err
	}
	defer t.close()
	resp, err := t.http.Get(gateway + "/")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	text := regexp.MustCompile(`(?s)<style.*?</style>|<[^>]+>`).ReplaceAllString(string(b), "\n")
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			fmt.Println(line)
		}
	}
	return nil
}

func cmdWatch() error {
	t, err := up()
	if err != nil {
		return err
	}
	defer t.close()
	last := ""
	for {
		v, err := fetchView(t)
		now := time.Now().Format("15:04:05")
		if err != nil {
			if last != err.Error() {
				fmt.Println(now, err)
				last = err.Error()
			}
		} else {
			d := describe(v)
			// Usage changes every second; print when anything else does.
			key := regexp.MustCompile(`[\d.]+ (GB|MB|KB|B) of`).ReplaceAllString(d, "of")
			if key != last {
				fmt.Print(now, " ", d)
				last = key
			}
		}
		time.Sleep(2 * time.Second)
	}
}
