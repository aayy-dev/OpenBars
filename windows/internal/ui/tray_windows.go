// Package ui is the tray icon and the Open Bars window (WebView2), running
// as the signed-in user and talking to the service over its named pipe.
package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"fyne.io/systray"
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
	systray.Run(onReady, func() {})
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

func onReady() {
	systray.SetIcon(iconWaiting)
	systray.SetTooltip("Open Bars")
	systray.SetOnTapped(func() { openWindow("status") })

	mStatus := systray.AddMenuItem("Open Bars", "")
	mStatus.Disable()
	mOpen := systray.AddMenuItem("Open Open Bars", "")
	systray.AddSeparator()
	mAuto := systray.AddMenuItemCheckbox("Connect automatically", "Connect whenever this PC is on the iPhone's hotspot", true)
	mConnect := systray.AddMenuItem("Connect now", "")
	mDisconnect := systray.AddMenuItem("Disconnect", "")
	mPair := systray.AddMenuItem("Pair with iPhone…", "")
	systray.AddSeparator()
	mLog := systray.AddMenuItem("Show log", "")
	mDiag := systray.AddMenuItem("Diagnose", "")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit (the connection keeps working)", "")
	mDisconnect.Hide()

	update := func() {
		s, err := status()
		if err != nil {
			systray.SetIcon(iconProblem)
			systray.SetTooltip("Open Bars: the service isn't running")
			mStatus.SetTitle("Open Bars service isn't running")
			return
		}
		systray.SetIcon(iconFor(s.Phase))
		systray.SetTooltip("Open Bars: " + s.Title)
		mStatus.SetTitle(s.Title)
		if s.AutoConnect {
			mAuto.Check()
		} else {
			mAuto.Uncheck()
		}
		if s.Paired {
			mPair.Hide()
			mAuto.Enable()
		} else {
			mPair.Show()
			mAuto.Disable()
		}
		up := s.Tunnel != nil
		if up {
			mConnect.Hide()
			mDisconnect.Show()
		} else {
			mDisconnect.Hide()
			mConnect.Show()
			if s.Paired && s.Phase != core.NoHotspot {
				mConnect.Enable()
			} else {
				mConnect.Disable()
			}
		}
	}
	update()
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				update()
			case <-mOpen.ClickedCh:
				openWindow("status")
			case <-mAuto.ClickedCh:
				call(ipc.OpSetAuto, ipc.Request{On: !mAuto.Checked()}, nil, 5*time.Second)
				update()
			case <-mConnect.ClickedCh:
				call(ipc.OpConnect, ipc.Request{}, nil, 5*time.Second)
				update()
			case <-mDisconnect.ClickedCh:
				call(ipc.OpDisconnect, ipc.Request{}, nil, 5*time.Second)
				update()
			case <-mPair.ClickedCh:
				openWindow("status")
			case <-mLog.ClickedCh:
				openWindow("log")
			case <-mDiag.ClickedCh:
				openWindow("diagnose")
			case <-mQuit.ClickedCh:
				closeWindow()
				systray.Quit()
				return
			}
		}
	}()
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
