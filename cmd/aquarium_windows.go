//go:build windows

package cmd

import (
	"errors"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/TinoA/claude-desktop-switcher/internal/aquarium"
	"github.com/TinoA/claude-desktop-switcher/internal/platform"
	"github.com/TinoA/claude-desktop-switcher/internal/profile"
	"github.com/TinoA/claude-desktop-switcher/internal/sessions"
	"golang.org/x/sys/windows"
)

// The aquarium sits above the clock: each open Claude window is a fish and
// the crowned one is the main window. A click on a fish shows or closes its
// window, the plus on the rim opens an account that has none. The window
// never takes focus, and its transparent pixels let clicks through.

const (
	aquariumClassName   = "WindowsClaudeSwapAquarium"
	aquariumTick        = 83 * time.Millisecond
	aquariumPanelWidth  = 276
	aquariumGap         = 10
	aquariumEdge        = 8
	aquariumTopRoom     = 28
	aquariumMinHeight   = 196
	aquariumShowMessage = 0x8001
	aquariumMaxListed   = 3

	wmMouseMove     = 0x0200
	wmLButtonUp     = 0x0202
	wmMouseLeave    = 0x02A3
	wmMouseActivate = 0x0021
	wmSetCursor     = 0x0020
	maNoActivate    = 3
	tmeLeave        = 2
	spiGetWorkArea  = 0x0030
	idcArrow        = 32512
	idcHand         = 32649
)

var (
	aquariumTrackMouse   = nativeUser32.NewProc("TrackMouseEvent")
	aquariumLoadCursor   = nativeUser32.NewProc("LoadCursorW")
	aquariumSetCursor    = nativeUser32.NewProc("SetCursor")
	aquariumSystemInfo   = nativeUser32.NewProc("SystemParametersInfoW")
	aquariumSystemDPI    = nativeUser32.NewProc("GetDpiForSystem")
	aquariumNotification = windows.NewLazySystemDLL("shell32.dll").NewProc("SHQueryUserNotificationState")
	aquariumClassOnce    sync.Once
	aquariumClassErr     error
	aquariumWndProc      = windows.NewCallback(aquariumWindowProc)
	aquariumCurrentMu    sync.Mutex
	aquariumCurrent      *aquariumWindow
)

var (
	aquariumCreateDIBSection = nativeGDI32.NewProc("CreateDIBSection")
	aquariumTextOut          = nativeGDI32.NewProc("TextOutW")
	aquariumTextExtent       = nativeGDI32.NewProc("GetTextExtentPoint32W")
	aquariumGdiFlush         = nativeGDI32.NewProc("GdiFlush")
	aquariumUpdateLayered    = nativeUser32.NewProc("UpdateLayeredWindow")
	aquariumGetDC            = nativeUser32.NewProc("GetDC")
	aquariumReleaseDC        = nativeUser32.NewProc("ReleaseDC")
)

type aquariumBitmapInfo struct {
	size          uint32
	width         int32
	height        int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPelsPerMeter int32
	yPelsPerMeter int32
	clrUsed       uint32
	clrImportant  uint32
}

type aquariumPoint struct{ x, y int32 }

type aquariumSize struct{ cx, cy int32 }

type aquariumBlend struct{ op, flags, constantAlpha, alphaFormat byte }

var (
	panelFill   = color.NRGBA{R: 31, G: 27, B: 36, A: 255}
	panelBorder = color.NRGBA{R: 58, G: 48, B: 70, A: 255}
	panelShadow = color.NRGBA{R: 14, G: 11, B: 18, A: 255}
	panelText   = color.NRGBA{R: 243, G: 233, B: 226, A: 255}
	panelMuted  = color.NRGBA{R: 201, G: 188, B: 201, A: 255}
	panelAccent = color.NRGBA{R: 242, G: 185, B: 160, A: 255}
	buttonHover = color.NRGBA{R: 217, G: 119, B: 87, A: 255}
	barStripe   = color.NRGBA{R: 232, G: 149, B: 111, A: 255}
	tagInk      = color.NRGBA{R: 20, G: 15, B: 24, A: 255}
	iconWhite   = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
)

var aquariumIcons = map[string][]string{
	"eye":   {"..xxxxx..", ".x.....x.", "x..xxx..x", "x..x.x..x", "x..xxx..x", ".x.....x.", "..xxxxx.."},
	"close": {"x.....x", ".x...x.", "..x.x..", "...x...", "..x.x..", ".x...x.", "x.....x"},
	"plus":  {"...x...", "...x...", "...x...", "xxxxxxx", "...x...", "...x...", "...x..."},
	"check": {"......x", ".....x.", "x...x..", ".x.x...", "..x...."},
	"crown": {"x..x..x", "xx.x.xx", "xxxxxxx", "xxxxxxx"},
}

// unknownMainKey stands for a main window whose account has no saved profile.
const unknownMainKey = "\x00main"

// aquariumAccount is what the tray tells the aquarium about one account.
type aquariumAccount struct {
	key     string
	label   string
	palette aquarium.Palette
	open    bool
	main    bool
	working []string
}

type aquariumPanelKind int

const (
	fishPanel aquariumPanelKind = iota + 1
	confirmPanel
	plusPanel
	progressPanel
)

type aquariumPanel struct {
	kind     aquariumPanelKind
	key      string
	action   string
	note     string
	title    string
	progress float64
	doneAt   time.Time
}

type aquariumButton struct {
	rect   image.Rectangle
	action string
	key    string
}

type maskKey struct {
	text string
	font uintptr
}

type aquariumWindow struct {
	tray *trayState

	mu       sync.Mutex
	accounts []aquariumAccount
	version  int
	panel    *aquariumPanel
	wanted   bool

	hwnd       uintptr
	tank       *aquarium.Tank
	synced     int
	dpi        float64
	scale      int
	width      int
	height     int
	x, y       int
	bowl       image.Point
	dc         uintptr
	textDC     uintptr
	bitmap     uintptr
	oldBitmap  uintptr
	pixels     []byte
	frame      *image.NRGBA
	art        *image.NRGBA
	fonts      map[string]uintptr
	masks      map[maskKey]*image.Alpha
	buttons    []aquariumButton
	panelRect  image.Rectangle
	hover      string
	hoverFish  string
	tracking   bool
	pointerIn  bool
	pointerOut time.Time
	visible    bool
	fullScreen bool
	ticks      int
}

// startAquarium opens the aquarium's window on a thread of its own; it is
// shown when wanted is true.
func startAquarium(tray *trayState, wanted bool) *aquariumWindow {
	a := &aquariumWindow{tray: tray, wanted: wanted, tank: aquarium.NewTank(uint64(time.Now().UnixNano())), masks: make(map[maskKey]*image.Alpha)}
	ready := make(chan error, 1)
	go a.run(ready)
	if err := <-ready; err != nil {
		tray.setStatus("Could not start the aquarium: " + err.Error())
		return nil
	}
	return a
}

func (a *aquariumWindow) setAccounts(accounts []aquariumAccount) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.accounts = accounts
	a.version++
	a.mu.Unlock()
}

func (a *aquariumWindow) setWanted(wanted bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.wanted = wanted
	a.mu.Unlock()
	show := uintptr(0)
	if wanted {
		show = 1
	}
	nativePostMessage.Call(a.hwnd, aquariumShowMessage, show, 0)
}

func (a *aquariumWindow) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if previous := setGuideThreadDPIAwareness(); previous != 0 {
		defer nativeSetThreadDPIContext.Call(previous)
	}
	aquariumClassOnce.Do(func() { aquariumClassErr = nativeRegisterClass(aquariumClassName, aquariumWndProc) })
	if aquariumClassErr != nil {
		ready <- aquariumClassErr
		return
	}
	if err := a.prepare(); err != nil {
		ready <- err
		return
	}
	defer a.release()
	aquariumCurrentMu.Lock()
	aquariumCurrent = a
	aquariumCurrentMu.Unlock()
	hInstance, _, _ := nativeGetModule.Call(0)
	hwnd, _, _ := nativeCreateWindow.Call(
		nativeWSExLayered|nativeWSExTopmost|nativeWSExToolWindow|nativeWSExNoActivate,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(aquariumClassName))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(ProductName+" aquarium"))),
		nativeWSPopup,
		uintptr(a.x), uintptr(a.y), uintptr(a.width), uintptr(a.height),
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		ready <- errors.New("could not create the aquarium window")
		return
	}
	a.hwnd = hwnd
	ready <- nil
	a.mu.Lock()
	wanted := a.wanted
	a.mu.Unlock()
	a.show(wanted)

	var message nativeMSG
	for {
		result, _, _ := nativeGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if result == 0 || int32(result) == -1 {
			break
		}
		nativeTranslate.Call(uintptr(unsafe.Pointer(&message)))
		nativeDispatch.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func aquariumWindowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	aquariumCurrentMu.Lock()
	a := aquariumCurrent
	aquariumCurrentMu.Unlock()
	if a != nil && a.hwnd == hwnd {
		switch message {
		case nativeWMTimer:
			a.tick()
			return 0
		case wmMouseMove:
			a.mouseMove(lParam)
			return 0
		case wmMouseLeave:
			a.tracking, a.pointerIn, a.pointerOut = false, false, time.Now()
			a.hover, a.hoverFish = "", ""
			return 0
		case wmLButtonUp:
			a.click(lParam)
			return 0
		case wmMouseActivate:
			return maNoActivate
		case wmSetCursor:
			shape := uintptr(idcArrow)
			if a.hover != "" {
				shape = idcHand
			}
			cursor, _, _ := aquariumLoadCursor.Call(0, shape)
			aquariumSetCursor.Call(cursor)
			return 1
		case aquariumShowMessage:
			a.show(wParam != 0)
			return 0
		case nativeWMClose:
			return 0
		case nativeWMDestroy:
			nativePostQuit.Call(0)
			return 0
		}
	}
	result, _, _ := nativeDefWindowProc.Call(hwnd, message, wParam, lParam)
	return result
}

// prepare sizes the window for the screen's scale: the art is drawn with
// whole pixels, two per art pixel at 100 %.
func (a *aquariumWindow) prepare() error {
	dpi := uintptr(96)
	if aquariumSystemDPI.Find() == nil {
		if value, _, _ := aquariumSystemDPI.Call(); value != 0 {
			dpi = value
		}
	}
	a.dpi = float64(dpi) / 96
	a.scale = max(2, int(math.Round(2*a.dpi)))
	a.width = a.px(aquariumEdge+aquariumPanelWidth+aquariumGap) + aquarium.BowlW*a.scale
	a.height = max(aquarium.BowlH*a.scale+a.px(aquariumTopRoom), a.px(aquariumMinHeight))
	a.bowl = image.Pt(a.width-aquarium.BowlW*a.scale, a.height-aquarium.BowlH*a.scale)
	a.place()

	screen, _, _ := aquariumGetDC.Call(0)
	if screen == 0 {
		return errors.New("could not reach the screen")
	}
	defer aquariumReleaseDC.Call(0, screen)
	a.dc, _, _ = nativeCreateCompatibleDC.Call(screen)
	a.textDC, _, _ = nativeCreateCompatibleDC.Call(screen)
	if a.dc == 0 || a.textDC == 0 {
		a.release()
		return errors.New("could not prepare the aquarium")
	}
	info := aquariumBitmapInfo{width: int32(a.width), height: -int32(a.height), planes: 1, bitCount: 32}
	info.size = uint32(unsafe.Sizeof(info))
	var bits unsafe.Pointer
	a.bitmap, _, _ = aquariumCreateDIBSection.Call(a.dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if a.bitmap == 0 || bits == nil {
		a.release()
		return errors.New("could not prepare the aquarium")
	}
	a.oldBitmap, _, _ = nativeSelectObject.Call(a.dc, a.bitmap)
	a.pixels = unsafe.Slice((*byte)(bits), a.width*a.height*4)
	a.frame = image.NewNRGBA(image.Rect(0, 0, a.width, a.height))
	a.art = image.NewNRGBA(image.Rect(0, 0, aquarium.BowlW, aquarium.BowlH))
	a.fonts = map[string]uintptr{
		"title":  a.font(14, 600),
		"body":   a.font(12.5, 400),
		"button": a.font(12.5, 600),
		"tag":    a.font(12, 700),
	}
	nativeSetBkMode.Call(a.textDC, nativeTransparent)
	nativeSetTextColor.Call(a.textDC, 0x00FFFFFF)
	return nil
}

func (a *aquariumWindow) px(value float64) int { return int(math.Round(value * a.dpi)) }

func (a *aquariumWindow) font(size float64, weight int) uintptr {
	font, _, _ := nativeCreateFont.Call(
		uintptr(int32(-a.px(size))), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 4, 0,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("Segoe UI"))),
	)
	return font
}

// place keeps the window above the clock, in the primary screen's corner.
func (a *aquariumWindow) place() {
	var work struct{ left, top, right, bottom int32 }
	if ok, _, _ := aquariumSystemInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&work)), 0); ok == 0 {
		width, _, _ := nativeGetMetrics.Call(nativeSMCXScreen)
		height, _, _ := nativeGetMetrics.Call(nativeSMCYScreen)
		work.right, work.bottom = int32(width), int32(height)
	}
	a.x = int(work.right) - a.width - a.px(8)
	a.y = int(work.bottom) - a.height - a.px(2)
}

func (a *aquariumWindow) release() {
	if a.oldBitmap != 0 {
		nativeSelectObject.Call(a.dc, a.oldBitmap)
	}
	if a.bitmap != 0 {
		nativeDeleteObject.Call(a.bitmap)
	}
	for _, font := range a.fonts {
		if font != 0 {
			nativeDeleteObject.Call(font)
		}
	}
	if a.dc != 0 {
		nativeDeleteDC.Call(a.dc)
	}
	if a.textDC != 0 {
		nativeDeleteDC.Call(a.textDC)
	}
}

func (a *aquariumWindow) show(wanted bool) {
	if !wanted {
		nativeKillTimer.Call(a.hwnd, nativeTimerID)
		nativeShowWindow.Call(a.hwnd, nativeSWHide)
		a.visible = false
		return
	}
	nativeSetTimer.Call(a.hwnd, nativeTimerID, uintptr(aquariumTick.Milliseconds()), 0)
	a.fullScreen = aquariumFullScreen()
	if !a.fullScreen {
		a.render()
		nativeShowWindow.Call(a.hwnd, nativeSWShowNoActivate)
		a.visible = true
	}
}

// A game, a video or a presentation in full screen hides the aquarium.
func aquariumFullScreen() bool {
	if aquariumNotification.Find() != nil {
		return false
	}
	var state uint32
	if result, _, _ := aquariumNotification.Call(uintptr(unsafe.Pointer(&state))); result != 0 {
		return false
	}
	return state == 2 || state == 3 || state == 4
}

func (a *aquariumWindow) tick() {
	a.ticks++
	if a.ticks%24 == 0 {
		a.place()
		if full := aquariumFullScreen(); full != a.fullScreen {
			a.fullScreen = full
			if full {
				nativeShowWindow.Call(a.hwnd, nativeSWHide)
				a.visible = false
			}
		}
	}
	if a.fullScreen {
		return
	}
	a.render()
	if !a.visible {
		nativeShowWindow.Call(a.hwnd, nativeSWShowNoActivate)
		a.visible = true
	}
}

func (a *aquariumWindow) render() {
	a.mu.Lock()
	accounts, version := a.accounts, a.version
	if p := a.panel; p != nil && p.kind == progressPanel {
		if p.doneAt.IsZero() {
			p.progress += (0.9 - p.progress) * 0.04
		} else if time.Since(p.doneAt) > 400*time.Millisecond {
			a.panel = nil
		}
	}
	var panel aquariumPanel
	open := a.panel != nil
	if open {
		panel = *a.panel
	}
	a.mu.Unlock()

	if version != a.synced {
		a.tank.Sync(aquariumWindows(accounts))
		a.synced = version
	}
	a.tank.Step(aquariumTick.Seconds())
	clear(a.frame.Pix)
	a.tank.Draw(a.art)
	if a.plusShown(open && panel.kind == plusPanel) {
		aquarium.DrawPlus(a.art, a.hover == "plus")
	}
	aquarium.DrawImage(a.frame, a.art, a.bowl.X, a.bowl.Y, a.scale)
	a.drawTag(accounts)
	a.buttons = a.buttons[:0]
	a.panelRect = image.Rectangle{}
	if open {
		a.drawPanel(panel, accounts)
	}
	a.present()
}

// The plus shows while the pointer is on the aquarium, a moment after it
// leaves, and while the list it opens is shown.
func (a *aquariumWindow) plusShown(listOpen bool) bool {
	return a.pointerIn || listOpen || time.Since(a.pointerOut) < 600*time.Millisecond
}

func aquariumWindows(accounts []aquariumAccount) []aquarium.Window {
	var windows []aquarium.Window
	for _, account := range accounts {
		if account.open {
			windows = append(windows, aquarium.Window{Key: account.key, Palette: account.palette, Main: account.main, Working: len(account.working)})
		}
	}
	return windows
}

func (a *aquariumWindow) present() {
	pix := a.frame.Pix
	for i := 0; i < len(pix); i += 4 {
		alpha := uint16(pix[i+3])
		a.pixels[i] = uint8(uint16(pix[i+2]) * alpha / 255)
		a.pixels[i+1] = uint8(uint16(pix[i+1]) * alpha / 255)
		a.pixels[i+2] = uint8(uint16(pix[i]) * alpha / 255)
		a.pixels[i+3] = pix[i+3]
	}
	aquariumGdiFlush.Call()
	screen, _, _ := aquariumGetDC.Call(0)
	defer aquariumReleaseDC.Call(0, screen)
	position := aquariumPoint{x: int32(a.x), y: int32(a.y)}
	size := aquariumSize{cx: int32(a.width), cy: int32(a.height)}
	origin := aquariumPoint{}
	blend := aquariumBlend{constantAlpha: 255, alphaFormat: 1}
	aquariumUpdateLayered.Call(a.hwnd, screen, uintptr(unsafe.Pointer(&position)), uintptr(unsafe.Pointer(&size)), a.dc, uintptr(unsafe.Pointer(&origin)), 0, uintptr(unsafe.Pointer(&blend)), 2)
}

// ---------- drawing helpers ----------

func (a *aquariumWindow) fill(r image.Rectangle, c color.NRGBA) {
	r = r.Intersect(a.frame.Rect)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			aquarium.Blend(a.frame, x, y, c)
		}
	}
}

// textMask draws the text in white on black with GDI; the brightness becomes
// the coverage. Masks are kept, since the same few texts come back each frame.
func (a *aquariumWindow) textMask(text, font string) *image.Alpha {
	handle := a.fonts[font]
	key := maskKey{text: text, font: handle}
	if mask, ok := a.masks[key]; ok {
		return mask
	}
	if len(a.masks) > 256 {
		clear(a.masks)
	}
	empty := image.NewAlpha(image.Rect(0, 0, 0, 0))
	if text == "" {
		return empty
	}
	utf := windows.StringToUTF16(text)
	previousFont, _, _ := nativeSelectObject.Call(a.textDC, handle)
	defer nativeSelectObject.Call(a.textDC, previousFont)
	var size aquariumSize
	aquariumTextExtent.Call(a.textDC, uintptr(unsafe.Pointer(&utf[0])), uintptr(len(utf)-1), uintptr(unsafe.Pointer(&size)))
	width, height := int(size.cx)+2, int(size.cy)+2
	info := aquariumBitmapInfo{width: int32(width), height: -int32(height), planes: 1, bitCount: 32}
	info.size = uint32(unsafe.Sizeof(info))
	var bits unsafe.Pointer
	bitmap, _, _ := aquariumCreateDIBSection.Call(a.textDC, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		return empty
	}
	defer nativeDeleteObject.Call(bitmap)
	previousBitmap, _, _ := nativeSelectObject.Call(a.textDC, bitmap)
	defer nativeSelectObject.Call(a.textDC, previousBitmap)
	pixels := unsafe.Slice((*byte)(bits), width*height*4)
	clear(pixels)
	aquariumTextOut.Call(a.textDC, 1, 1, uintptr(unsafe.Pointer(&utf[0])), uintptr(len(utf)-1))
	aquariumGdiFlush.Call()
	mask := image.NewAlpha(image.Rect(0, 0, width, height))
	for i := range width * height {
		mask.Pix[i] = max(pixels[i*4], pixels[i*4+1], pixels[i*4+2])
	}
	a.masks[key] = mask
	return mask
}

func (a *aquariumWindow) textWidth(text, font string) int {
	return max(0, a.textMask(text, font).Rect.Dx()-2)
}

func (a *aquariumWindow) text(text, font string, x, y int, c color.NRGBA) {
	mask := a.textMask(text, font)
	for my := 0; my < mask.Rect.Dy(); my++ {
		for mx := 0; mx < mask.Rect.Dx(); mx++ {
			if coverage := mask.Pix[my*mask.Stride+mx]; coverage != 0 {
				aquarium.Blend(a.frame, x+mx-1, y+my-1, color.NRGBA{R: c.R, G: c.G, B: c.B, A: uint8(uint16(c.A) * uint16(coverage) / 255)})
			}
		}
	}
}

// fit shortens the text with an ellipsis until it is at most width wide.
func (a *aquariumWindow) fit(text, font string, width int) string {
	if a.textWidth(text, font) <= width {
		return text
	}
	runes := []rune(text)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		if candidate := strings.TrimSpace(string(runes)) + "…"; a.textWidth(candidate, font) <= width {
			return candidate
		}
	}
	return "…"
}

func (a *aquariumWindow) icon(name string, x, y int, c color.NRGBA) (int, int) {
	rows := aquariumIcons[name]
	size := max(1, a.px(2))
	for row, line := range rows {
		for column, ch := range line {
			if ch == 'x' {
				a.fill(image.Rect(x+column*size, y+row*size, x+(column+1)*size, y+(row+1)*size), c)
			}
		}
	}
	return len(rows[0]) * size, len(rows) * size
}

func (a *aquariumWindow) miniFish(account aquariumAccount, x, y int) int {
	scale := max(1, a.scale/2)
	aquarium.DrawImage(a.frame, aquarium.Sprite(account.palette, account.main, 0, 1), x, y, scale)
	return aquarium.SpriteW * scale
}

func (a *aquariumWindow) button(label, iconName, action, key string, x, y int, small bool) int {
	pad, height := a.px(9), a.px(26)
	if small {
		pad, height = a.px(6), a.px(24)
	}
	iconWidth := len(aquariumIcons[iconName][0]) * max(1, a.px(2))
	width := pad + iconWidth + a.px(6) + a.textWidth(label, "button") + pad
	rect := image.Rect(x, y, x+width, y+height)
	fill := panelBorder
	if a.hover == "button:"+action+":"+key {
		fill = buttonHover
	}
	a.fill(image.Rect(rect.Min.X, rect.Max.Y, rect.Max.X, rect.Max.Y+a.px(2)), panelShadow)
	a.fill(rect, fill)
	iconHeight := len(aquariumIcons[iconName]) * max(1, a.px(2))
	a.icon(iconName, x+pad, y+(height-iconHeight)/2, iconWhite)
	textHeight := a.textMask(label, "button").Rect.Dy() - 2
	a.text(label, "button", x+pad+iconWidth+a.px(6), y+(height-textHeight)/2, iconWhite)
	a.buttons = append(a.buttons, aquariumButton{rect: rect, action: action, key: key})
	return width
}

func shortAccountName(label string) string {
	if at := strings.Index(label, "@"); at > 0 {
		return label[:at+1]
	}
	return label
}

func findAquariumAccount(accounts []aquariumAccount, key string) (aquariumAccount, bool) {
	for _, account := range accounts {
		if account.key == key {
			return account, true
		}
	}
	return aquariumAccount{}, false
}

func (a *aquariumWindow) drawTag(accounts []aquariumAccount) {
	if a.hoverFish == "" {
		return
	}
	for _, fish := range a.tank.Fish {
		if fish.Key != a.hoverFish || fish.Leaving() {
			continue
		}
		account, ok := findAquariumAccount(accounts, fish.Key)
		if !ok {
			return
		}
		label := shortAccountName(account.label)
		tx, ty := fish.TagPoint()
		width := a.textWidth(label, "tag") + a.px(12)
		height := a.textMask(label, "tag").Rect.Dy() - 2 + a.px(4)
		x := min(max(a.bowl.X+tx*a.scale-width/2, a.px(2)), a.width-width-a.px(2))
		y := max(a.bowl.Y+ty*a.scale-height, a.px(2))
		border := a.px(2)
		a.fill(image.Rect(x-border, y-border, x+width+border, y+height+border), tagInk)
		a.fill(image.Rect(x, y, x+width, y+height), account.palette.Tag)
		a.text(label, "tag", x+a.px(6), y+a.px(2), tagInk)
		return
	}
}

// ---------- the panel ----------

type panelLine struct {
	text  string
	color color.NRGBA
}

func (a *aquariumWindow) drawPanel(panel aquariumPanel, accounts []aquariumAccount) {
	width := a.px(aquariumPanelWidth)
	pad := a.px(12)
	inner := width - 2*pad
	account, _ := findAquariumAccount(accounts, panel.key)
	short := shortAccountName(account.label)

	var who string
	var lines []panelLine
	switch panel.kind {
	case fishPanel:
		who = account.label
		place := "separate window"
		if account.main {
			place = "main window"
		}
		switch {
		case panel.note != "":
			lines = append(lines, panelLine{panel.note, panelAccent})
		case len(account.working) > 0:
			lines = append(lines, panelLine{place + " · " + strconv.Itoa(len(account.working)) + " working", panelAccent})
		default:
			lines = append(lines, panelLine{place + " · idle", panelMuted})
		}
	case confirmPanel:
		work := account.working
		if panel.action == "main" {
			who = "switch to " + short + "?"
			lines = append(lines, panelLine{"The main window reopens on " + short + ".", panelMuted})
			work = nil
			for _, other := range accounts {
				if other.main {
					work = other.working
				}
			}
		} else {
			who = "close " + short + "?"
		}
		if len(work) == 0 {
			lines = append(lines, panelLine{"Nothing is working right now.", panelMuted})
		} else {
			lines = append(lines, panelLine{strconv.Itoa(len(work)) + " working and will stop:", panelAccent})
			for i, title := range work {
				if i == aquariumMaxListed {
					lines = append(lines, panelLine{"· and " + strconv.Itoa(len(work)-i) + " more", panelMuted})
					break
				}
				lines = append(lines, panelLine{a.fit("· "+title, "body", inner), panelText})
			}
		}
	case plusPanel:
		lines = append(lines, panelLine{"accounts without a window", panelMuted})
	case progressPanel:
		who = panel.title
	}

	lineHeight := a.textMask("Ag", "body").Rect.Dy() + a.px(2)
	whoHeight := 0
	if who != "" {
		whoHeight = max(aquarium.SpriteH*max(1, a.scale/2), a.textMask("Ag", "title").Rect.Dy()) + a.px(6)
	}
	var closed []aquariumAccount
	for _, other := range accounts {
		if !other.open && other.key != "" {
			closed = append(closed, other)
		}
	}
	rowHeight := max(aquarium.SpriteH*max(1, a.scale/2), a.px(26)) + a.px(6)
	height := pad + whoHeight + len(lines)*lineHeight + pad
	switch panel.kind {
	case fishPanel, confirmPanel:
		height += a.px(8) + a.px(28)
	case plusPanel:
		if len(closed) == 0 {
			height += lineHeight
		}
		height += len(closed) * rowHeight
	case progressPanel:
		height += a.px(24)
	}

	right := a.bowl.X - a.px(aquariumGap)
	bottom := a.height - a.px(6)
	rect := image.Rect(right-width, bottom-height, right, bottom)
	a.panelRect = rect
	outer, edge := a.px(2), a.px(2)
	a.fill(rect.Inset(-(outer + edge)), panelShadow)
	a.fill(rect.Inset(-edge), panelBorder)
	a.fill(rect, panelFill)

	x, y := rect.Min.X+pad, rect.Min.Y+pad
	if who != "" {
		textX := x
		if panel.kind != progressPanel {
			textX += a.miniFish(account, x, y-a.px(2)) + a.px(8)
		}
		title := a.fit(who, "title", rect.Max.X-pad-textX)
		titleHeight := a.textMask(title, "title").Rect.Dy() - 2
		a.text(title, "title", textX, y+(whoHeight-a.px(6)-titleHeight)/2, panelText)
		y += whoHeight
	}
	for _, line := range lines {
		a.text(line.text, "body", x, y, line.color)
		y += lineHeight
	}
	switch panel.kind {
	case fishPanel:
		y += a.px(8)
		x += a.button("show", "eye", "show", panel.key, x, y, false) + a.px(8)
		a.button("close", "close", "close", panel.key, x, y, false)
	case confirmPanel:
		y += a.px(8)
		label := "close"
		if panel.action == "main" {
			label = "switch"
		}
		x += a.button(label, "check", "yes-"+panel.action, panel.key, x, y, false) + a.px(8)
		a.button("cancel", "close", "cancel", panel.key, x, y, false)
	case plusPanel:
		if len(closed) == 0 {
			a.text("every account is open", "body", x, y, panelMuted)
		}
		for _, other := range closed {
			rowX := x + a.miniFish(other, x, y) + a.px(6)
			mainWidth := a.px(12) + len(aquariumIcons["crown"][0])*max(1, a.px(2)) + a.px(6) + a.textWidth("main", "button")
			openWidth := a.px(12) + len(aquariumIcons["plus"][0])*max(1, a.px(2)) + a.px(6) + a.textWidth("open", "button")
			nameWidth := rect.Max.X - pad - rowX - openWidth - mainWidth - a.px(12)
			name := a.fit(shortAccountName(other.label), "body", nameWidth)
			buttonY := y + (rowHeight-a.px(6)-a.px(24))/2
			a.text(name, "body", rowX, buttonY+a.px(4), panelText)
			buttonX := rect.Max.X - pad - openWidth - a.px(6) - mainWidth
			buttonX += a.button("open", "plus", "open", other.key, buttonX, buttonY, true) + a.px(6)
			a.button("main", "crown", "main", other.key, buttonX, buttonY, true)
			y += rowHeight
		}
	case progressPanel:
		y += a.px(6)
		bar := image.Rect(x, y, x+inner, y+a.px(10))
		a.fill(bar.Inset(-a.px(2)), panelShadow)
		a.fill(bar, panelBorder)
		steps := int(math.Round(panel.progress * 20))
		filled := bar.Min.X + bar.Dx()*steps/20
		stripe := a.px(8)
		for sx := bar.Min.X; sx < filled; sx++ {
			c := buttonHover
			if (sx-bar.Min.X)%stripe >= stripe*3/4 {
				c = barStripe
			}
			a.fill(image.Rect(sx, bar.Min.Y, sx+1, bar.Max.Y), c)
		}
	}
}

// ---------- the pointer ----------

func pointerPosition(lParam uintptr) image.Point {
	return image.Pt(int(int16(lParam&0xFFFF)), int(int16(lParam>>16&0xFFFF)))
}

type trackMouse struct {
	size      uint32
	flags     uint32
	hwnd      uintptr
	hoverTime uint32
}

func (a *aquariumWindow) mouseMove(lParam uintptr) {
	if !a.tracking {
		track := trackMouse{flags: tmeLeave, hwnd: a.hwnd}
		track.size = uint32(unsafe.Sizeof(track))
		aquariumTrackMouse.Call(uintptr(unsafe.Pointer(&track)))
		a.tracking = true
	}
	a.pointerIn = true
	a.hover, a.hoverFish = a.hitTest(pointerPosition(lParam))
}

func floorDiv(a, b int) int {
	if a < 0 {
		return -((-a + b - 1) / b)
	}
	return a / b
}

func (a *aquariumWindow) hitTest(p image.Point) (string, string) {
	for _, b := range a.buttons {
		if p.In(b.rect) {
			return "button:" + b.action + ":" + b.key, ""
		}
	}
	art := image.Pt(floorDiv(p.X-a.bowl.X, a.scale), floorDiv(p.Y-a.bowl.Y, a.scale))
	if art.In(aquarium.PlusButton) && a.plusShown(false) {
		return "plus", ""
	}
	if fish := a.tank.FishAt(art.X, art.Y); fish != nil {
		return "fish:" + fish.Key, fish.Key
	}
	return "", ""
}

func (a *aquariumWindow) click(lParam uintptr) {
	p := pointerPosition(lParam)
	target, fishKey := a.hitTest(p)
	switch {
	case target == "plus":
		a.togglePanel(aquariumPanel{kind: plusPanel})
	case strings.HasPrefix(target, "fish:"):
		a.togglePanel(aquariumPanel{kind: fishPanel, key: fishKey})
	case strings.HasPrefix(target, "button:"):
		parts := strings.SplitN(target, ":", 3)
		a.press(parts[1], parts[2])
	case p.In(a.panelRect):
	default:
		a.setPanel(nil)
	}
}

func (a *aquariumWindow) togglePanel(panel aquariumPanel) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.panel != nil && a.panel.kind == progressPanel {
		return
	}
	if a.panel != nil && a.panel.kind == panel.kind && a.panel.key == panel.key {
		a.panel = nil
		return
	}
	a.panel = &panel
}

func (a *aquariumWindow) setPanel(panel *aquariumPanel) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.panel != nil && a.panel.kind == progressPanel && a.panel.doneAt.IsZero() {
		return
	}
	a.panel = panel
}

func (a *aquariumWindow) account(key string) (aquariumAccount, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return findAquariumAccount(a.accounts, key)
}

func (a *aquariumWindow) press(action, key string) {
	account, ok := a.account(key)
	if !ok {
		a.setPanel(nil)
		return
	}
	short := shortAccountName(account.label)
	switch action {
	case "show":
		for _, fish := range a.tank.Fish {
			if fish.Key == key {
				fish.Hop()
			}
		}
		a.setPanel(&aquariumPanel{kind: fishPanel, key: key, note: short + " is in front now"})
		go a.tray.showAccountWindow(account)
	case "close":
		if len(account.working) > 0 {
			a.setPanel(&aquariumPanel{kind: confirmPanel, key: key, action: "close"})
			return
		}
		a.start("closing "+short, func() { a.tray.closeAccountWindow(account) })
	case "yes-close":
		a.start("closing "+short, func() { a.tray.closeAccountWindow(account) })
	case "open":
		a.start("opening "+short, func() { a.tray.openSeparateWindowWith(account.key, true) })
	case "main":
		a.setPanel(&aquariumPanel{kind: confirmPanel, key: key, action: "main"})
	case "yes-main":
		a.setPanel(nil)
		go a.tray.switchTo(account.key, false)
	case "cancel":
		a.setPanel(nil)
	}
}

// start shows a progress panel while work runs; the fish follow once the tray
// sees the windows change.
func (a *aquariumWindow) start(title string, work func()) {
	a.mu.Lock()
	a.panel = &aquariumPanel{kind: progressPanel, title: title, progress: 0.08}
	a.mu.Unlock()
	go func() {
		work()
		a.mu.Lock()
		if a.panel != nil && a.panel.kind == progressPanel {
			a.panel.progress, a.panel.doneAt = 1, time.Now()
		}
		a.mu.Unlock()
	}()
}

// ---------- the tray's side ----------

func (s *trayState) handleAquariumItem() {
	for range s.aquariumItem.ClickedCh {
		wanted := !s.aquariumItem.Checked()
		if wanted {
			s.aquariumItem.Check()
		} else {
			s.aquariumItem.Uncheck()
		}
		if err := saveAquariumWanted(wanted); err != nil {
			s.setStatus("Could not save the aquarium setting: " + err.Error())
		}
		s.aquarium.setWanted(wanted)
	}
}

// aquariumAccountsLocked describes every saved account for the aquarium: its
// colour, its window and the Claude Code sessions working there.
func (s *trayState) aquariumAccountsLocked(profiles []profile.Meta, active string, running bool) []aquariumAccount {
	if s.aquarium == nil {
		return nil
	}
	home, _ := os.UserHomeDir()
	claudeDir := filepath.Join(home, ".claude")
	appData, _ := s.platform.AppDataPath()
	accounts := make([]aquariumAccount, 0, len(profiles)+1)
	for i, meta := range profiles {
		account := aquariumAccount{key: meta.Name, label: s.profileLabels[meta.Name], palette: aquarium.Palettes[i%len(aquarium.Palettes)]}
		if account.label == "" {
			account.label = meta.Name
		}
		switch {
		case running && active == meta.Name:
			account.open, account.main = true, true
			account.working = sessions.Working(claudeDir, appData)
		case s.separateShown[meta.Name].open:
			account.open = true
			if folder, err := separateWindowFolder(meta.Name); err == nil {
				account.working = sessions.Working(claudeDir, folder)
			}
		}
		accounts = append(accounts, account)
	}
	if running && active == "" {
		accounts = append(accounts, aquariumAccount{key: unknownMainKey, label: "Main window", palette: aquarium.Unknown, open: true, main: true, working: sessions.Working(claudeDir, appData)})
	}
	return accounts
}

func (s *trayState) showAccountWindow(account aquariumAccount) {
	if account.main {
		if shower, ok := s.platform.(interface{ ShowMainWindow() error }); ok {
			if err := shower.ShowMainWindow(); err != nil {
				s.setStatus("Could not show the main window: " + err.Error())
			}
		}
		return
	}
	runner, ok := s.platform.(platform.SeparateWindowRunner)
	folder, err := separateWindowFolder(account.key)
	if !ok || err != nil {
		return
	}
	if err := runner.OpenSeparateWindow(folder, ""); err != nil {
		s.setStatus("Could not show the separate window: " + err.Error())
	}
}

// closeAccountWindow closes an account's window after the aquarium asked.
func (s *trayState) closeAccountWindow(account aquariumAccount) {
	if !account.main {
		s.closeSeparateWindowNow(account.key, true)
		return
	}
	if !s.beginSwitch() {
		s.setStatus("Wait for the current operation to finish")
		return
	}
	s.toggleClaudeDesktop(true)
}

// ---------- the setting ----------

func aquariumSettingPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude-swap", "aquarium-off"), nil
}

// aquariumWanted is true unless the aquarium was turned off from the menu.
func aquariumWanted() bool {
	path, err := aquariumSettingPath()
	if err != nil {
		return true
	}
	_, err = os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

func saveAquariumWanted(wanted bool) error {
	path, err := aquariumSettingPath()
	if err != nil {
		return err
	}
	if wanted {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0o600)
}
