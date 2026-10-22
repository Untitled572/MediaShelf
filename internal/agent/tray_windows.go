//go:build windows

package agent

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmNull          = 0x0000
	wmApp           = 0x8000
	wmTrayCallback  = wmApp + 1
	wmScanFinished  = wmApp + 2
	wmRunnerStopped = wmApp + 3
	wmRButtonUp     = 0x0205
	wmLButtonUp     = 0x0202
	wmContextMenu   = 0x007B
	idiApplication  = 32512
	niAdd           = 0
	niDelete        = 2
	nifMessage      = 1
	nifIcon         = 2
	nifTip          = 4
	mfString        = 0
	mfByPosition    = 0x0400
	mfGrayed        = 1
	mfSeparator     = 0x0800
	tpmRightButton  = 2
	tpmBottomAlign  = 0x0020
	swShowNormal    = 1
	commandStatus   = 101
	commandWebUI    = 102
	commandScan     = 103
	commandExit     = 104
)

type point struct{ X, Y int32 }
type winMessage struct {
	HWnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}
type wndClassEx struct {
	CbSize     uint32
	Style      uint32
	WndProc    uintptr
	CbClsExtra int32
	CbWndExtra int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  uintptr
}
type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUID             [16]byte
	BalloonIcon      uintptr
}

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	shell32             = syscall.NewLazyDLL("shell32.dll")
	registerClassExW    = user32.NewProc("RegisterClassExW")
	createWindowExW     = user32.NewProc("CreateWindowExW")
	defWindowProcW      = user32.NewProc("DefWindowProcW")
	getMessageW         = user32.NewProc("GetMessageW")
	translateMessage    = user32.NewProc("TranslateMessage")
	dispatchMessageW    = user32.NewProc("DispatchMessageW")
	postQuitMessage     = user32.NewProc("PostQuitMessage")
	destroyWindow       = user32.NewProc("DestroyWindow")
	postMessageW        = user32.NewProc("PostMessageW")
	createPopupMenu     = user32.NewProc("CreatePopupMenu")
	appendMenuW         = user32.NewProc("AppendMenuW")
	modifyMenuW         = user32.NewProc("ModifyMenuW")
	destroyMenu         = user32.NewProc("DestroyMenu")
	getCursorPos        = user32.NewProc("GetCursorPos")
	setForegroundWindow = user32.NewProc("SetForegroundWindow")
	trackPopupMenu      = user32.NewProc("TrackPopupMenu")
	loadIconW           = user32.NewProc("LoadIconW")
	getModuleHandleW    = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
	shellNotifyIconW    = shell32.NewProc("Shell_NotifyIconW")
	shellExecuteW       = shell32.NewProc("ShellExecuteW")
)

type trayState struct {
	hwnd     uintptr
	menu     uintptr
	icon     uintptr
	runner   *Runner
	ctx      context.Context
	logger   *log.Logger
	webURL   string
	scanning bool
}

var currentTray *trayState

// RunTray starts the agent worker and owns the calling thread for the native
// Windows message loop until the user chooses Exit or the context is canceled.
func RunTray(ctx context.Context, c Config, runner *Runner, scanAll bool, logger *log.Logger) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	className, _ := syscall.UTF16PtrFromString("MediaShelfAgentTrayWindow")
	instance, _, _ := getModuleHandleW.Call(0)
	proc := syscall.NewCallback(trayWindowProc)
	wc := wndClassEx{CbSize: uint32(unsafe.Sizeof(wndClassEx{})), WndProc: proc, Instance: instance, ClassName: className}
	if atom, _, err := registerClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 && err != syscall.Errno(1410) {
		return fmt.Errorf("register tray window class: %w", err)
	}
	const hwndMessage = ^uintptr(2) // HWND_MESSAGE (-3)
	hwnd, _, err := createWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, hwndMessage, 0, instance, 0)
	if hwnd == 0 {
		return fmt.Errorf("create tray window: %w", err)
	}
	state := &trayState{hwnd: hwnd, runner: runner, ctx: ctx, logger: logger, webURL: c.ServerURL}
	currentTray = state
	if err := state.addIcon(); err != nil {
		destroyWindow.Call(hwnd)
		currentTray = nil
		return err
	}
	if err := state.createMenu(); err != nil {
		state.removeIcon()
		destroyWindow.Call(hwnd)
		currentTray = nil
		return err
	}
	go func() {
		if e := runner.Run(ctx, false, scanAll); e != nil {
			state.logf("agent stopped: %v", e)
			postMessageW.Call(hwnd, wmRunnerStopped, 0, 0)
		}
	}()
	go func() { <-ctx.Done(); postMessageW.Call(hwnd, wmClose, 0, 0) }()
	var msg winMessage
	for {
		ret, _, callErr := getMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) == -1 {
			return fmt.Errorf("read tray window message: %w", callErr)
		}
		if ret == 0 {
			break
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	state.removeIcon()
	currentTray = nil
	return nil
}

func (s *trayState) addIcon() error {
	icon, _, err := loadIconW.Call(0, uintptr(idiApplication))
	if icon == 0 {
		return fmt.Errorf("load tray icon: %w", err)
	}
	s.icon = icon
	var d notifyIconData
	d.CbSize = uint32(unsafe.Sizeof(d))
	d.HWnd = s.hwnd
	d.UID = 1
	d.Flags = nifMessage | nifIcon | nifTip
	d.CallbackMessage = wmTrayCallback
	d.Icon = icon
	tip, _ := syscall.UTF16FromString("MediaShelf Agent")
	copy(d.Tip[:], tip)
	ok, _, e := shellNotifyIconW.Call(niAdd, uintptr(unsafe.Pointer(&d)))
	if ok == 0 {
		return fmt.Errorf("add tray icon: %w", e)
	}
	return nil
}
func (s *trayState) removeIcon() {
	var d notifyIconData
	d.CbSize = uint32(unsafe.Sizeof(d))
	d.HWnd = s.hwnd
	d.UID = 1
	shellNotifyIconW.Call(niDelete, uintptr(unsafe.Pointer(&d)))
}
func (s *trayState) createMenu() error {
	m, _, err := createPopupMenu.Call()
	if m == 0 {
		return fmt.Errorf("create tray menu: %w", err)
	}
	s.menu = m
	if err := s.appendMenu(commandStatus, "状态：运行中", mfString|mfGrayed); err != nil {
		return err
	}
	if err := s.appendMenu(commandWebUI, "打开 MediaShelf", mfString); err != nil {
		return err
	}
	if err := s.appendSeparator(); err != nil {
		return err
	}
	if err := s.appendMenu(commandScan, "立即扫描", mfString); err != nil {
		return err
	}
	if err := s.appendMenu(commandExit, "退出", mfString); err != nil {
		return err
	}
	return nil
}
func (s *trayState) appendMenu(id uintptr, text string, flags uintptr) error {
	p, e := syscall.UTF16PtrFromString(text)
	if e != nil {
		return e
	}
	ok, _, err := appendMenuW.Call(s.menu, flags, id, uintptr(unsafe.Pointer(p)))
	if ok == 0 {
		return fmt.Errorf("add tray menu item: %w", err)
	}
	return nil
}
func (s *trayState) appendSeparator() error {
	ok, _, err := appendMenuW.Call(s.menu, mfSeparator, 0, 0)
	if ok == 0 {
		return fmt.Errorf("add tray menu separator: %w", err)
	}
	return nil
}
func (s *trayState) setStatus(text string) {
	p, e := syscall.UTF16PtrFromString(text)
	if e != nil {
		return
	}
	modifyMenuW.Call(s.menu, 0, mfByPosition|mfString|mfGrayed, commandStatus, uintptr(unsafe.Pointer(p)))
}
func (s *trayState) showMenu() {
	var p point
	getCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	setForegroundWindow.Call(s.hwnd)
	trackPopupMenu.Call(s.menu, tpmRightButton|tpmBottomAlign, uintptr(p.X), uintptr(p.Y), 0, s.hwnd, 0)
	postMessageW.Call(s.hwnd, wmNull, 0, 0)
}
func (s *trayState) openWebUI() {
	action, _ := syscall.UTF16PtrFromString("open")
	target, e := syscall.UTF16PtrFromString(s.webURL)
	if e != nil {
		return
	}
	shellExecuteW.Call(s.hwnd, uintptr(unsafe.Pointer(action)), uintptr(unsafe.Pointer(target)), 0, 0, swShowNormal)
}
func (s *trayState) startScan() {
	if s.scanning {
		return
	}
	s.scanning = true
	s.setStatus("状态：正在扫描")
	s.logf("tray requested a library scan")
	go func() {
		err := s.runner.ScanNow(s.ctx)
		if err != nil {
			s.logf("tray scan failed: %v", err)
			postMessageW.Call(s.hwnd, wmScanFinished, 0, 0)
		} else {
			postMessageW.Call(s.hwnd, wmScanFinished, 1, 0)
		}
	}()
}
func (s *trayState) logf(f string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(f, args...)
	}
}

func trayWindowProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	s := currentTray
	switch uint32(msg) {
	case wmTrayCallback:
		if uint32(lParam) == wmRButtonUp || uint32(lParam) == wmLButtonUp || uint32(lParam) == wmContextMenu {
			if s != nil {
				s.showMenu()
			}
		}
		return 0
	case wmCommand:
		if s == nil {
			return 0
		}
		switch uint16(wParam) {
		case commandWebUI:
			s.openWebUI()
		case commandScan:
			s.startScan()
		case commandExit:
			destroyWindow.Call(hwnd)
		}
		return 0
	case wmScanFinished:
		if s != nil {
			s.scanning = false
			if wParam != 0 {
				s.setStatus("状态：运行中")
			} else {
				s.setStatus("状态：扫描失败")
			}
		}
		return 0
	case wmRunnerStopped:
		if s != nil {
			s.setStatus("状态：已停止")
		}
		return 0
	case wmClose:
		destroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		if s != nil {
			s.removeIcon()
			destroyMenu.Call(s.menu)
		}
		postQuitMessage.Call(0)
		return 0
	default:
		r, _, _ := defWindowProcW.Call(hwnd, msg, wParam, lParam)
		return r
	}
}
