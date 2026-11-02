package applog

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
)

// File persists a ring's entries as JSON Lines so the log survives restarts.
// It keeps at most max entries; the file is rewritten when it has grown to
// twice that.
type File struct {
	mu    sync.Mutex
	path  string
	max   int
	lines int
	f     *os.File
}

// OpenFile loads saved entries (oldest first, at most max) and opens the file
// for appending.
func OpenFile(path string, max int) (*File, []Entry, error) {
	saved, err := readEntries(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	if len(saved) > max {
		saved = saved[len(saved)-max:]
	}
	lf := &File{path: path, max: max}
	if err := lf.rewrite(saved); err != nil {
		return nil, nil, err
	}
	return lf, saved, nil
}

// Append saves one entry. Errors are ignored: losing a log line must never
// break the app.
func (lf *File) Append(e Entry) {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	if lf.f == nil {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	lf.f.Write(append(b, '\n'))
	lf.lines++
	if lf.lines >= 2*lf.max {
		if saved, err := readEntries(lf.path); err == nil {
			if len(saved) > lf.max {
				saved = saved[len(saved)-lf.max:]
			}
			lf.rewriteLocked(saved)
		}
	}
}

// Clear empties the file.
func (lf *File) Clear() {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	lf.rewriteLocked(nil)
}

// Close closes the file.
func (lf *File) Close() error {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	if lf.f == nil {
		return nil
	}
	err := lf.f.Close()
	lf.f = nil
	return err
}

func (lf *File) rewrite(entries []Entry) error {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	return lf.rewriteLocked(entries)
}

func (lf *File) rewriteLocked(entries []Entry) error {
	if lf.f != nil {
		lf.f.Close()
		lf.f = nil
	}
	tmp := lf.path + ".tmp"
	w, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	for _, e := range entries {
		b, _ := json.Marshal(e)
		bw.Write(append(b, '\n'))
	}
	if err := bw.Flush(); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, lf.path); err != nil {
		return err
	}
	f, err := os.OpenFile(lf.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	lf.f = f
	lf.lines = len(entries)
	return nil
}

func readEntries(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Seq > 0 {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}
