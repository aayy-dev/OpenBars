// Package winutil holds small Windows helpers: elevation, message boxes,
// the clipboard, console output and opening Explorer.
package winutil

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procShellExecuteExW  = shell32.NewProc("ShellExecuteExW")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	procRtlMoveMemory    = kernel32.NewProc("RtlMoveMemory")
	procAttachConsole    = kernel32.NewProc("AttachConsole")
)

// IsElevated reports whether this process runs as administrator.
func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

// ErrCancelled means the person said no to the UAC prompt.
var ErrCancelled = errors.New("cancelled")

// RunElevated starts this executable as administrator with args (showing
// the UAC prompt) and waits for it, returning its exit code.
func RunElevated(args ...string) (uint32, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	var quoted []string
	for _, a := range args {
		quoted = append(quoted, syscall.EscapeArg(a))
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	const seeMaskNoCloseProcess = 0x40
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess, lpVerb: verb, lpFile: file, lpParameters: params, nShow: windows.SW_HIDE}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errors.Is(e, windows.ERROR_CANCELLED) {
			return 0, ErrCancelled
		}
		return 0, e
	}
	defer windows.CloseHandle(info.hProcess)
	if _, err := windows.WaitForSingleObject(info.hProcess, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	err = windows.GetExitCodeProcess(info.hProcess, &code)
	return code, err
}

// Message box styles.
const (
	MBOK          = windows.MB_OK
	MBYesNo       = windows.MB_YESNO
	MBIconInfo    = windows.MB_ICONINFORMATION
	MBIconWarning = windows.MB_ICONWARNING
	MBIconError   = windows.MB_ICONERROR
	IDYes         = 6
)

// MessageBox shows a simple dialog and returns the button pressed.
func MessageBox(title, text string, style uint32) int32 {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	r, _ := windows.MessageBox(0, m, t, style|windows.MB_SETFOREGROUND)
	return r
}

// CopyText puts text on the clipboard.
func CopyText(text string) error {
	u, err := windows.UTF16FromString(strings.ReplaceAll(text, "\n", "\r\n"))
	if err != nil {
		return err
	}
	if r, _, e := procOpenClipboard.Call(0); r == 0 {
		return e
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	const gmemMoveable, cfUnicodeText = 0x0002, 13
	size := uintptr(len(u) * 2)
	h, _, e := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return e
	}
	p, _, e := procGlobalLock.Call(h)
	if p == 0 {
		return e
	}
	procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), size)
	procGlobalUnlock.Call(h)
	if r, _, e := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		return e
	}
	return nil
}

// ShowInExplorer opens Explorer with path selected.
func ShowInExplorer(path string) {
	verb, _ := windows.UTF16PtrFromString("open")
	exe, _ := windows.UTF16PtrFromString("explorer.exe")
	args, _ := windows.UTF16PtrFromString("/select," + syscall.EscapeArg(path))
	windows.ShellExecute(0, verb, exe, args, nil, windows.SW_SHOWNORMAL)
}

// AttachParentConsole sends this GUI program's output to the console it was
// started from, if any (for `OpenBars.exe install` in a terminal).
func AttachParentConsole() bool {
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return false
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
	return true
}
