// Package ui is the tray icon and the Open Bars window (WebView2), running
// as the signed-in user and talking to the service over its named pipe.
package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/aayy-dev/OpenBars/windows/internal/core"
	"github.com/aayy-dev/OpenBars/windows/internal/install"
	"github.com/aayy-dev/OpenBars/windows/internal/ipc"
	"github.com/aayy-dev/OpenBars/windows/internal/winutil"
)

const (
	instanceMutex = `Local\OpenBarsTray`
	showEvent     = `Local\OpenBarsTrayShow`
)

// Version is shown in the window and exported logs.
var Version = "dev"

// Run starts the tray app, or brings the running one's window up.
func Run(version, page string) error {
	Version = version
	// A different version run from outside Program Files offers to update
	// the installed copy (which also replaces a running tray app).
	if exe, err := os.Executable(); err == nil && install.Installed() && !strings.EqualFold(exe, install.Exe()) {
		if installed := install.InstalledVersion(); installed != version {
			if err := install.CheckFiles(); err != nil {
				return err
			}
			return offerUpdate(installed, version)
		}
	}
	name, _ := windows.UTF16PtrFromString(instanceMutex)
	mu, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		signalShow()
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(mu)

	if !install.Installed() {
		if err := install.CheckFiles(); err != nil {
			return err // shown in a message box by main
		}
		if !offerInstall() {
			return nil
		}
	}
	if exe, err := os.Executable(); err == nil && exe == install.Exe() {
		install.SetAutostart(exe)
	}
	go waitForShow()
	if page != "" {
		go openWindow(page)
	}
	runTray()
	return nil
}

// offerInstall asks to install the service (UAC) when it isn't yet.
func offerInstall() bool {
	if winutil.MessageBox("Open Bars", "Open Bars needs to install a small background service to connect this PC through your iPhone. Install it now?",
		winutil.MBYesNo|winutil.MBIconInfo) != winutil.IDYes {
		return false
	}
	code, err := winutil.RunElevated("install")
	switch {
	case errors.Is(err, winutil.ErrCancelled):
		return false
	case err != nil:
		winutil.MessageBox("Open Bars", "Couldn't install: "+err.Error(), winutil.MBOK|winutil.MBIconError)
		return false
	case code != 0:
		winutil.MessageBox("Open Bars", "Installing didn't finish (code "+fmt.Sprint(code)+"). Run “OpenBars.exe install” as administrator to see why.", winutil.MBOK|winutil.MBIconError)
		return false
	}
	// Continue from the installed copy so autostart points at it.
	if exe, _ := os.Executable(); exe != install.Exe() {
		startDetached(install.Exe(), "tray", "--show")
		os.Exit(0)
	}
	return true
}

// offerUpdate installs this version over the installed one, then hands
// over to the newly installed tray app.
func offerUpdate(installed, version string) error {
	from := installed
	if from == "" {
		from = "the installed version"
	}
	if winutil.MessageBox("Open Bars", fmt.Sprintf("Update Open Bars from %s to %s?", from, version),
		winutil.MBYesNo|winutil.MBIconInfo) != winutil.IDYes {
		return nil
	}
	code, err := winutil.RunElevated("install")
	switch {
	case errors.Is(err, winutil.ErrCancelled):
		return nil
	case err != nil:
		return fmt.Errorf("couldn't update: %w", err)
	case code != 0:
		return fmt.Errorf("updating didn't finish (code %d); run “OpenBars.exe install” as administrator to see why", code)
	}
	startDetached(install.Exe(), "tray", "--show")
	return nil
}

func signalShow() {
	name, _ := windows.UTF16PtrFromString(showEvent)
	if h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name); err == nil {
		windows.SetEvent(h)
		windows.CloseHandle(h)
	}
}

func waitForShow() {
	name, _ := windows.UTF16PtrFromString(showEvent)
	h, err := windows.CreateEvent(nil, 0, 0, name)
	if err != nil {
		return
	}
	for {
		if ev, err := windows.WaitForSingleObject(h, windows.INFINITE); err != nil || ev != windows.WAIT_OBJECT_0 {
			return
		}
		openWindow("status")
	}
}

func call(op string, req ipc.Request, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req.Op = op
	return ipc.Call(ctx, ipc.Dial, req, out)
}

func status() (core.Status, error) {
	var s core.Status
	err := call(ipc.OpStatus, ipc.Request{}, &s, 3*time.Second)
	return s, err
}

// Menu item IDs.
const (
	miStatus = iota + 1
	miOpen
	miAuto
	miConnect
	miDisconnect
	miPair
	miLog
	miDiagnose
	miQuit
)

var (
	trayMu     sync.Mutex
	trayStatus core.Status
	trayErr    error
)

// runTray shows the notification-area icon until Quit is chosen.
func runTray() {
	t := &TrayIcon{OnClick: func() { openWindow("status") }}
	t.Menu = trayMenu
	t.OnMenu = func(id uint32) { trayCommand(t, id) }
	t.SetIcon(iconWaiting, "Open Bars")
	go func() {
		for {
			refreshTray(t)
			time.Sleep(2 * time.Second)
		}
	}()
	t.Run()
}

func refreshTray(t *TrayIcon) {
	s, err := status()
	trayMu.Lock()
	trayStatus, trayErr = s, err
	trayMu.Unlock()
	if err != nil {
		t.SetIcon(iconProblem, "Open Bars: the service isn't running")
		return
	}
	t.SetIcon(iconFor(s.Phase), "Open Bars: "+s.Title)
}

func trayMenu() []MenuItem {
	trayMu.Lock()
	s, err := trayStatus, trayErr
	trayMu.Unlock()
	title := s.Title
	if err != nil {
		title = "Open Bars service isn't running"
	}
	items := []MenuItem{
		{ID: miStatus, Title: title, Disabled: true},
		{ID: miOpen, Title: "Open Open Bars"},
		{},
	}
	if s.Paired {
		items = append(items, MenuItem{ID: miAuto, Title: "Connect automatically", Checked: s.AutoConnect})
		if s.Tunnel != nil {
			items = append(items, MenuItem{ID: miDisconnect, Title: "Disconnect"})
		} else {
			items = append(items, MenuItem{ID: miConnect, Title: "Connect now", Disabled: s.Phase == core.NoHotspot})
		}
	} else {
		items = append(items, MenuItem{ID: miPair, Title: "Pair with iPhone…", Disabled: err != nil})
	}
	return append(items, MenuItem{},
		MenuItem{ID: miLog, Title: "Show log"},
		MenuItem{ID: miDiagnose, Title: "Diagnose"},
		MenuItem{},
		MenuItem{ID: miQuit, Title: "Quit (the connection keeps working)"},
	)
}

func trayCommand(t *TrayIcon, id uint32) {
	switch id {
	case miOpen, miPair:
		openWindow("status")
	case miAuto:
		trayMu.Lock()
		on := !trayStatus.AutoConnect
		trayMu.Unlock()
		call(ipc.OpSetAuto, ipc.Request{On: on}, nil, 5*time.Second)
	case miConnect:
		call(ipc.OpConnect, ipc.Request{}, nil, 5*time.Second)
	case miDisconnect:
		call(ipc.OpDisconnect, ipc.Request{}, nil, 5*time.Second)
	case miLog:
		openWindow("log")
	case miDiagnose:
		openWindow("diagnose")
	case miQuit:
		closeWindow()
		t.Quit()
		return
	}
	refreshTray(t)
}

func iconFor(p core.Phase) []byte {
	switch p {
	case core.Connected:
		return iconConnected
	case core.Connecting, core.Checking:
		return iconConnecting
	case core.NotSharing, core.NotThisPhone, core.Problem:
		return iconProblem
	}
	return iconWaiting
}
