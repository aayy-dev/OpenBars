// Package service is the Open Bars Connect Windows service: it owns the
// pairing and the tunnel, and answers the tray app over a named pipe.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"github.com/aayy-dev/OpenBars/windows/internal/applog"
	"github.com/aayy-dev/OpenBars/windows/internal/core"
	"github.com/aayy-dev/OpenBars/windows/internal/hotspot"
	"github.com/aayy-dev/OpenBars/windows/internal/ipc"
	"github.com/aayy-dev/OpenBars/windows/internal/phone"
	"github.com/aayy-dev/OpenBars/windows/internal/probe"
	"github.com/aayy-dev/OpenBars/windows/internal/state"
	"github.com/aayy-dev/OpenBars/windows/internal/sysinfo"
	"github.com/aayy-dev/OpenBars/windows/internal/tunnel"
)

// Name is the Windows service name.
const Name = "OpenBarsConnect"

const logCapacity = 3000

// DataDir is where the service keeps its state and log.
func DataDir() string {
	if d := os.Getenv("OPENBARS_DATA"); d != "" {
		return d // for testing
	}
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "Open Bars")
}

// App is a running service instance.
type App struct {
	ctrl    *core.Controller
	logFile *applog.File
	done    chan struct{}
	unreg   []func()
}

// Start sets everything up and runs until ctx ends.
func Start(ctx context.Context, version string) (*App, error) {
	dir := DataDir()
	if err := makePrivateDir(dir); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	ring := applog.NewRing(logCapacity)
	lf, saved, err := applog.OpenFile(filepath.Join(dir, "connect-log.jsonl"), logCapacity)
	if err == nil {
		ring.Restore(saved)
		ring.OnAdd(lf.Append)
	} else {
		ring.Add(applog.Warn, "svc", "Couldn't open the saved log: "+err.Error(), "")
	}

	ctrl := core.New(core.Config{
		StatePath: filepath.Join(dir, "state.json"),
		Sealer:    state.DPAPI{},
		Log:       ring,
		ClearLog: func() {
			if lf != nil {
				lf.Clear()
			}
		},
		Phone:      phone.New(),
		Net:        netSnapshot{},
		Tunnel:     tunnel.New(ring),
		Probe:      probe.New(),
		DeviceName: sysinfo.ComputerName(),
		About:      fmt.Sprintf("Open Bars %s on %s", version, sysinfo.OS()),
	})
	app := &App{ctrl: ctrl, logFile: lf, done: make(chan struct{})}
	app.watchNetwork()

	l, err := ipc.Listen()
	if err != nil {
		return nil, fmt.Errorf("open the pipe for the tray app: %w", err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ctrl.Run(ctx) }()
	go func() { defer wg.Done(); ipc.Serve(ctx, l, app.handle) }()
	go func() {
		wg.Wait()
		for _, u := range app.unreg {
			u()
		}
		if lf != nil {
			lf.Close()
		}
		close(app.done)
	}()
	return app, nil
}

// Done is closed once the service has fully stopped (tunnel down).
func (a *App) Done() <-chan struct{} { return a.done }

// Poke re-checks the network now (e.g. after waking from sleep).
func (a *App) Poke() { a.ctrl.Poke() }

// watchNetwork pokes the controller whenever adapters, addresses or routes change.
func (a *App) watchNetwork() {
	if cb, err := winipcfg.RegisterInterfaceChangeCallback(func(winipcfg.MibNotificationType, *winipcfg.MibIPInterfaceRow) { a.ctrl.Poke() }); err == nil {
		a.unreg = append(a.unreg, func() { cb.Unregister() })
	}
	if cb, err := winipcfg.RegisterUnicastAddressChangeCallback(func(winipcfg.MibNotificationType, *winipcfg.MibUnicastIPAddressRow) { a.ctrl.Poke() }); err == nil {
		a.unreg = append(a.unreg, func() { cb.Unregister() })
	}
	if cb, err := winipcfg.RegisterRouteChangeCallback(func(winipcfg.MibNotificationType, *winipcfg.MibIPforwardRow2) { a.ctrl.Poke() }); err == nil {
		a.unreg = append(a.unreg, func() { cb.Unregister() })
	}
}

// handle answers the tray app.
func (a *App) handle(ctx context.Context, req ipc.Request) (any, string, error) {
	c := a.ctrl
	switch req.Op {
	case ipc.OpStatus:
		return c.Status(), "", nil
	case ipc.OpPair:
		msg, hint, err := c.Pair(ctx)
		return map[string]string{"message": msg}, hint, err
	case ipc.OpSetAuto:
		return nil, "", c.SetAutoConnect(req.On)
	case ipc.OpConnect:
		c.ConnectNow()
		return nil, "", nil
	case ipc.OpDisconnect:
		c.Disconnect()
		return nil, "", nil
	case ipc.OpForget:
		return nil, "", c.Forget()
	case ipc.OpDiagnose:
		return c.Diagnose(ctx), "", nil
	case ipc.OpLogs:
		return c.Logs(req.After), "", nil
	case ipc.OpClearLogs:
		c.ClearLogs()
		return nil, "", nil
	case ipc.OpSetDetailed:
		c.SetDetailed(req.On)
		return nil, "", nil
	}
	return nil, "", errors.New("unknown request " + req.Op)
}

type netSnapshot struct{}

func (netSnapshot) Snapshot() ([]hotspot.Iface, error) { return hotspot.Snapshot() }

// makePrivateDir creates dir readable only by LocalSystem and Administrators.
func makePrivateDir(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;GA;;;SY)(A;OICI;GA;;;BA)")
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(p, sa); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return err
	}
	return nil
}

// Run runs as a Windows service (called when the SCM starts OpenBars.exe service).
func Run(version string) error {
	return svc.Run(Name, &handler{version: version})
}

type handler struct{ version string }

func (h *handler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app, err := Start(ctx, h.version)
	if err != nil {
		writeEventLog("Open Bars couldn't start: " + err.Error())
		return false, 1
	}
	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPowerEvent}
	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			s <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			s <- svc.Status{State: svc.StopPending}
			cancel()
			select {
			case <-app.Done():
			case <-time.After(15 * time.Second):
			}
			return false, 0
		case svc.PowerEvent:
			// PBT_APMRESUMEAUTOMATIC / PBT_APMRESUMESUSPEND: look again after sleep.
			if c.EventType == 0x12 || c.EventType == 0x7 {
				app.Poke()
			}
		}
	}
	return false, 0
}

// RunDebug runs the service in the foreground until ctx ends (for testing).
func RunDebug(ctx context.Context, version string) error {
	app, err := Start(ctx, version)
	if err != nil {
		return err
	}
	<-app.Done()
	return nil
}

func writeEventLog(msg string) {
	fmt.Fprintln(os.Stderr, msg)
}
