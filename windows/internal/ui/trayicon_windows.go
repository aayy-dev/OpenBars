package ui

import (
	"encoding/binary"
	"errors"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A small notification-area icon built directly on Shell_NotifyIcon:
//   - the menu is rebuilt each time it opens, from the current status;
//   - it opens clear of the taskbar (TPMPARAMS.rcExclude), wherever the
//     taskbar is;
//   - menus follow Windows' dark mode.

var (
	shell32tray = windows.NewLazySystemDLL("shell32.dll")
	user32tray  = windows.NewLazySystemDLL("user32.dll")

	procShellNotifyIconW        = shell32tray.NewProc("Shell_NotifyIconW")
	procSHAppBarMessage         = shell32tray.NewProc("SHAppBarMessage")
	procRegisterClassExW        = user32tray.NewProc("RegisterClassExW")
	procCreateWindowExW         = user32tray.NewProc("CreateWindowExW")
	procDefWindowProcW          = user32tray.NewProc("DefWindowProcW")
	procDestroyWindow           = user32tray.NewProc("DestroyWindow")
	procGetMessageW             = user32tray.NewProc("GetMessageW")
	procTranslateMessage        = user32tray.NewProc("TranslateMessage")
	procDispatchMessageW        = user32tray.NewProc("DispatchMessageW")
	procPostMessageW            = user32tray.NewProc("PostMessageW")
	procPostQuitMessage         = user32tray.NewProc("PostQuitMessage")
	procRegisterWindowMessageW  = user32tray.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu         = user32tray.NewProc("CreatePopupMenu")
	procAppendMenuW             = user32tray.NewProc("AppendMenuW")
	procDestroyMenu             = user32tray.NewProc("DestroyMenu")
	procTrackPopupMenuEx        = user32tray.NewProc("TrackPopupMenuEx")
	procSetForegroundWindowTray = user32tray.NewProc("SetForegroundWindow")
	procCreateIconFromResourceE = user32tray.NewProc("CreateIconFromResourceEx")
	procDestroyIcon             = user32tray.NewProc("DestroyIcon")
	procGetDpiForSystem         = user32tray.NewProc("GetDpiForSystem")
	procGetSystemMetricsForDpi  = user32tray.NewProc("GetSystemMetricsForDpi")
	procGetCursorPos            = user32tray.NewProc("GetCursorPos")
)

const (
	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nimSetVersion = 4
	nifMessage    = 0x1
	nifIcon       = 0x2
	nifTip        = 0x4
	nifShowTip    = 0x80

	wmApp         = 0x8000
	wmTrayCallbck = wmApp + 1
	wmTrayUpdate  = wmApp + 2
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmContextMenu = 0x007B
	wmLButtonUp   = 0x0202
	ninSelect     = 0x0400
	ninKeySelect  = 0x0401

	mfString    = 0x0
	mfGrayed    = 0x1
	mfChecked   = 0x8
	mfSeparator = 0x800

	tpmReturnCmd  = 0x0100
	tpmRightBtn   = 0x0002
	tpmBottom     = 0x0020
	tpmRightAlign = 0x0008
	tpmVertical   = 0x0040
	tpmHorizontal = 0x0000

	abmGetTaskbarPos = 0x5
	abeLeft          = 0
	abeTop           = 1
	abeRight         = 2
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             windows.HWND
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     windows.Handle
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }
type msg struct {
	HWnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}
type tpmParams struct {
	CbSize    uint32
	RcExclude rect
}
type appBarData struct {
	CbSize           uint32
	HWnd             windows.HWND
	UCallbackMessage uint32
	UEdge            uint32
	Rc               rect
	LParam           uintptr
}

// MenuItem is one entry of the tray menu; a zero ID is a separator.
type MenuItem struct {
	ID       uint32
	Title    string
	Disabled bool
	Checked  bool
}

// TrayIcon is the notification-area icon.
type TrayIcon struct {
	// Menu returns the items to show when the menu opens.
	Menu func() []MenuItem
	// OnMenu is called with the chosen item's ID.
	OnMenu func(id uint32)
	// OnClick is called on a left click (or Enter).
	OnClick func()

	mu      sync.Mutex
	hwnd    windows.HWND
	icon    []byte // current .ico
	tip     string
	hicon   windows.Handle
	created uint32 // the "TaskbarCreated" message, sent when Explorer restarts
}

var activeTray *TrayIcon

// SetIcon changes the icon (an .ico file's bytes) and tooltip.
func (t *TrayIcon) SetIcon(ico []byte, tip string) {
	t.mu.Lock()
	t.icon, t.tip = ico, tip
	h := t.hwnd
	t.mu.Unlock()
	if h != 0 {
		procPostMessageW.Call(uintptr(h), wmTrayUpdate, 0, 0)
	}
}

// Quit removes the icon and ends Run.
func (t *TrayIcon) Quit() {
	t.mu.Lock()
	h := t.hwnd
	t.mu.Unlock()
	if h != 0 {
		procPostMessageW.Call(uintptr(h), wmClose, 0, 0)
	}
}

// Run shows the icon and handles its messages until Quit.
func (t *TrayIcon) Run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	allowDarkMenus()

	inst, err := getModuleHandle()
	if err != nil {
		return err
	}
	className, _ := windows.UTF16PtrFromString("OpenBarsTray")
	wc := wndClassEx{LpfnWndProc: windows.NewCallback(trayWndProc), HInstance: inst, LpszClassName: className}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	if r, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return e
	}
	activeTray = t
	tbc, _ := windows.UTF16PtrFromString("TaskbarCreated")
	created, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tbc)))
	t.created = uint32(created)
	hwnd, _, e := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)),
		0, 0, 0, 0, 0, 0, 0, uintptr(inst), 0)
	if hwnd == 0 {
		return e
	}
	t.mu.Lock()
	t.hwnd = windows.HWND(hwnd)
	t.mu.Unlock()
	t.add()

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	return nil
}

func (t *TrayIcon) data() notifyIconData {
	nid := notifyIconData{HWnd: t.hwnd, UID: 1}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	return nid
}

// add puts the icon in the notification area (again, after Explorer restarts).
func (t *TrayIcon) add() {
	nid := t.data()
	nid.UFlags = nifMessage | nifIcon | nifTip | nifShowTip
	nid.UCallbackMessage = wmTrayCallbck
	t.fill(&nid)
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	nid.UVersion = 4 // NOTIFYICON_VERSION_4: richer callbacks
	procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&nid)))
}

func (t *TrayIcon) update() {
	nid := t.data()
	nid.UFlags = nifIcon | nifTip | nifShowTip
	t.fill(&nid)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

// fill sets the icon handle and tooltip from the current state.
func (t *TrayIcon) fill(nid *notifyIconData) {
	t.mu.Lock()
	ico, tip := t.icon, t.tip
	t.mu.Unlock()
	if h, err := iconFromICO(ico, smallIconSize()); err == nil {
		if t.hicon != 0 {
			procDestroyIcon.Call(uintptr(t.hicon))
		}
		t.hicon = h
	}
	nid.HIcon = t.hicon
	u, _ := windows.UTF16FromString(tip)
	copy(nid.SzTip[:len(nid.SzTip)-1], u)
}

func trayWndProc(hwnd windows.HWND, message uint32, wParam, lParam uintptr) uintptr {
	t := activeTray
	switch {
	case t == nil:
	case message == wmTrayCallbck:
		switch uint32(lParam & 0xffff) {
		case wmContextMenu:
			t.showMenu(int32(int16(wParam&0xffff)), int32(int16(wParam>>16&0xffff)))
		case ninSelect, ninKeySelect, wmLButtonUp:
			if t.OnClick != nil {
				go t.OnClick()
			}
		}
		return 0
	case message == wmTrayUpdate:
		t.update()
		return 0
	case message == t.created && t.created != 0:
		t.add()
		return 0
	case message == wmClose:
		nid := t.data()
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
		procDestroyWindow.Call(uintptr(hwnd))
		return 0
	case message == wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return r
}

// showMenu opens the menu at (x, y), kept clear of the taskbar.
func (t *TrayIcon) showMenu(x, y int32) {
	if x == 0 && y == 0 {
		var p point
		procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
		x, y = p.X, p.Y
	}
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 || t.Menu == nil {
		return
	}
	defer procDestroyMenu.Call(menu)
	for _, it := range t.Menu() {
		if it.ID == 0 {
			procAppendMenuW.Call(menu, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfString)
		if it.Disabled {
			flags |= mfGrayed
		}
		if it.Checked {
			flags |= mfChecked
		}
		title, _ := windows.UTF16PtrFromString(it.Title)
		procAppendMenuW.Call(menu, flags, uintptr(it.ID), uintptr(unsafe.Pointer(title)))
	}

	flags := uintptr(tpmReturnCmd | tpmRightBtn)
	params := tpmParams{}
	params.CbSize = uint32(unsafe.Sizeof(params))
	if edge, rc, ok := taskbar(); ok {
		params.RcExclude = rc
		switch edge {
		case abeLeft:
			flags |= tpmHorizontal
		case abeRight:
			flags |= tpmHorizontal | tpmRightAlign
		case abeTop:
			flags |= tpmVertical
		default: // bottom
			flags |= tpmVertical | tpmBottom
		}
	} else {
		flags |= tpmBottom
	}
	// The menu only closes on an outside click if our window is foreground.
	procSetForegroundWindowTray.Call(uintptr(t.hwnd))
	id, _, _ := procTrackPopupMenuEx.Call(menu, flags, uintptr(x), uintptr(y), uintptr(t.hwnd), uintptr(unsafe.Pointer(&params)))
	procPostMessageW.Call(uintptr(t.hwnd), 0, 0, 0) // WM_NULL, per the TrackPopupMenu docs
	if id != 0 && t.OnMenu != nil {
		go t.OnMenu(uint32(id))
	}
}

// taskbar returns which screen edge the taskbar is on and its rectangle.
func taskbar() (edge uint32, rc rect, ok bool) {
	abd := appBarData{}
	abd.CbSize = uint32(unsafe.Sizeof(abd))
	if r, _, _ := procSHAppBarMessage.Call(abmGetTaskbarPos, uintptr(unsafe.Pointer(&abd))); r == 0 {
		return 0, rect{}, false
	}
	return abd.UEdge, abd.Rc, true
}

// smallIconSize is the notification-area icon size at the system DPI.
func smallIconSize() int {
	const smCxSmIcon = 49
	dpi, _, _ := procGetDpiForSystem.Call()
	if dpi == 0 {
		dpi = 96
	}
	if s, _, _ := procGetSystemMetricsForDpi.Call(smCxSmIcon, dpi); s > 0 {
		return int(s)
	}
	return 16 * int(dpi) / 96
}

// iconFromICO makes an icon handle from the best frame of an .ico whose
// frames are PNGs (as tools/mkicons writes them).
func iconFromICO(ico []byte, size int) (windows.Handle, error) {
	if len(ico) < 6 {
		return 0, errors.New("no icon")
	}
	n := int(binary.LittleEndian.Uint16(ico[4:]))
	best, bestSize := -1, 0
	for i := 0; i < n && 6+16*(i+1) <= len(ico); i++ {
		s := int(ico[6+16*i])
		if s == 0 {
			s = 256
		}
		// Smallest frame at least as big as wanted, else the biggest.
		if best < 0 || (s >= size && (bestSize < size || s < bestSize)) || (bestSize < size && s > bestSize) {
			best, bestSize = i, s
		}
	}
	if best < 0 {
		return 0, errors.New("empty icon")
	}
	e := ico[6+16*best:]
	length := binary.LittleEndian.Uint32(e[8:])
	offset := binary.LittleEndian.Uint32(e[12:])
	if int(offset+length) > len(ico) {
		return 0, errors.New("bad icon")
	}
	data := ico[offset : offset+length]
	h, _, err := procCreateIconFromResourceE.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(length), 1,
		0x00030000, uintptr(size), uintptr(size), 0)
	if h == 0 {
		return 0, err
	}
	return windows.Handle(h), nil
}

func getModuleHandle() (windows.Handle, error) {
	var h windows.Handle
	err := windows.GetModuleHandleEx(0, nil, &h)
	return h, err
}

// allowDarkMenus lets this process's menus follow Windows' dark mode.
// uxtheme's SetPreferredAppMode (ordinal 135, Windows 10 1903+) is
// undocumented but used widely; failing to find it just keeps light menus.
func allowDarkMenus() {
	h, err := windows.LoadLibraryEx("uxtheme.dll", 0, windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		return
	}
	if p, err := windows.GetProcAddressByOrdinal(h, 135); err == nil {
		const allowDark = 1
		syscall.SyscallN(p, allowDark)
	}
	if p, err := windows.GetProcAddressByOrdinal(h, 136); err == nil { // FlushMenuThemes
		syscall.SyscallN(p)
	}
}
