// Package install puts Open Bars in Program Files, registers the service,
// the Start menu shortcut and the Apps entry, and takes them all away again.
package install

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/aayy-dev/OpenBars/windows/internal/service"
)

const (
	displayName  = "Open Bars Connect"
	description  = "Sends this PC's traffic through Open Bars on your iPhone while the PC is on that iPhone's Personal Hotspot."
	uninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\OpenBarsConnect`
	runKey       = `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`
	runValue     = "Open Bars"
	exeName      = "OpenBars.exe"
)

// files that ship next to the exe and get installed with it.
var payload = []string{"wintun.dll", "LICENSE.txt", "THIRD_PARTY_NOTICES.txt", "wintun-LICENSE.txt"}

// Dir is where Open Bars is installed.
func Dir() string {
	base := os.Getenv("ProgramFiles")
	if base == "" {
		base = `C:\Program Files`
	}
	return filepath.Join(base, "Open Bars")
}

// Exe is the installed executable.
func Exe() string { return filepath.Join(Dir(), exeName) }

// Installed reports whether the service is registered (works without admin).
func Installed() bool {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(m)
	name, _ := windows.UTF16PtrFromString(service.Name)
	s, err := windows.OpenService(m, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	windows.CloseServiceHandle(s)
	return true
}

// Install copies the files, then creates (or updates) and starts the
// service. Needs administrator rights.
func Install(version string, out io.Writer) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	srcDir := filepath.Dir(src)
	if _, err := os.Stat(filepath.Join(srcDir, "wintun.dll")); err != nil {
		return errors.New("wintun.dll must be in the same folder as OpenBars.exe")
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("can't manage services (run as administrator): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(service.Name)
	exists := err == nil
	if exists {
		fmt.Fprintln(out, "Stopping the running Open Bars service…")
		stop(s)
	}

	dir := Dir()
	if !strings.EqualFold(filepath.Clean(srcDir), filepath.Clean(dir)) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := copyFile(src, filepath.Join(dir, exeName)); err != nil {
			return fmt.Errorf("copy OpenBars.exe: %w", err)
		}
		for _, f := range payload {
			if err := copyFile(filepath.Join(srcDir, f), filepath.Join(dir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("copy %s: %w", f, err)
			}
		}
		fmt.Fprintln(out, "Copied Open Bars to", dir)
	}

	exe := Exe()
	if exists {
		cfg, err := s.Config()
		if err != nil {
			return err
		}
		cfg.BinaryPathName = syscall.EscapeArg(exe) + " service"
		cfg.DisplayName, cfg.Description, cfg.StartType = displayName, description, mgr.StartAutomatic
		if err := s.UpdateConfig(cfg); err != nil {
			return fmt.Errorf("update the service: %w", err)
		}
	} else {
		s, err = m.CreateService(service.Name, exe, mgr.Config{
			DisplayName: displayName, Description: description,
			StartType: mgr.StartAutomatic, ServiceType: windows.SERVICE_WIN32_OWN_PROCESS,
		}, "service")
		if err != nil {
			return fmt.Errorf("create the service: %w", err)
		}
	}
	defer s.Close()
	s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
	}, uint32((24 * time.Hour).Seconds()))
	if err := s.Start(); err != nil {
		return fmt.Errorf("start the service: %w", err)
	}
	fmt.Fprintln(out, "Started the Open Bars service")

	if err := writeUninstallEntry(exe, version); err != nil {
		fmt.Fprintln(out, "Couldn't add Open Bars to Settings › Apps:", err)
	}
	if err := startMenuShortcut(exe); err != nil {
		fmt.Fprintln(out, "Couldn't add the Start menu shortcut:", err)
	}
	return nil
}

// Uninstall removes the service, shortcut, Apps entry and files. With
// removeData it also deletes the pairing and log.
func Uninstall(removeData bool, out io.Writer) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("can't manage services (run as administrator): %w", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(service.Name); err == nil {
		stop(s)
		if err := s.Delete(); err != nil {
			s.Close()
			return fmt.Errorf("remove the service: %w", err)
		}
		s.Close()
		fmt.Fprintln(out, "Removed the Open Bars service")
	}
	registry.DeleteKey(registry.LOCAL_MACHINE, uninstallKey)
	os.Remove(shortcutPath())
	RemoveAutostart()
	if removeData {
		os.RemoveAll(service.DataDir())
		fmt.Fprintln(out, "Deleted the pairing and log")
	}
	// The running exe can't delete itself; a short-lived cmd does it after we exit.
	dir := Dir()
	if _, err := os.Stat(dir); err == nil {
		cmd := exec.Command("cmd.exe", "/c", "ping -n 3 127.0.0.1 >nul & rmdir /s /q "+syscall.EscapeArg(dir))
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS}
		cmd.Start()
		fmt.Fprintln(out, "Removing", dir)
	}
	return nil
}

// SetAutostart starts the tray app at sign-in for the current user.
func SetAutostart(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(runValue, syscall.EscapeArg(exe)+" tray")
}

// RemoveAutostart undoes SetAutostart.
func RemoveAutostart() {
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
		k.DeleteValue(runValue)
		k.Close()
	}
}

func stop(s *mgr.Service) {
	st, err := s.Control(svc.Stop)
	if err != nil {
		return
	}
	for deadline := time.Now().Add(20 * time.Second); st.State != svc.Stopped && time.Now().Before(deadline); {
		time.Sleep(300 * time.Millisecond)
		if st, err = s.Query(); err != nil {
			return
		}
	}
}

func writeUninstallEntry(exe, version string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	for name, v := range map[string]string{
		"DisplayName":     displayName,
		"DisplayVersion":  version,
		"Publisher":       "Open Bars",
		"DisplayIcon":     exe,
		"InstallLocation": Dir(),
		"UninstallString": syscall.EscapeArg(exe) + " uninstall",
		"URLInfoAbout":    "https://github.com/aayy-dev/OpenBars",
	} {
		if err := k.SetStringValue(name, v); err != nil {
			return err
		}
	}
	k.SetDWordValue("NoModify", 1)
	k.SetDWordValue("NoRepair", 1)
	return nil
}

func shortcutPath() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, `Microsoft\Windows\Start Menu\Programs`, "Open Bars.lnk")
}

// startMenuShortcut uses PowerShell's WScript.Shell to make the .lnk.
func startMenuShortcut(exe string) error {
	script := fmt.Sprintf(`$s=(New-Object -ComObject WScript.Shell).CreateShortcut('%s');$s.TargetPath='%s';$s.Arguments='tray';$s.WorkingDirectory='%s';$s.Description='Open Bars Connect';$s.Save()`,
		psQuote(shortcutPath()), psQuote(exe), psQuote(filepath.Dir(exe)))
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(b)))
	}
	return nil
}

func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := to + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// A running exe can't be overwritten, but it can be renamed out of the way.
	if _, err := os.Stat(to); err == nil {
		old := to + ".old"
		os.Remove(old)
		os.Rename(to, old)
		defer os.Remove(old) // fails harmlessly while the old copy still runs
	}
	return os.Rename(tmp, to)
}
