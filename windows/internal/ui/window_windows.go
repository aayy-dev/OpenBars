package ui

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"github.com/aayy-dev/OpenBars/windows/internal/applog"
	"github.com/aayy-dev/OpenBars/windows/internal/core"
	"github.com/aayy-dev/OpenBars/windows/internal/ipc"
	"github.com/aayy-dev/OpenBars/windows/internal/sysinfo"
	"github.com/aayy-dev/OpenBars/windows/internal/winutil"
)

//go:embed web/index.html
var indexHTML string

var (
	winMu sync.Mutex
	win   webview2.WebView
)

// openWindow shows the Open Bars window on a page (status, log, diagnose),
// creating it if needed. Each window runs its own message loop on a
// dedicated OS thread.
func openWindow(page string) {
	winMu.Lock()
	if win != nil {
		w := win
		winMu.Unlock()
		w.Dispatch(func() {
			w.Eval(fmt.Sprintf("showPage(%q)", page))
			hwnd := windows.HWND(uintptr(w.Window()))
			showWindow(hwnd)
		})
		return
	}
	winMu.Unlock()
	go func() {
		runtime.LockOSThread()
		dataDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Open Bars", "WebView2")
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:  dataDir,
			AutoFocus: true,
			WindowOptions: webview2.WindowOptions{
				Title: "Open Bars", Width: 780, Height: 600, IconId: 1, Center: true,
			},
		})
		if w == nil {
			winutil.MessageBox("Open Bars", "Open Bars needs the Microsoft Edge WebView2 Runtime, which comes with Windows 11. Install it from microsoft.com/edge/webview2 and try again.", winutil.MBOK|winutil.MBIconError)
			return
		}
		bindAll(w)
		w.SetSize(780, 600, webview2.HintMin)
		w.SetHtml(strings.Replace(indexHTML, "__START_PAGE__", page, 1))
		winMu.Lock()
		win = w
		winMu.Unlock()
		w.Run()
		winMu.Lock()
		win = nil
		winMu.Unlock()
		w.Destroy()
	}()
}

func closeWindow() {
	winMu.Lock()
	w := win
	winMu.Unlock()
	if w != nil {
		w.Dispatch(func() { w.Terminate() })
	}
}

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

func showWindow(h windows.HWND) {
	const swRestore = 9
	procShowWindow.Call(uintptr(h), swRestore)
	procSetForegroundWindow.Call(uintptr(h))
}

// prefs are the window's own settings (per user).
type prefs struct {
	UTC bool `json:"utc"`
}

func prefsPath() string {
	return filepath.Join(os.Getenv("APPDATA"), "Open Bars", "ui.json")
}

func loadPrefs() prefs {
	var p prefs
	if b, err := os.ReadFile(prefsPath()); err == nil {
		json.Unmarshal(b, &p)
	}
	return p
}

func savePrefs(p prefs) {
	os.MkdirAll(filepath.Dir(prefsPath()), 0o755)
	b, _ := json.Marshal(p)
	os.WriteFile(prefsPath(), b, 0o644)
}

func location() *time.Location {
	if loadPrefs().UTC {
		return time.UTC
	}
	return time.Local
}

// uiEntry is a log entry ready to show.
type uiEntry struct {
	Seq   int64  `json:"seq"`
	Stamp string `json:"ts"`
	Level string `json:"lvl"`
	Src   string `json:"src"`
	Msg   string `json:"msg"`
	Hint  string `json:"hint,omitempty"`
}

type uiPage struct {
	Next    int64     `json:"next"`
	First   int64     `json:"first"`
	Entries []uiEntry `json:"entries"`
}

// failure turns an error into something the page can show with its hint.
func failure(err error) error {
	var se *ipc.ServiceError
	if errors.As(err, &se) && se.Hint != "" {
		return fmt.Errorf("%s\n%s", se.Msg, se.Hint)
	}
	if errors.Is(err, ipc.ErrNoService) {
		return errors.New("The Open Bars service isn't running.\nOpen Services and start “Open Bars Connect”, or reinstall Open Bars.")
	}
	return err
}

func bindAll(w webview2.WebView) {
	w.Bind("obStatus", func() (core.Status, error) {
		s, err := status()
		return s, failure(err)
	})
	w.Bind("obPair", func() (string, error) {
		var out struct{ Message string }
		err := call(ipc.OpPair, ipc.Request{}, &out, 110*time.Second)
		return out.Message, failure(err)
	})
	w.Bind("obSetAuto", func(on bool) error { return failure(call(ipc.OpSetAuto, ipc.Request{On: on}, nil, 5*time.Second)) })
	w.Bind("obConnect", func() error { return failure(call(ipc.OpConnect, ipc.Request{}, nil, 5*time.Second)) })
	w.Bind("obDisconnect", func() error { return failure(call(ipc.OpDisconnect, ipc.Request{}, nil, 5*time.Second)) })
	w.Bind("obForget", func() error { return failure(call(ipc.OpForget, ipc.Request{}, nil, 5*time.Second)) })
	w.Bind("obDiagnose", func() ([]core.Check, error) {
		var out []core.Check
		err := call(ipc.OpDiagnose, ipc.Request{}, &out, 60*time.Second)
		return out, failure(err)
	})
	w.Bind("obLogs", func(after int64) (uiPage, error) {
		var p applog.Page
		if err := call(ipc.OpLogs, ipc.Request{After: after}, &p, 5*time.Second); err != nil {
			return uiPage{}, failure(err)
		}
		loc := location()
		out := uiPage{Next: p.Next, First: p.First, Entries: []uiEntry{}}
		for _, e := range p.Entries {
			out.Entries = append(out.Entries, uiEntry{Seq: e.Seq, Stamp: applog.Stamp(e, loc), Level: string(e.Level),
				Src: e.Src, Msg: applog.Message(e), Hint: e.Hint})
		}
		return out, nil
	})
	w.Bind("obClearLogs", func() error { return failure(call(ipc.OpClearLogs, ipc.Request{}, nil, 5*time.Second)) })
	w.Bind("obSetDetailed", func(on bool) error {
		return failure(call(ipc.OpSetDetailed, ipc.Request{On: on}, nil, 5*time.Second))
	})
	w.Bind("obPrefs", func() prefs { return loadPrefs() })
	w.Bind("obSetUTC", func(on bool) { p := loadPrefs(); p.UTC = on; savePrefs(p) })
	w.Bind("obExport", func() (string, error) {
		text, err := exportText()
		if err != nil {
			return "", failure(err)
		}
		dir, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0)
		if err != nil {
			dir = os.Getenv("USERPROFILE")
		}
		name := "Open Bars Connect log " + time.Now().Format("2006-01-02 15-04") + ".txt"
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(text, "\n", "\r\n")), 0o644); err != nil {
			return "", err
		}
		winutil.ShowInExplorer(path)
		return path, nil
	})
	w.Bind("obCopy", func() error {
		text, err := exportText()
		if err != nil {
			return failure(err)
		}
		return winutil.CopyText(text)
	})
	w.Bind("obAbout", func() map[string]string {
		return map[string]string{"version": Version, "os": sysinfo.OS(), "computer": sysinfo.ComputerName()}
	})
	w.Bind("obOpenURL", func(url string) {
		if strings.HasPrefix(url, "https://") {
			cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			cmd.Start()
		}
	})
}

// exportText is the whole log with a header, in the chosen time zone.
func exportText() (string, error) {
	var p applog.Page
	if err := call(ipc.OpLogs, ipc.Request{After: 0}, &p, 5*time.Second); err != nil {
		return "", err
	}
	device := sysinfo.Model()
	if device == "" {
		device = sysinfo.ComputerName()
	} else {
		device += " (" + sysinfo.ComputerName() + ")"
	}
	return applog.Export(applog.Header{
		App: "Open Bars " + Version, OS: sysinfo.OS(), Device: device, Which: "Connect (Windows)",
		Location: location(), Now: time.Now(),
	}, p.Entries), nil
}

// startDetached launches exe with args and doesn't wait.
func startDetached(exe string, args ...string) {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	cmd.Start()
}
