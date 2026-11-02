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
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

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
				Title: "Open Bars", Width: 780, Height: 680, IconId: 1, Center: true,
			},
		})
		if w == nil {
			winutil.MessageBox("Open Bars", "Open Bars needs the Microsoft Edge WebView2 Runtime, which comes with Windows 11. Install it from microsoft.com/edge/webview2 and try again.", winutil.MBOK|winutil.MBIconError)
			return
		}
		bindAll(w)
		hwnd := windows.HWND(uintptr(w.Window()))
		matchTheme(hwnd)
		stopTheme := make(chan struct{})
		go func() {
			// Follow light/dark switches while the window is open.
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stopTheme:
					return
				case <-t.C:
					w.Dispatch(func() { matchTheme(hwnd) })
				}
			}
		}()
		defer close(stopTheme)
		w.Init(`document.addEventListener("contextmenu", e => {
			if (!(e.target.closest("input, .log") || String(getSelection()))) e.preventDefault();
		});`)
		w.SetSize(640, 480, webview2.HintMin)
		z := &sizer{hwnd: hwnd}
		if p := loadPrefs(); p.Width > 0 && p.Height > 0 {
			z.userSized = true
			z.resize(p.Width, p.Height)
		} else {
			z.fit(page)
		}
		w.Bind("obFit", func(page string) { w.Dispatch(func() { z.fit(page) }) })
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

var (
	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	procSetWindowPos          = user32.NewProc("SetWindowPos")
	lastDark                  = map[windows.HWND]bool{}
)

// matchTheme gives the window a dark title bar when Windows' apps use dark mode.
func matchTheme(h windows.HWND) {
	dark := systemUsesDarkMode()
	if prev, ok := lastDark[h]; ok && prev == dark {
		return
	}
	lastDark[h] = dark
	const dwmwaUseImmersiveDarkMode = 20
	var v int32
	if dark {
		v = 1
	}
	procDwmSetWindowAttribute.Call(uintptr(h), dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&v)), unsafe.Sizeof(v))
	// Redraw the frame so the change shows right away.
	const swpFlags = 0x0001 | 0x0002 | 0x0004 | 0x0010 | 0x0020 // NOSIZE|NOMOVE|NOZORDER|NOACTIVATE|FRAMECHANGED
	procSetWindowPos.Call(uintptr(h), 0, 0, 0, 0, 0, swpFlags)
}

func systemUsesDarkMode() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

func showWindow(h windows.HWND) {
	const swRestore = 9
	procShowWindow.Call(uintptr(h), swRestore)
	procSetForegroundWindow.Call(uintptr(h))
}

// prefs are the window's own settings (per user).
type prefs struct {
	UTC    bool `json:"utc"`
	Width  int  `json:"width,omitempty"` // a size the person chose (DIPs); 0 = fit each page
	Height int  `json:"height,omitempty"`
}

// pageSizes fit each page's content without scrolling (DIPs, outer size).
var pageSizes = map[string][2]int{
	"status":   {780, 680},
	"log":      {1200, 760}, // full log lines
	"diagnose": {880, 820},  // all seven checks with hints
}

// sizer resizes the window to suit each page until the person resizes it
// themselves; from then on their size is kept (and remembered).
type sizer struct {
	hwnd         windows.HWND
	lastW, lastH int32 // the size we last set (pixels)
	userSized    bool
}

var (
	procGetWindowRect     = user32.NewProc("GetWindowRect")
	procIsZoomed          = user32.NewProc("IsZoomed")
	procGetDpiForWindow   = user32.NewProc("GetDpiForWindow")
	procMonitorFromWindow = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW   = user32.NewProc("GetMonitorInfoW")
)

type winRect struct{ Left, Top, Right, Bottom int32 }

func (z *sizer) rect() winRect {
	var r winRect
	procGetWindowRect.Call(uintptr(z.hwnd), uintptr(unsafe.Pointer(&r)))
	return r
}

func (z *sizer) scale(v int) int32 {
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(z.hwnd))
	if dpi == 0 {
		dpi = 96
	}
	return int32(v * int(dpi) / 96)
}

// fit sizes the window for page unless the person has sized it.
func (z *sizer) fit(page string) {
	if z.userSized {
		return
	}
	r := z.rect()
	w, h := r.Right-r.Left, r.Bottom-r.Top
	zoomed, _, _ := procIsZoomed.Call(uintptr(z.hwnd))
	if zoomed != 0 || (z.lastW != 0 && (abs32(w-z.lastW) > 4 || abs32(h-z.lastH) > 4)) {
		// They resized or maximized it: keep their size from now on.
		z.userSized = true
		if zoomed == 0 {
			dpi, _, _ := procGetDpiForWindow.Call(uintptr(z.hwnd))
			if dpi == 0 {
				dpi = 96
			}
			p := loadPrefs()
			p.Width, p.Height = int(w)*96/int(dpi), int(h)*96/int(dpi)
			savePrefs(p)
		}
		return
	}
	size, ok := pageSizes[page]
	if !ok {
		size = pageSizes["status"]
	}
	z.resize(size[0], size[1])
}

// resize sets the outer size (DIPs), keeping the window's center where it
// is and the whole window on its screen.
func (z *sizer) resize(dw, dh int) {
	r := z.rect()
	w, h := z.scale(dw), z.scale(dh)
	cx, cy := (r.Left+r.Right)/2, (r.Top+r.Bottom)/2
	x, y := cx-w/2, cy-h/2
	if work, ok := z.workArea(); ok {
		w, h = min(w, work.Right-work.Left), min(h, work.Bottom-work.Top)
		x = max(work.Left, min(x, work.Right-w))
		y = max(work.Top, min(y, work.Bottom-h))
	}
	const swpNoZOrder, swpNoActivate = 0x0004, 0x0010
	procSetWindowPos.Call(uintptr(z.hwnd), 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), swpNoZOrder|swpNoActivate)
	got := z.rect()
	z.lastW, z.lastH = got.Right-got.Left, got.Bottom-got.Top
}

func (z *sizer) workArea() (winRect, bool) {
	const monitorDefaultToNearest = 2
	mon, _, _ := procMonitorFromWindow.Call(uintptr(z.hwnd), monitorDefaultToNearest)
	if mon == 0 {
		return winRect{}, false
	}
	var mi struct {
		CbSize  uint32
		Monitor winRect
		Work    winRect
		Flags   uint32
	}
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return winRect{}, false
	}
	return mi.Work, true
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
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
