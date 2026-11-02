// OpenBars.exe: Open Bars Connect for Windows.
//
//	OpenBars.exe                 tray app (offers to install the service first)
//	OpenBars.exe tray [--show]   tray app, optionally opening its window
//	OpenBars.exe install         install or update (needs administrator)
//	OpenBars.exe uninstall [--keep-data]
//	OpenBars.exe service         run as the Windows service (started by Windows)
//	OpenBars.exe service-debug   run the service in this console (testing)
//	OpenBars.exe status|logs|diagnose   ask the running service (testing)
//	OpenBars.exe version
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/aayy-dev/OpenBars/windows/internal/applog"
	"github.com/aayy-dev/OpenBars/windows/internal/install"
	"github.com/aayy-dev/OpenBars/windows/internal/ipc"
	"github.com/aayy-dev/OpenBars/windows/internal/service"
	"github.com/aayy-dev/OpenBars/windows/internal/ui"
	"github.com/aayy-dev/OpenBars/windows/internal/winutil"
)

// version is set at build time (-ldflags "-X main.version=1.0.0").
var version = "dev"

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "service":
		if err := service.Run(version); err != nil {
			os.Exit(1)
		}
	case "service-debug":
		winutil.AttachParentConsole()
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		fmt.Println("Open Bars service running in the foreground; Ctrl+C to stop. Data:", service.DataDir())
		if err := service.RunDebug(ctx, version); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "install":
		os.Exit(runAdmin("install", func() error { return install.Install(version, os.Stdout) }))
	case "uninstall":
		keep := slices.Contains(args, "--keep-data")
		if !slices.Contains(args, "--yes") && !winutil.AttachParentConsole() {
			if winutil.MessageBox("Open Bars", "Remove Open Bars Connect from this PC? This PC stops connecting through your iPhone.",
				winutil.MBYesNo|winutil.MBIconWarning) != winutil.IDYes {
				return
			}
		}
		stopTrayApps()
		os.Exit(runAdmin("uninstall", func() error { return install.Uninstall(!keep, os.Stdout) }, "--yes"))
	case "status", "logs", "diagnose":
		// For testing from a terminal: ask the running service.
		winutil.AttachParentConsole()
		os.Exit(query(cmd))
	case "version", "--version":
		winutil.AttachParentConsole()
		fmt.Println("Open Bars", version)
	case "", "tray":
		page := ""
		if slices.Contains(args, "--show") {
			page = "status"
		}
		if err := ui.Run(version, page); err != nil {
			winutil.MessageBox("Open Bars", err.Error(), winutil.MBOK|winutil.MBIconError)
		}
	default:
		winutil.AttachParentConsole()
		fmt.Fprintln(os.Stderr, "usage: OpenBars.exe [tray|install|uninstall [--keep-data]|version]")
		os.Exit(2)
	}
}

// runAdmin runs f as administrator: directly when already elevated,
// otherwise by relaunching itself through the UAC prompt.
func runAdmin(name string, f func() error, extra ...string) int {
	console := winutil.AttachParentConsole()
	if !winutil.IsElevated() {
		args := append([]string{name}, extra...)
		for _, a := range os.Args[2:] {
			if !slices.Contains(args, a) {
				args = append(args, a)
			}
		}
		code, err := winutil.RunElevated(args...)
		if err != nil {
			report(console, "Couldn't get administrator rights: "+err.Error())
			return 1
		}
		return int(code)
	}
	if err := f(); err != nil {
		report(console, fmt.Sprintf("Couldn't %s Open Bars: %v", name, err))
		return 1
	}
	if name == "install" && console {
		fmt.Println("Open Bars is installed. Start it from the Start menu (“Open Bars”).")
	}
	return 0
}

func report(console bool, msg string) {
	if console {
		fmt.Fprintln(os.Stderr, msg)
		return
	}
	winutil.MessageBox("Open Bars", msg, winutil.MBOK|winutil.MBIconError)
}

// stopTrayApps closes other Open Bars processes (the tray apps) so their
// files can be removed.
func stopTrayApps() {
	cmd := exec.Command("taskkill.exe", "/F", "/IM", "OpenBars.exe", "/FI", "PID ne "+strconv.Itoa(os.Getpid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Run()
}

// query prints the service's status, log or Diagnose results.
func query(op string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	switch op {
	case "logs":
		var p applog.Page
		if err := ipc.Call(ctx, ipc.Dial, ipc.Request{Op: ipc.OpLogs}, &p); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, e := range p.Entries {
			fmt.Println(applog.Line(e, time.Local))
		}
		return 0
	case "diagnose":
		op = ipc.OpDiagnose
	}
	var out json.RawMessage
	if err := ipc.Call(ctx, ipc.Dial, ipc.Request{Op: op}, &out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var pretty bytes.Buffer
	json.Indent(&pretty, out, "", "  ")
	fmt.Println(pretty.String())
	return 0
}
