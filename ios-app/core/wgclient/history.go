package wgclient

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/device"
)

// Speed history for the Connect tab's chart: one sample per second for the
// last 5 minutes, kept while the tunnel runs (same shape as the iPhone's
// engine). down = from the iPhone to this device (rx), up = tx.

const historySeconds = 300

type sample struct {
	T    int64 `json:"t"`
	Down int64 `json:"down"`
	Up   int64 `json:"up"`
}

type history struct {
	mu      sync.Mutex
	samples []sample
	stop    chan struct{}
}

var hist = &history{}

// start samples read() (cumulative rx, tx) every second until stop is called.
func (h *history) start(read func() (rx, tx int64)) {
	h.mu.Lock()
	if h.stop != nil {
		close(h.stop)
	}
	h.samples = nil
	stop := make(chan struct{})
	h.stop = stop
	h.mu.Unlock()
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		var prevRx, prevTx int64 // the tunnel starts from zero
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				rx, tx := read()
				h.record(now.Unix(), rx, tx, prevRx, prevTx)
				prevRx, prevTx = rx, tx
			}
		}
	}()
}

func (h *history) record(t, rx, tx, prevRx, prevTx int64) {
	d, u := rx-prevRx, tx-prevTx
	if d < 0 || u < 0 { // counters restarted
		d, u = rx, tx
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.samples = append(h.samples, sample{T: t, Down: d, Up: u})
	if len(h.samples) > historySeconds {
		h.samples = append(h.samples[:0], h.samples[len(h.samples)-historySeconds:]...)
	}
}

func (h *history) halt() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stop != nil {
		close(h.stop)
		h.stop = nil
	}
	h.samples = nil
}

func (h *history) since(t int64) []sample {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := sort.Search(len(h.samples), func(i int) bool { return h.samples[i].T > t })
	return append([]sample{}, h.samples[i:]...)
}

// HistoryJSON returns per-second samples newer than since (unix seconds) as
// a JSON array, oldest first ("[]" when the tunnel is down).
func HistoryJSON(since int64) string {
	b, _ := json.Marshal(hist.since(since))
	return string(b)
}

// peerState reads the iPhone peer's endpoint, last handshake and counters.
func peerState(d *device.Device) (endpoint string, handshake, rx, tx int64) {
	ipc, err := d.IpcGet()
	if err != nil {
		return
	}
	for _, line := range strings.Split(ipc, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "endpoint":
			endpoint = v
		case "last_handshake_time_sec":
			handshake, _ = strconv.ParseInt(v, 10, 64)
		case "rx_bytes":
			rx, _ = strconv.ParseInt(v, 10, 64)
		case "tx_bytes":
			tx, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return
}
