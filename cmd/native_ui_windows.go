//go:build windows

package cmd

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"github.com/TinoA/claude-desktop-switcher/internal/platform"
	"golang.org/x/sys/windows"
)

const (
	nativeWMCommand       = 0x0111
	nativeWMKeyDown       = 0x0100
	nativeWMClose         = 0x0010
	nativeWMDestroy       = 0x0002
	nativeWMPaint         = 0x000F
	nativeWMTimer         = 0x0113
	nativeWMSetFont       = 0x0030
	nativeVKEscape        = 0x1B
	nativeWSChild         = 0x40000000
	nativeWSVisible       = 0x10000000
	nativeWSBorder        = 0x00800000
	nativeWSCaption       = 0x00C00000
	nativeWSSysMenu       = 0x00080000
	nativeWSPopup         = 0x80000000
	nativeWSExLayered     = 0x00080000
	nativeWSExTopmost     = 0x00000008
	nativeWSExNoActivate  = 0x08000000
	nativeWSExToolWindow  = 0x00000080
	nativeWSExDialogFrame = 0x00000001
	nativeWSTabStop       = 0x00010000
	nativeBSDefault       = 0x00000001
	nativeESPassword      = 0x00000020
	nativeESAutoHScroll   = 0x00000080
	nativeSMCXScreen      = 0
	nativeSMCYScreen      = 1
	nativeSWShow          = 5
	nativeSWNoActivate    = 8
	nativeSWPNoZOrder     = 0x0004
	nativeSWPNoActivate   = 0x0010
	nativeSWHide          = 0
	nativeLayeredAlpha    = 0x00000002
	nativeTimerID         = 1
	nativeDIIcon          = 0x00000003
	nativeDTCenter        = 0x00000001
	nativeDTVCenter       = 0x00000004
	nativeDTSingleLine    = 0x00000020
	nativeTransparent     = 1
	nativeMBYesNo         = 0x00000004
	nativeMBYesNoCancel   = 0x00000003
	nativeMBIconQuestion  = 0x00000020
	nativeMBIconWarning   = 0x00000030
	nativeMBSetForeground = 0x00010000
	nativeMBTopMost       = 0x00040000
	nativeIDConfirm       = 1001
	nativeIDCancel        = 1002
	nativeOFNExplorer     = 0x00080000
	nativeOFNPathExists   = 0x00000800
	nativeOFNFileExists   = 0x00001000
	nativeOFNOverwrite    = 0x00000002
	windowsAppUserModelID = "TinoA.ClaudeSwap.Tray.1"
)

var (
	nativeUser32            = windows.NewLazySystemDLL("user32.dll")
	nativeKernel32          = windows.NewLazySystemDLL("kernel32.dll")
	nativeShell32           = windows.NewLazySystemDLL("shell32.dll")
	nativeGDI32             = windows.NewLazySystemDLL("gdi32.dll")
	nativeCommDlg           = windows.NewLazySystemDLL("comdlg32.dll")
	nativeRegisterClassProc = nativeUser32.NewProc("RegisterClassExW")
	nativeCreateWindow      = nativeUser32.NewProc("CreateWindowExW")
	nativeDestroyWindow     = nativeUser32.NewProc("DestroyWindow")
	nativeDefWindowProc     = nativeUser32.NewProc("DefWindowProcW")
	nativeDispatch          = nativeUser32.NewProc("DispatchMessageW")
	nativeGetMessage        = nativeUser32.NewProc("GetMessageW")
	nativeTranslate         = nativeUser32.NewProc("TranslateMessage")
	nativePostQuit          = nativeUser32.NewProc("PostQuitMessage")
	nativePostMessage       = nativeUser32.NewProc("PostMessageW")
	nativeShowWindow        = nativeUser32.NewProc("ShowWindow")
	nativeUpdateWindow      = nativeUser32.NewProc("UpdateWindow")
	nativeSetForeground     = nativeUser32.NewProc("SetForegroundWindow")
	nativeSetActiveWindow   = nativeUser32.NewProc("SetActiveWindow")
	nativeBringToTop        = nativeUser32.NewProc("BringWindowToTop")
	nativeSetFocus          = nativeUser32.NewProc("SetFocus")
	nativeGetForeground     = nativeUser32.NewProc("GetForegroundWindow")
	nativeGetWindowThread   = nativeUser32.NewProc("GetWindowThreadProcessId")
	nativeAttachThreadInput = nativeUser32.NewProc("AttachThreadInput")
	nativeGetMetrics        = nativeUser32.NewProc("GetSystemMetrics")
	nativeGetWindowRect     = nativeUser32.NewProc("GetWindowRect")
	nativeIsWindowVisible   = nativeUser32.NewProc("IsWindowVisible")
	nativeIsIconic          = nativeUser32.NewProc("IsIconic")
	nativeSetWindowPos      = nativeUser32.NewProc("SetWindowPos")
	nativeSetLayered        = nativeUser32.NewProc("SetLayeredWindowAttributes")
	nativeSetTimer          = nativeUser32.NewProc("SetTimer")
	nativeKillTimer         = nativeUser32.NewProc("KillTimer")
	nativeInvalidate        = nativeUser32.NewProc("InvalidateRect")
	nativeBeginPaint        = nativeUser32.NewProc("BeginPaint")
	nativeEndPaint          = nativeUser32.NewProc("EndPaint")
	nativeSendMessage       = nativeUser32.NewProc("SendMessageW")
	nativeGetWindowText     = nativeUser32.NewProc("GetWindowTextW")
	nativeGetTextLength     = nativeUser32.NewProc("GetWindowTextLengthW")
	nativeMessageBox        = nativeUser32.NewProc("MessageBoxW")
	nativeSetProcessAppID   = nativeShell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	nativeDrawIcon          = nativeUser32.NewProc("DrawIconEx")
	nativeDestroyIcon       = nativeUser32.NewProc("DestroyIcon")
	nativeGetModule         = nativeKernel32.NewProc("GetModuleHandleW")
	nativeGetCurrentThread  = nativeKernel32.NewProc("GetCurrentThreadId")
	nativeExtractIconEx     = nativeShell32.NewProc("ExtractIconExW")
	nativeCreateBrush       = nativeGDI32.NewProc("CreateSolidBrush")
	nativeDeleteObject      = nativeGDI32.NewProc("DeleteObject")
	nativeFillRect          = nativeUser32.NewProc("FillRect")
	nativeSetBkMode         = nativeGDI32.NewProc("SetBkMode")
	nativeSetTextColor      = nativeGDI32.NewProc("SetTextColor")
	nativeDrawText          = nativeUser32.NewProc("DrawTextW")
	nativeCreateFont        = nativeGDI32.NewProc("CreateFontW")
	nativeSelectObject      = nativeGDI32.NewProc("SelectObject")
	nativeGetOpenFile       = nativeCommDlg.NewProc("GetOpenFileNameW")
	nativeGetSaveFile       = nativeCommDlg.NewProc("GetSaveFileNameW")
	nativeCommDlgError      = nativeCommDlg.NewProc("CommDlgExtendedError")
)

func setCurrentProcessAppUserModelID() error {
	appID, err := windows.UTF16PtrFromString(windowsAppUserModelID)
	if err != nil {
		return err
	}
	result, _, _ := nativeSetProcessAppID.Call(uintptr(unsafe.Pointer(appID)))
	if int32(result) < 0 {
		return fmt.Errorf("set AppUserModelID: HRESULT 0x%08X", uint32(result))
	}
	return nil
}

type nativeWndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type nativeMSG struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	ptX     int32
	ptY     int32
}

type nativeRect struct {
	left   int32
	top    int32
	right  int32
	bottom int32
}

type nativePaintStruct struct {
	hdc         uintptr
	erase       int32
	paint       nativeRect
	restore     int32
	incUpdate   int32
	rgbReserved [32]byte
}

var (
	nativeInputClassOnce   sync.Once
	nativeInputClassErr    error
	nativeInputStatesMu    sync.Mutex
	nativeInputStates      = make(map[uintptr]*nativeInputState)
	nativeOverlayClassOnce sync.Once
	nativeOverlayClassErr  error
	nativeOverlayStatesMu  sync.Mutex
	nativeOverlayStates    = make(map[uintptr]*nativeOverlayState)
)

type nativeInputState struct {
	edit      uintptr
	value     string
	confirmed bool
}

type nativeOverlayState struct {
	message string
	success bool
	frame   int
	icon    uintptr
	owner   uintptr
}

var nativeInputWndProc = windows.NewCallback(nativeInputWindowProc)
var nativeOverlayWndProc = windows.NewCallback(nativeOverlayWindowProc)

func nativeRegisterClass(name string, proc uintptr) error {
	hInstance, _, _ := nativeGetModule.Call(0)
	className := windows.StringToUTF16Ptr(name)
	windowClass := nativeWndClassEx{
		cbSize:        uint32(unsafe.Sizeof(nativeWndClassEx{})),
		lpfnWndProc:   proc,
		hInstance:     hInstance,
		lpszClassName: className,
	}
	if result, _, err := nativeRegisterClassProc.Call(uintptr(unsafe.Pointer(&windowClass))); result == 0 {
		if !errors.Is(err, windows.ERROR_CLASS_ALREADY_EXISTS) {
			return fmt.Errorf("register native window class: %w", err)
		}
	}
	return nil
}

func nativeRegisterInputClass() error {
	nativeInputClassOnce.Do(func() {
		nativeInputClassErr = nativeRegisterClass("WindowsClaudeSwapInput", nativeInputWndProc)
	})
	return nativeInputClassErr
}

func nativeRegisterOverlayClass() error {
	nativeOverlayClassOnce.Do(func() {
		nativeOverlayClassErr = nativeRegisterClass("WindowsClaudeSwapOverlay", nativeOverlayWndProc)
	})
	return nativeOverlayClassErr
}

func nativeTrayChoice(title, message string) (trayChoiceValue, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	result, err := nativeFocusedMessageBox(title, message, nativeMBYesNoCancel|nativeMBIconQuestion)
	switch result {
	case 6:
		return trayYes, nil
	case 7:
		return trayNo, nil
	case 2:
		return trayCancel, nil
	default:
		return trayCancel, err
	}
}

func nativeTrayConfirm(title, message string) (bool, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	result, err := nativeFocusedMessageBox(title, message, nativeMBYesNo|nativeMBIconQuestion)
	switch result {
	case 6:
		return true, nil
	case 7:
		return false, nil
	default:
		return false, err
	}
}

func trayWarning(title, message string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_, _ = nativeFocusedMessageBox(title, message, nativeMBIconWarning)
}

func nativeFocusedMessageBox(title, message string, flags uintptr) (uintptr, error) {
	owner := nativeMessageOwner()
	if owner != 0 {
		defer nativeDestroyWindow.Call(owner)
		nativeActivateWindow(owner)
	}
	result, _, err := nativeMessageBox.Call(
		owner,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(message))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(title))),
		flags|nativeMBSetForeground|nativeMBTopMost,
	)
	return result, err
}

func nativeMessageOwner() uintptr {
	if err := nativeRegisterInputClass(); err != nil {
		return 0
	}
	screenWidth, _, _ := nativeGetMetrics.Call(nativeSMCXScreen)
	screenHeight, _, _ := nativeGetMetrics.Call(nativeSMCYScreen)
	hInstance, _, _ := nativeGetModule.Call(0)
	owner, _, _ := nativeCreateWindow.Call(
		nativeWSExTopmost|nativeWSExToolWindow|nativeWSExLayered,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("WindowsClaudeSwapInput"))),
		0,
		nativeWSPopup,
		screenWidth/2, screenHeight/2, 1, 1,
		0, 0, hInstance, 0,
	)
	if owner == 0 {
		return 0
	}
	nativeSetLayered.Call(owner, 0, 0, nativeLayeredAlpha)
	nativeShowWindow.Call(owner, nativeSWShow)
	nativeUpdateWindow.Call(owner)
	return owner
}

func nativeActivateWindow(hwnd uintptr) {
	currentThread, _, _ := nativeGetCurrentThread.Call()
	foreground, _, _ := nativeGetForeground.Call()
	var foregroundThread uintptr
	if foreground != 0 {
		foregroundThread, _, _ = nativeGetWindowThread.Call(foreground, 0)
	}
	attached := foregroundThread != 0 && foregroundThread != currentThread
	if attached {
		nativeAttachThreadInput.Call(currentThread, foregroundThread, 1)
		defer nativeAttachThreadInput.Call(currentThread, foregroundThread, 0)
	}
	nativeBringToTop.Call(hwnd)
	nativeSetForeground.Call(hwnd)
	nativeSetActiveWindow.Call(hwnd)
	nativeSetFocus.Call(hwnd)
}

func nativeTraySecretPrompt(title, message string) (string, error) {
	value, err := nativeInputDialog(title, message, true)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", errors.New("password cannot be empty")
	}
	return value, nil
}

func nativeInputDialog(title, message string, password bool) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := nativeRegisterInputClass(); err != nil {
		return "", err
	}
	width, height := 460, 190
	screenWidth, _, _ := nativeGetMetrics.Call(nativeSMCXScreen)
	screenHeight, _, _ := nativeGetMetrics.Call(nativeSMCYScreen)
	hInstance, _, _ := nativeGetModule.Call(0)
	state := &nativeInputState{}
	hwnd, _, err := nativeCreateWindow.Call(
		nativeWSExTopmost|nativeWSExToolWindow|nativeWSExDialogFrame,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("WindowsClaudeSwapInput"))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(title))),
		nativeWSCaption|nativeWSSysMenu|nativeWSVisible,
		uintptr((int(screenWidth)-width)/2), uintptr((int(screenHeight)-height)/2),
		uintptr(width), uintptr(height), 0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return "", err
	}
	nativeInputStatesMu.Lock()
	nativeInputStates[hwnd] = state
	nativeInputStatesMu.Unlock()
	defer func() {
		nativeInputStatesMu.Lock()
		delete(nativeInputStates, hwnd)
		nativeInputStatesMu.Unlock()
		nativeDestroyWindow.Call(hwnd)
	}()

	createNativeInputControl("STATIC", message, 20, 20, 410, 35, hwnd, 0, false)
	editStyle := uint32(nativeWSChild | nativeWSVisible | nativeWSBorder | nativeWSTabStop | nativeESAutoHScroll)
	if password {
		editStyle |= nativeESPassword
	}
	edit, _, _ := nativeCreateWindow.Call(
		0,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("EDIT"))),
		0,
		uintptr(editStyle), 20, 65, 410, 28, hwnd, 0, hInstance, 0,
	)
	state.edit = edit
	createNativeInputControl("BUTTON", "OK", 235, 115, 95, 32, hwnd, nativeIDConfirm, true)
	createNativeInputControl("BUTTON", "Cancel", 335, 115, 95, 32, hwnd, nativeIDCancel, true)
	nativeSetForeground.Call(hwnd)
	nativeSetFocus.Call(edit)

	var messageLoop nativeMSG
	for {
		result, _, getErr := nativeGetMessage.Call(uintptr(unsafe.Pointer(&messageLoop)), 0, 0, 0)
		if int32(result) == -1 {
			return "", fmt.Errorf("read native dialog message: %w", getErr)
		}
		if result == 0 {
			break
		}
		nativeTranslate.Call(uintptr(unsafe.Pointer(&messageLoop)))
		nativeDispatch.Call(uintptr(unsafe.Pointer(&messageLoop)))
	}
	if !state.confirmed {
		return "", errors.New("dialog cancelled")
	}
	return strings.TrimSpace(state.value), nil
}

func createNativeInputControl(className, text string, x, y, width, height int, parent, id uintptr, button bool) uintptr {
	style := uintptr(nativeWSChild | nativeWSVisible)
	if button {
		style |= nativeWSTabStop
		if id == nativeIDConfirm {
			style |= nativeBSDefault
		}
	}
	control, _, _ := nativeCreateWindow.Call(
		0,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(className))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(text))),
		style, uintptr(x), uintptr(y), uintptr(width), uintptr(height), parent, id, 0, 0,
	)
	if control != 0 {
		nativeSendMessage.Call(control, nativeWMSetFont, 0, 1)
	}
	return control
}

func nativeInputWindowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	nativeInputStatesMu.Lock()
	state := nativeInputStates[hwnd]
	nativeInputStatesMu.Unlock()
	if state != nil {
		switch message {
		case nativeWMCommand:
			switch wParam & 0xffff {
			case nativeIDConfirm:
				length, _, _ := nativeGetTextLength.Call(state.edit)
				buffer := make([]uint16, int(length)+1)
				nativeGetWindowText.Call(state.edit, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
				state.value = windows.UTF16ToString(buffer)
				state.confirmed = true
				nativeDestroyWindow.Call(hwnd)
			case nativeIDCancel:
				nativeDestroyWindow.Call(hwnd)
			}
		case nativeWMKeyDown:
			if wParam == nativeVKEscape {
				nativeDestroyWindow.Call(hwnd)
			}
		case nativeWMClose:
			nativeDestroyWindow.Call(hwnd)
		case nativeWMDestroy:
			nativePostQuit.Call(0)
		}
	}
	result, _, _ := nativeDefWindowProc.Call(hwnd, message, wParam, lParam)
	return result
}

func nativeTrayFileDialog(open bool, defaultName string) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	buffer := make([]uint16, 32768)
	if defaultName == "" {
		defaultName = "claude-desktop-switcher-backup.csb"
	}
	copy(buffer, windows.StringToUTF16(defaultName))
	filter := nativeBackupFileFilter()
	defaultExtension, _ := windows.UTF16PtrFromString("csb")
	dialog := nativeOpenFileName{
		lStructSize: uint32(unsafe.Sizeof(nativeOpenFileName{})),
		lpstrFilter: &filter[0],
		lpstrFile:   &buffer[0],
		nMaxFile:    uint32(len(buffer)),
		Flags:       nativeOFNExplorer | nativeOFNPathExists,
		lpstrDefExt: defaultExtension,
	}
	var result uintptr
	if open {
		dialog.Flags |= nativeOFNFileExists
		result, _, _ = nativeGetOpenFile.Call(uintptr(unsafe.Pointer(&dialog)))
	} else {
		dialog.Flags |= nativeOFNOverwrite
		result, _, _ = nativeGetSaveFile.Call(uintptr(unsafe.Pointer(&dialog)))
	}
	runtime.KeepAlive(filter)
	if result == 0 {
		code, _, _ := nativeCommDlgError.Call()
		if code != 0 {
			return "", fmt.Errorf("windows file picker failed: 0x%X", code)
		}
		return "", nil
	}
	return windows.UTF16ToString(buffer), nil
}

func nativeBackupFileFilter() []uint16 {
	return utf16.Encode([]rune("Claude Desktop Switcher backup (*.csb)\x00*.csb\x00All files (*.*)\x00*.*\x00\x00"))
}

type nativeOpenFileName struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	Flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	FlagsEx           uint32
}

func startNativeOverlay(message string, success bool, iconPaths ...string) *switchOverlay {
	overlay := &switchOverlay{ready: make(chan error, 1), done: make(chan struct{})}
	iconPath := ""
	if len(iconPaths) > 0 {
		iconPath = iconPaths[0]
	}
	go runNativeOverlay(overlay, message, success, iconPath)
	if err := <-overlay.ready; err != nil {
		return &switchOverlay{}
	}
	return overlay
}

func runNativeOverlay(overlay *switchOverlay, message string, success bool, iconPath string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var owner uintptr
	if provider, ok := platform.Current().(interface{ FocusDesktopWindow() (uintptr, error) }); ok {
		owner, _ = provider.FocusDesktopWindow()
	}
	bounds, visible := nativeOverlayPlacement(owner, nativeWindowBounds)
	if !visible {
		overlay.ready <- nil
		close(overlay.done)
		return
	}
	if err := nativeRegisterOverlayClass(); err != nil {
		overlay.ready <- err
		close(overlay.done)
		return
	}
	hInstance, _, _ := nativeGetModule.Call(0)
	state := &nativeOverlayState{message: message, success: success, owner: owner}
	state.icon = nativeExtractIcon(iconPath)
	hwnd, _, _ := nativeCreateWindow.Call(
		nativeWSExLayered|nativeWSExToolWindow|nativeWSExNoActivate,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("WindowsClaudeSwapOverlay"))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(ProductName))),
		nativeWSPopup,
		uintptr(bounds.left), uintptr(bounds.top), uintptr(bounds.right-bounds.left), uintptr(bounds.bottom-bounds.top), owner, 0, hInstance, 0,
	)
	if hwnd == 0 {
		if state.icon != 0 {
			nativeDestroyIcon.Call(state.icon)
		}
		overlay.ready <- errors.New("could not create native overlay")
		close(overlay.done)
		return
	}
	overlay.hwnd = hwnd
	nativeOverlayStatesMu.Lock()
	nativeOverlayStates[hwnd] = state
	nativeOverlayStatesMu.Unlock()
	nativeSetLayered.Call(hwnd, 0, 180, nativeLayeredAlpha)
	nativeSetTimer.Call(hwnd, nativeTimerID, 125, 0)
	nativeShowWindow.Call(hwnd, nativeSWNoActivate)
	nativeUpdateWindow.Call(hwnd)
	overlay.ready <- nil

	var messageLoop nativeMSG
	for {
		result, _, _ := nativeGetMessage.Call(uintptr(unsafe.Pointer(&messageLoop)), 0, 0, 0)
		if result <= 0 {
			break
		}
		nativeTranslate.Call(uintptr(unsafe.Pointer(&messageLoop)))
		nativeDispatch.Call(uintptr(unsafe.Pointer(&messageLoop)))
	}
	nativeOverlayStatesMu.Lock()
	delete(nativeOverlayStates, hwnd)
	nativeOverlayStatesMu.Unlock()
	close(overlay.done)
}

func nativeOverlayPlacement(owner uintptr, readBounds func(uintptr) (nativeRect, bool)) (nativeRect, bool) {
	if owner == 0 {
		return nativeRect{}, false
	}
	bounds, visible := readBounds(owner)
	return bounds, visible && bounds.right > bounds.left && bounds.bottom > bounds.top
}

func nativeWindowBounds(hwnd uintptr) (nativeRect, bool) {
	var bounds nativeRect
	visible, _, _ := nativeIsWindowVisible.Call(hwnd)
	minimized, _, _ := nativeIsIconic.Call(hwnd)
	ok, _, _ := nativeGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&bounds)))
	return bounds, ok != 0 && visible != 0 && minimized == 0
}

func nativeExtractIcon(path string) uintptr {
	if path == "" {
		return 0
	}
	var large uintptr
	nativeExtractIconEx.Call(
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(path))),
		0,
		uintptr(unsafe.Pointer(&large)),
		0,
		1,
	)
	return large
}

func nativeOverlayWindowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	nativeOverlayStatesMu.Lock()
	state := nativeOverlayStates[hwnd]
	nativeOverlayStatesMu.Unlock()
	if state == nil {
		result, _, _ := nativeDefWindowProc.Call(hwnd, message, wParam, lParam)
		return result
	}
	switch message {
	case nativeWMTimer:
		bounds, visible := nativeOverlayPlacement(state.owner, nativeWindowBounds)
		if !visible {
			nativeShowWindow.Call(hwnd, nativeSWHide)
			return 0
		}
		nativeSetWindowPos.Call(hwnd, 0, uintptr(bounds.left), uintptr(bounds.top), uintptr(bounds.right-bounds.left), uintptr(bounds.bottom-bounds.top), nativeSWPNoZOrder|nativeSWPNoActivate)
		nativeShowWindow.Call(hwnd, nativeSWNoActivate)
		state.frame++
		nativeInvalidate.Call(hwnd, 0, 0)
	case nativeWMPaint:
		var paint nativePaintStruct
		hdc, _, _ := nativeBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
		if hdc != 0 {
			brush, _, _ := nativeCreateBrush.Call(0x51565A)
			nativeFillRect.Call(hdc, uintptr(unsafe.Pointer(&paint.paint)), brush)
			nativeDeleteObject.Call(brush)
			nativeSetBkMode.Call(hdc, nativeTransparent)
			nativeSetTextColor.Call(hdc, 0x00FFFFFF)
			if state.icon != 0 && !state.success {
				x := (paint.paint.right - paint.paint.left - 96) / 2
				nativeDrawIcon.Call(hdc, uintptr(x), 100, state.icon, 96, 96, 0, 0, nativeDIIcon)
				nativeDrawCenteredText(hdc, []string{"◌", "◍", "◎", "◉"}[state.frame%4], paint.paint, 205)
			} else {
				glyph := "✓"
				if !state.success {
					glyph = []string{"◌", "◍", "◎", "◉"}[state.frame%4]
				} else {
					nativeSetTextColor.Call(hdc, 0x0000FF00)
				}
				nativeDrawCenteredText(hdc, glyph, paint.paint, 100)
				nativeSetTextColor.Call(hdc, 0x00FFFFFF)
			}
			nativeDrawCenteredText(hdc, state.message, paint.paint, 260)
			nativeEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
		}
	case nativeWMClose:
		nativeKillTimer.Call(hwnd, nativeTimerID)
		nativeDestroyWindow.Call(hwnd)
	case nativeWMDestroy:
		if state.icon != 0 {
			nativeDestroyIcon.Call(state.icon)
		}
		nativePostQuit.Call(0)
	}
	result, _, _ := nativeDefWindowProc.Call(hwnd, message, wParam, lParam)
	return result
}

func nativeDrawCenteredText(hdc uintptr, text string, bounds nativeRect, centerY int32) {
	font, _, _ := nativeCreateFont.Call(
		32, 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("Segoe UI"))),
	)
	if font != 0 {
		old, _, _ := nativeSelectObject.Call(hdc, font)
		defer func() {
			nativeSelectObject.Call(hdc, old)
			nativeDeleteObject.Call(font)
		}()
	}
	rect := nativeRect{left: bounds.left, top: centerY - 24, right: bounds.right, bottom: centerY + 24}
	nativeDrawText.Call(hdc, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(text))), ^uintptr(0), uintptr(unsafe.Pointer(&rect)), nativeDTCenter|nativeDTVCenter|nativeDTSingleLine)
}
