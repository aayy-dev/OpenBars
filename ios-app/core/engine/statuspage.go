package engine

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// The status page is what a connected device sees at http://10.66.66.1:
// its usage and limits, and a form to ask the iPhone for more time or data.
// It's served right here in the userspace stack, never through a socket,
// so the address a request comes from is the device's tunnel address,
// which WireGuard ties to the device's key: no one can ask on another
// device's behalf. It stays reachable while the device is blocked.

const (
	statusPagePort = 80
	askCooldown    = 10 * time.Minute // after a "no"
	askExpiry      = 2 * time.Hour    // unanswered requests
	asksPerHour    = 6
	askMessageMax  = 200
)

// Ask states.
const (
	AskPending  = "pending"
	AskApproved = "approved"
	AskDenied   = "denied"
	AskExpired  = "expired"
)

// Ask is a device asking for more time or data.
type Ask struct {
	ID      string `json:"id"`
	Key     string `json:"key"` // device public key
	Name    string `json:"name"`
	Minutes int    `json:"minutes,omitempty"` // no limit for this long
	Today   bool   `json:"today,omitempty"`   // no limit for the rest of today
	Bytes   int64  `json:"bytes,omitempty"`   // this much more data
	Message string `json:"message,omitempty"`
	Since   int64  `json:"since"` // unix seconds
	State   string `json:"state"`
	// Answered is when it was approved or denied (unix seconds).
	Answered int64 `json:"answered,omitempty"`
}

// askChoices are the form's options.
var askChoices = []struct {
	id, label string
	minutes   int
	today     bool
	bytes     int64
}{
	{"15m", "15 more minutes", 15, false, 0},
	{"1h", "1 more hour", 60, false, 0},
	{"today", "The rest of today", 0, true, 0},
	{"250mb", "250 MB more", 0, false, 250_000_000},
	{"1gb", "1 GB more", 0, false, 1_000_000_000},
}

type askBook struct {
	mu     sync.Mutex
	latest map[string]*Ask        // by device key
	times  map[string][]time.Time // recent asks per device, for the hourly limit
}

// expire marks old unanswered requests. Caller holds mu.
func (b *askBook) expire(now time.Time) {
	for _, a := range b.latest {
		if a.State == AskPending && now.Unix()-a.Since > int64(askExpiry.Seconds()) {
			a.State = AskExpired
		}
	}
}

// pending lists unanswered requests, oldest first.
func (b *askBook) pending(now time.Time) []Ask {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(now)
	out := []Ask{}
	for _, a := range b.latest {
		if a.State == AskPending {
			out = append(out, *a)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Since < out[j-1].Since; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// latestFor is a device's most recent request, if any.
func (b *askBook) latestFor(key string, now time.Time) *Ask {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(now)
	if a := b.latest[key]; a != nil {
		c := *a
		return &c
	}
	return nil
}

// add records a new request, or says why it can't be made now.
func (b *askBook) add(a Ask, now time.Time) (Ask, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.latest == nil {
		b.latest = map[string]*Ask{}
		b.times = map[string][]time.Time{}
	}
	b.expire(now)
	if prev := b.latest[a.Key]; prev != nil {
		if prev.State == AskPending {
			return Ask{}, "You already asked. Wait for an answer on the iPhone."
		}
		if prev.State == AskDenied && now.Sub(time.Unix(prev.Answered, 0)) < askCooldown {
			return Ask{}, "Your last request was declined. Try again in a few minutes."
		}
	}
	recent := b.times[a.Key][:0]
	for _, t := range b.times[a.Key] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= asksPerHour {
		b.times[a.Key] = recent
		return Ask{}, "That's a lot of requests for one hour. Try again later."
	}
	b.times[a.Key] = append(recent, now)
	a.ID = newAskID()
	a.Since = now.Unix()
	a.State = AskPending
	c := a
	b.latest[a.Key] = &c
	return a, ""
}

// answer marks a request approved or denied; false if it isn't pending.
func (b *askBook) answer(id, state string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, a := range b.latest {
		if a.ID == id && a.State == AskPending {
			a.State = state
			a.Answered = now.Unix()
			return true
		}
	}
	return false
}

func newAskID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// DenyAsk declines a device's request (the app's "Don't Allow").
func (s *Server) DenyAsk(id string) bool {
	ok := s.asks.answer(id, AskDenied, time.Now())
	if ok {
		s.log.Infof("limits", "Request %s declined in the app", id)
	}
	return ok
}

// noteGrants marks requests approved when the app adds a grant for them.
func (s *Server) noteGrants(peers []PeerConfig) {
	now := time.Now()
	for _, p := range peers {
		if p.Limit == nil {
			continue
		}
		for _, g := range p.Limit.Grants {
			if g.Ask != "" && s.asks.answer(g.Ask, AskApproved, now) {
				s.log.Infof("limits", "%q's request was approved", p.Name)
			}
		}
	}
}

// chanListener hands the status page connections from the userspace stack.
type chanListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newChanListener() *chanListener {
	return &chanListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(10, 66, 66, 1), Port: statusPagePort}
}

func (l *chanListener) deliver(c net.Conn) {
	select {
	case l.conns <- c:
	case <-l.done:
		c.Close()
	}
}

// startStatusPage starts the HTTP server behind the status page.
func (s *Server) startStatusPage() {
	s.statusLn = newChanListener()
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.statusPage)
	mux.HandleFunc("/ask", s.statusAsk)
	mux.HandleFunc("/status.json", s.statusJSON)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
		ErrorLog:          log.New(webErrorLog{s.log}, "", 0),
	}
	s.webs = append(s.webs, srv)
	go srv.Serve(s.statusLn)
}

// serveStatusConn completes a device's connection to the status page in
// the userspace stack and hands it to the HTTP server.
func (s *Server) serveStatusConn(r *tcp.ForwarderRequest) {
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		r.Complete(true)
		return
	}
	r.Complete(false)
	s.statusLn.deliver(gonet.NewTCPConn(&wq, ep))
}

// peerAt is the device with this tunnel address.
func (s *Server) peerAt(remote string) (PeerConfig, bool) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return PeerConfig{}, false
	}
	a = a.Unmap()
	for _, p := range s.Config().Peers {
		pa, err := netip.ParseAddr(strings.TrimSpace(p.Address))
		if err != nil {
			continue
		}
		if pa.Unmap() == a {
			return p, true
		}
		if v6, ok := peerAddr6(pa); ok && v6 == a {
			return p, true
		}
	}
	return PeerConfig{}, false
}

// DeviceView is the status page's data, also served as /status.json.
type DeviceView struct {
	Name  string       `json:"name"`
	Limit LimitStatus  `json:"limit"`
	Ask   *Ask         `json:"ask,omitempty"`
	Cap   []RuleStatus `json:"cap,omitempty"`
	Phone string       `json:"phone"` // the iPhone's name
	TZ    string       `json:"time_zone"`
}

func (s *Server) deviceView(p PeerConfig) DeviceView {
	st, capRules := s.limitStatus(p.PublicKey)
	return DeviceView{
		Name:  p.Name,
		Limit: st,
		Ask:   s.asks.latestFor(p.PublicKey, time.Now()),
		Cap:   capRules,
		Phone: s.Config().HotspotSSID,
		TZ:    s.stats.loc.String(),
	}
}

func (s *Server) statusJSON(w http.ResponseWriter, r *http.Request) {
	p, ok := s.peerAt(r.RemoteAddr)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a connected device"})
		return
	}
	writeJSON(w, http.StatusOK, s.deviceView(p))
}

func (s *Server) statusAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	p, ok := s.peerAt(r.RemoteAddr)
	if !ok {
		http.Error(w, "This page is for devices connected through Open Bars.", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	choice := r.PostForm.Get("what")
	a := Ask{Key: p.PublicKey, Name: p.Name}
	found := false
	for _, c := range askChoices {
		if c.id == choice {
			a.Minutes, a.Today, a.Bytes = c.minutes, c.today, c.bytes
			found = true
		}
	}
	if !found {
		http.Redirect(w, r, "/?e="+urlQueryEscape("Choose what to ask for."), http.StatusSeeOther)
		return
	}
	a.Message = cleanMessage(r.PostForm.Get("message"))
	made, why := s.asks.add(a, time.Now())
	if why != "" {
		http.Redirect(w, r, "/?e="+urlQueryEscape(why), http.StatusSeeOther)
		return
	}
	s.log.Infof("limits", "%q is asking for %s", p.Name, askWhat(made))
	s.events.add(Event{Kind: EventAsk, Key: p.PublicKey, Name: p.Name, Ask: &made})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// cleanMessage keeps a short single line of printable text.
func cleanMessage(m string) string {
	m = strings.Join(strings.Fields(m), " ")
	if !utf8.ValidString(m) {
		m = strings.ToValidUTF8(m, "")
	}
	if len([]rune(m)) > askMessageMax {
		m = string([]rune(m)[:askMessageMax])
	}
	return m
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// askWhat says what a request is for.
func askWhat(a Ask) string {
	switch {
	case a.Today:
		return "the rest of today"
	case a.Minutes == 60:
		return "1 more hour"
	case a.Minutes > 0:
		return fmt.Sprintf("%d more minutes", a.Minutes)
	case a.Bytes > 0:
		return humanBytes(a.Bytes) + " more"
	}
	return "more"
}

// humanBytes formats like iOS does (decimal units): "250 MB", "1.2 GB".
func humanBytes(n int64) string {
	f := float64(n)
	switch {
	case n >= 1_000_000_000:
		return trimZero(fmt.Sprintf("%.1f", f/1e9)) + " GB"
	case n >= 1_000_000:
		return fmt.Sprintf("%.0f MB", f/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0f KB", f/1e3)
	}
	return fmt.Sprintf("%d bytes", n)
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

// periodLabel names a rule's period on the page.
func periodLabel(p string) string {
	switch p {
	case PeriodHour:
		return "This hour"
	case PeriodDay:
		return "Today"
	case PeriodWeek:
		return "This week"
	case PeriodMonth:
		return "This month"
	}
	return "In total"
}

// whenText says when a moment is, from now, in the phone's time zone.
func whenText(t, now time.Time, loc *time.Location) string {
	t, now = t.In(loc), now.In(loc)
	clock := t.Format("3:04 PM")
	if t.Hour() == 0 && t.Minute() == 0 {
		clock = "midnight"
	}
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	days := int(time.Date(y2, m2, d2, 0, 0, 0, 0, loc).Sub(time.Date(y1, m1, d1, 0, 0, 0, 0, loc)).Hours() / 24)
	switch {
	case days == 0:
		return "today at " + clock
	case days == 1 && clock == "midnight":
		return "at midnight"
	case days == 1:
		return "tomorrow at " + clock
	case days < 7:
		return t.Format("Monday") + " at " + clock
	}
	return t.Format("Jan 2")
}

func (s *Server) statusPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	p, ok := s.peerAt(r.RemoteAddr)
	if !ok {
		fmt.Fprintf(w, statusPageHTML, "Open Bars", `<div class=card><p>This page shows a device's data limits. Open it from a device connected through Open Bars.</p></div>`)
		return
	}
	v := s.deviceView(p)
	now := time.Now()
	loc := s.stats.loc
	var b strings.Builder

	// What's happening now.
	state, detail := "Connected", "No limits right now."
	if len(v.Limit.Rules) > 0 {
		detail = "Within your limits."
	}
	switch v.Limit.State {
	case StatePaused:
		state, detail = "Paused", "The iPhone's owner paused internet for this device."
	case StateLimited:
		what := "limit"
		if v.Limit.Cap {
			what = "the limit for all devices"
		}
		switch v.Limit.Action {
		case ActionBlock:
			state, detail = "Limit reached", "You've used "+what+", so internet is off until it resets or you get more."
		case ActionSlow:
			state, detail = "Slowed down", "You've used "+what+", so internet is slower until it resets or you get more."
		default:
			state, detail = "Limit reached", "You've used "+what+". The iPhone's owner has been told."
		}
	case StateExtended:
		state, detail = "Extra time or data", "You're past a limit, but the iPhone's owner gave you more."
	case StateWarn:
		state, detail = "Almost at your limit", "You've used most of a limit."
	}
	fmt.Fprintf(&b, `<div class=card><p class=state>%s</p><p>%s</p></div>`, html.EscapeString(state), html.EscapeString(detail))
	if msg := r.URL.Query().Get("e"); msg != "" {
		fmt.Fprintf(&b, `<div class="card warn"><p>%s</p></div>`, html.EscapeString(msg))
	}

	// Limits.
	if len(v.Limit.Rules) > 0 || len(v.Cap) > 0 {
		b.WriteString(`<h2>Limits</h2><div class=card>`)
		row := func(label string, r RuleStatus) {
			pct := 0
			if r.Bytes > 0 {
				pct = int(r.Used * 100 / r.Bytes)
				if pct > 100 {
					pct = 100
				}
			}
			resets := "Never resets."
			if r.Resets > 0 {
				resets = "Resets " + whenText(time.Unix(r.Resets, 0), now, loc) + "."
			}
			fmt.Fprintf(&b, `<div class=rule><p><b>%s</b> %s of %s</p><div class=bar><i style="width:%d%%"%s></i></div><p class=muted>%s</p></div>`,
				html.EscapeString(label), humanBytes(r.Used), humanBytes(r.Bytes), pct, map[bool]string{true: ` class=over`}[r.over()], resets)
		}
		for _, r := range v.Limit.Rules {
			row(periodLabel(r.Period), r)
		}
		for _, r := range v.Cap {
			row(periodLabel(r.Period)+", all devices", r)
		}
		b.WriteString(`</div>`)
	}

	// Grants.
	for _, g := range v.Limit.Grants {
		if !g.Active {
			continue
		}
		if g.Until > 0 {
			fmt.Fprintf(&b, `<div class=card><p>No limit until %s.</p></div>`, html.EscapeString(whenText(time.Unix(g.Until, 0), now, loc)))
		} else {
			fmt.Fprintf(&b, `<div class=card><p>%s of extra data left.</p></div>`, humanBytes(g.Left))
		}
	}

	// Asking for more.
	if len(v.Limit.Rules) > 0 || v.Limit.State == StateLimited || v.Limit.State == StatePaused {
		b.WriteString(`<h2>Need more?</h2>`)
		if a := v.Ask; a != nil && a.State == AskPending {
			fmt.Fprintf(&b, `<div class=card><p>You asked for %s %s. The iPhone's owner will see it in a notification.</p></div>`,
				html.EscapeString(askWhat(*a)), html.EscapeString(whenText(time.Unix(a.Since, 0), now, loc)))
		} else {
			if a := v.Ask; a != nil && now.Unix()-a.Answered < 3600 {
				switch a.State {
				case AskApproved:
					fmt.Fprintf(&b, `<div class=card><p>Your request for %s was approved.</p></div>`, html.EscapeString(askWhat(*a)))
				case AskDenied:
					fmt.Fprintf(&b, `<div class=card><p>Your request for %s was declined.</p></div>`, html.EscapeString(askWhat(*a)))
				}
			}
			b.WriteString(`<form class=card method=post action="/ask">`)
			for i, c := range askChoices {
				checked := ""
				if i == 0 {
					checked = " checked"
				}
				fmt.Fprintf(&b, `<label><input type=radio name=what value=%s%s> %s</label>`, c.id, checked, html.EscapeString(c.label))
			}
			fmt.Fprintf(&b, `<textarea name=message maxlength=%d placeholder="Message (optional)"></textarea><button type=submit>Ask the iPhone</button></form>`, askMessageMax)
		}
	}
	title := html.EscapeString(v.Name)
	fmt.Fprintf(w, statusPageHTML, title, b.String())
}

// statusPageHTML follows the viewer's light/dark setting, like the config
// page. Args: title, body. Refreshes itself every 30 seconds.
const statusPageHTML = `<!doctype html><html><head>
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta http-equiv="refresh" content="30">
<title>Open Bars</title><style>
:root{color-scheme:light dark;--bg:#f2f2f7;--card:#fff;--text:#1c1c1e;--muted:#6c6c70;--link:#007aff;--green:#34c759;--red:#ff3b30;--orange:#ff9500;--line:#d1d1d6}
@media (prefers-color-scheme:dark){:root{--bg:#000;--card:#1c1c1e;--text:#f2f2f7;--muted:#98989f;--link:#0a84ff;--green:#30d158;--red:#ff453a;--orange:#ff9f0a;--line:#38383a}}
body{font:16px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:var(--bg);color:var(--text);
max-width:560px;margin:0 auto;padding:24px 16px}
h1{font-size:28px;margin:0 0 16px}
h2{font-size:13px;font-weight:600;text-transform:uppercase;letter-spacing:.04em;color:var(--muted);margin:24px 0 6px 16px}
.card{background:var(--card);border-radius:12px;padding:12px 16px;margin-bottom:10px}
.card p{margin:.3em 0}.state{font-size:20px;font-weight:600}.warn{color:var(--orange)}
.muted{color:var(--muted);font-size:14px}
.rule{padding:6px 0}.bar{height:8px;border-radius:4px;background:var(--line);overflow:hidden;margin:6px 0}
.bar i{display:block;height:100%%;background:var(--green)}.bar i.over{background:var(--red)}
label{display:block;padding:6px 0}textarea{width:100%%;box-sizing:border-box;margin:8px 0;font:inherit;padding:8px;border-radius:8px;border:1px solid var(--line);background:transparent;color:inherit}
button{font:inherit;font-weight:600;background:var(--link);color:#fff;border:0;border-radius:10px;padding:10px 16px;width:100%%}
footer{color:var(--muted);font-size:13px;margin:20px 16px}
</style></head><body>
<h1>%s</h1>
%s
<footer>Open Bars · this page updates itself</footer>
</body></html>`
