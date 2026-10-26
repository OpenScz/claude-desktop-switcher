//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/TinoA/claude-desktop-switcher/internal/winproc"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	windowsPackageFamily   = "Claude_pzs8sxrjxfjjc"
	windowsAUMID           = windowsPackageFamily + "!Claude"
	processName            = "Claude.exe"
	processPollInterval    = 100 * time.Millisecond
	processPolls           = 100
	processStablePolls     = 3
	installCacheTTL        = 5 * time.Minute
	mainWindowFocusTimeout = 8 * time.Second
	windowsSWShow          = 5
	windowsSWRestore       = 9
	windowsWMClose         = 0x0010
	gracefulCloseTimeout   = 2 * time.Second
	forcedCloseTimeout     = 3 * time.Second
	finalCloseGraceTimeout = 5 * time.Second
)

type windowsInstallKind uint8

const (
	installUnknown windowsInstallKind = iota
	installSquirrel
	installMSIX
	installPortable
	installWin32
)

type windowsPlatform struct {
	root         string
	executable   string
	processRoot  string
	kind         windowsInstallKind
	msix         bool
	alternatives []windowsInstall
}

type windowsInstall struct {
	root        string
	executable  string
	processRoot string
	kind        windowsInstallKind
	msix        bool
	id          string
}

func current() Platform {
	selected, alternatives := detectWindowsInstallCached()
	return &windowsPlatform{
		root:         selected.root,
		executable:   selected.executable,
		processRoot:  selected.processRoot,
		kind:         selected.kind,
		msix:         selected.msix,
		alternatives: alternatives,
	}
}

func (w *windowsPlatform) AppDataPath() (string, error) {
	if w.root == "" {
		return "", fmt.Errorf("claude desktop data directory was not detected")
	}
	return w.root, nil
}

func (w *windowsPlatform) IsInstalled() bool { return w.root != "" }

func (w *windowsPlatform) LaunchPath() string { return w.executable }

func (w *windowsPlatform) IsRunning() (bool, error) {
	for _, alternative := range w.alternatives {
		if alternative.root != w.root && candidateHasProcess(alternative) {
			return false, errors.New("multiple Claude Desktop installations are running; close the other installation first")
		}
	}
	paths, err := w.desktopProcessPaths()
	return len(paths) > 0, err
}

func (w *windowsPlatform) KillApp() error {
	if hwnd, _ := w.mainWindow(); hwnd != 0 {
		windowsPostMessage.Call(hwnd, windowsWMClose, 0, 0)
	}
	return stopWindowsProcesses(w.waitForExit, w.taskkillOwned, w.forceUntilExit)
}

func stopWindowsProcesses(
	waitForExit func(time.Duration) (bool, error),
	taskkill func(bool),
	forceUntilExit func(time.Duration) (bool, error),
) error {
	if exited, err := waitForExit(gracefulCloseTimeout); err != nil {
		return err
	} else if exited {
		return nil
	}
	taskkill(false)
	if exited, err := waitForExit(gracefulCloseTimeout); err != nil {
		return err
	} else if exited {
		return nil
	}
	if exited, err := forceUntilExit(forcedCloseTimeout); err != nil {
		return err
	} else if exited {
		return nil
	}
	// Electron children can disappear moments after taskkill returns.
	if exited, err := waitForExit(finalCloseGraceTimeout); err != nil {
		return err
	} else if exited {
		return nil
	}
	return errors.New("claude desktop processes did not exit before timeout")
}

func (w *windowsPlatform) forceUntilExit(timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		paths, err := w.desktopProcessPaths()
		if err != nil {
			return false, err
		}
		if len(paths) == 0 {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		w.taskkillOwned(true)
		time.Sleep(250 * time.Millisecond)
	}
}

func (w *windowsPlatform) taskkillOwned(force bool) {
	roots, err := w.desktopProcessRoots()
	if err != nil {
		return
	}
	for _, pid := range roots {
		args := []string{"/PID", strconv.Itoa(pid), "/T"}
		if force {
			args = append(args, "/F")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = winproc.CommandContext(ctx, "taskkill.exe", args...).Run()
		cancel()
	}
}

func (w *windowsPlatform) waitForExit(timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		paths, err := w.desktopProcessPaths()
		if err != nil {
			return false, err
		}
		if len(paths) == 0 {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(processPollInterval)
	}
}

func (w *windowsPlatform) LaunchApp() error {
	var err error
	if w.msix {
		verb, _ := windows.UTF16PtrFromString("open")
		target, _ := windows.UTF16PtrFromString("shell:AppsFolder\\" + windowsAUMID)
		err = windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
	} else if w.executable == "" {
		err = errors.New("claude desktop executable was not detected")
	} else {
		err = exec.Command(w.executable).Start()
	}
	if err != nil {
		return err
	}
	stable := 0
	for range processPolls {
		paths, pollErr := w.desktopProcessPaths()
		if pollErr != nil {
			return pollErr
		}
		if len(paths) > 0 {
			stable++
			if stable >= processStablePolls {
				w.focusMainWindow(mainWindowFocusTimeout)
				return nil
			}
		} else {
			stable = 0
		}
		time.Sleep(processPollInterval)
	}
	return errors.New("claude desktop did not start before timeout")
}

func (w *windowsPlatform) WaitForLoginWindow(ctx context.Context) error {
	for {
		if w.hasMainWindow() {
			return nil
		}
		timer := time.NewTimer(processPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (w *windowsPlatform) hasMainWindow() bool {
	found, err := w.mainWindow()
	if err != nil || found == 0 {
		return false
	}
	focusWindow(found)
	return true
}

func (w *windowsPlatform) focusMainWindow(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if w.hasMainWindow() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(processPollInterval)
	}
}

func focusWindow(hwnd uintptr) {
	minimized, _, _ := windowsIsIconic.Call(hwnd)
	show := showCommand(minimized != 0)
	windowsShowWindow.Call(hwnd, show)
	windowsBringWindowToTop.Call(hwnd)
	if focused, _, _ := windowsSetForegroundWindow.Call(hwnd); focused != 0 {
		return
	}
	currentThread, _, _ := windowsGetCurrentThreadID.Call()
	targetThread, _, _ := windowsGetWindowProcessID.Call(hwnd, 0)
	foreground, _, _ := windowsGetForegroundWindow.Call()
	foregroundThread := uintptr(0)
	if foreground != 0 {
		foregroundThread, _, _ = windowsGetWindowProcessID.Call(foreground, 0)
	}
	attachedTarget := currentThread != 0 && targetThread != 0 && currentThread != targetThread
	attachedForeground := currentThread != 0 && foregroundThread != 0 && currentThread != foregroundThread && foregroundThread != targetThread
	if attachedTarget {
		windowsAttachThreadInput.Call(currentThread, targetThread, 1)
		defer windowsAttachThreadInput.Call(currentThread, targetThread, 0)
	}
	if attachedForeground {
		windowsAttachThreadInput.Call(currentThread, foregroundThread, 1)
		defer windowsAttachThreadInput.Call(currentThread, foregroundThread, 0)
	}
	windowsShowWindow.Call(hwnd, show)
	windowsBringWindowToTop.Call(hwnd)
	windowsSetForegroundWindow.Call(hwnd)
	windowsSetFocus.Call(hwnd)
}

// SW_RESTORE also takes a maximized window out of its maximized size, so it is
// used only for a minimized window.
func showCommand(minimized bool) uintptr {
	if minimized {
		return windowsSWRestore
	}
	return windowsSWShow
}

func (w *windowsPlatform) LoginWindowVisible() (bool, error) {
	found, err := w.mainWindow()
	return found != 0, err
}

func (w *windowsPlatform) mainWindow() (uintptr, error) {
	pids, err := w.desktopProcessPaths()
	if err != nil || len(pids) == 0 {
		return 0, err
	}
	owned := make(map[int]bool, len(pids))
	for _, pid := range pids {
		owned[pid] = true
	}
	var found uintptr
	enumTopLevelWindows(func(hwnd uintptr) bool {
		var pid uint32
		windowsGetWindowProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if !owned[int(pid)] {
			return true
		}
		visible, _, _ := windowsIsWindowVisible.Call(hwnd)
		if visible == 0 {
			return true
		}
		length, _, _ := windowsGetWindowTextLength.Call(hwnd)
		if length > 0 {
			found = hwnd
			return false
		}
		return true
	})
	return found, nil
}

var topLevelWindows struct {
	sync.Mutex
	visit func(uintptr) bool
}

// Go never frees a Windows callback and stops the whole process after 2000 of
// them, so every enumeration shares this one instead of creating its own.
var topLevelWindowsCallback = windows.NewCallback(func(hwnd, _ uintptr) uintptr {
	if topLevelWindows.visit(hwnd) {
		return 1
	}
	return 0
})

func enumTopLevelWindows(visit func(hwnd uintptr) bool) {
	topLevelWindows.Lock()
	defer topLevelWindows.Unlock()
	topLevelWindows.visit = visit
	windowsEnumWindows.Call(topLevelWindowsCallback, 0)
	topLevelWindows.visit = nil
}

var installCache struct {
	sync.Mutex
	checkedAt    time.Time
	selected     windowsInstall
	alternatives []windowsInstall
}

var windowsMainWindowAPI = windows.NewLazySystemDLL("user32.dll")
var windowsKernel32 = windows.NewLazySystemDLL("kernel32.dll")

var (
	windowsEnumWindows         = windowsMainWindowAPI.NewProc("EnumWindows")
	windowsGetWindowProcessID  = windowsMainWindowAPI.NewProc("GetWindowThreadProcessId")
	windowsIsWindowVisible     = windowsMainWindowAPI.NewProc("IsWindowVisible")
	windowsGetWindowTextLength = windowsMainWindowAPI.NewProc("GetWindowTextLengthW")
	windowsShowWindow          = windowsMainWindowAPI.NewProc("ShowWindow")
	windowsBringWindowToTop    = windowsMainWindowAPI.NewProc("BringWindowToTop")
	windowsSetForegroundWindow = windowsMainWindowAPI.NewProc("SetForegroundWindow")
	windowsGetForegroundWindow = windowsMainWindowAPI.NewProc("GetForegroundWindow")
	windowsAttachThreadInput   = windowsMainWindowAPI.NewProc("AttachThreadInput")
	windowsSetFocus            = windowsMainWindowAPI.NewProc("SetFocus")
	windowsPostMessage         = windowsMainWindowAPI.NewProc("PostMessageW")
	windowsIsIconic            = windowsMainWindowAPI.NewProc("IsIconic")
	windowsGetCurrentThreadID  = windowsKernel32.NewProc("GetCurrentThreadId")
)

func detectWindowsInstallCached() (windowsInstall, []windowsInstall) {
	installCache.Lock()
	defer installCache.Unlock()
	if !installCache.checkedAt.IsZero() && time.Since(installCache.checkedAt) < installCacheTTL {
		return installCache.selected, append([]windowsInstall(nil), installCache.alternatives...)
	}
	selected, alternatives := detectWindowsInstall()
	installCache.checkedAt = time.Now()
	installCache.selected = selected
	installCache.alternatives = append([]windowsInstall(nil), alternatives...)
	return selected, alternatives
}

func (w *windowsPlatform) desktopProcessPaths() ([]int, error) {
	processes, err := w.desktopProcesses()
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(processes))
	for _, process := range processes {
		pids = append(pids, process.pid)
	}
	return pids, nil
}

func (w *windowsPlatform) desktopProcessRoots() ([]int, error) {
	processes, err := w.desktopProcesses()
	if err != nil {
		return nil, err
	}
	matched := make(map[int]windowsProcess, len(processes))
	for _, process := range processes {
		matched[process.pid] = process
	}
	roots := make([]int, 0, len(processes))
	for _, process := range processes {
		if _, isChild := matched[process.parent]; !isChild {
			roots = append(roots, process.pid)
		}
	}
	if len(roots) == 0 && len(processes) > 0 {
		roots = append(roots, processes[0].pid)
	}
	return roots, nil
}

type windowsProcess struct {
	pid    int
	parent int
}

func (w *windowsPlatform) desktopProcesses() ([]windowsProcess, error) {
	if w.executable == "" && !w.msix {
		return nil, nil
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	var processes []windowsProcess
	for {
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), processName) {
			pid := int(entry.ProcessID)
			if w.ownsProcess(processImagePath(uint32(pid))) {
				processes = append(processes, windowsProcess{pid: pid, parent: int(entry.ParentProcessID)})
			}
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, err
		}
	}
	return processes, nil
}

func (w *windowsPlatform) ownsProcess(path string) bool {
	selected := windowsInstall{
		root:        w.root,
		executable:  w.executable,
		processRoot: w.processRoot,
		kind:        w.kind,
		msix:        w.msix,
	}
	if selected.ownsProcess(path) {
		return true
	}
	for _, alternative := range w.alternatives {
		if alternative.root == w.root && alternative.ownsProcess(path) {
			return true
		}
	}
	return false
}

func (i windowsInstall) ownsProcess(path string) bool {
	if path == "" {
		return false
	}
	path = normalizeWindowsPath(path)
	launch := normalizeWindowsPath(i.executable)
	root := normalizeWindowsPath(i.processRoot)
	switch i.kind {
	case installSquirrel:
		if path == launch {
			return true
		}
		if !pathWithin(path, root) || !strings.EqualFold(filepath.Base(path), processName) {
			return false
		}
		return strings.HasPrefix(strings.ToLower(filepath.Base(filepath.Dir(path))), "app-")
	case installMSIX:
		if !strings.EqualFold(filepath.Base(path), processName) {
			return false
		}
		for directory := filepath.Dir(path); directory != filepath.Dir(directory); directory = filepath.Dir(directory) {
			name := strings.ToLower(filepath.Base(directory))
			if strings.HasPrefix(name, "claude_") && strings.HasSuffix(name, "__"+windowsPackageFamily[strings.LastIndex(windowsPackageFamily, "_")+1:]) {
				return true
			}
		}
		return false
	case installPortable, installWin32:
		return path == launch
	default:
		return false
	}
}

func pathWithin(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func processImagePath(pid uint32) string {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(process)
	buffer := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buffer[:size])
}

func detectWindowsInstall() (windowsInstall, []windowsInstall) {
	local := os.Getenv("LOCALAPPDATA")
	appData := os.Getenv("APPDATA")
	msixRoot := filepath.Join(local, "Packages", windowsPackageFamily, "LocalCache", "Roaming", "Claude")
	if _, err := os.Stat(msixRoot); errors.Is(err, os.ErrNotExist) {
		roamingRoot := filepath.Join(appData, "Claude")
		if info, err := os.Stat(roamingRoot); err == nil && info.IsDir() {
			msixRoot = roamingRoot
		}
	}
	candidates := make([]windowsInstall, 0, 6)
	if registered, ok := detectRegisteredWin32(appData); ok {
		candidates = append(candidates, registered)
	}
	squirrelRoot := filepath.Join(local, "AnthropicClaude")
	if squirrelExe := squirrelExecutable(squirrelRoot); squirrelExe != "" {
		candidates = appendInstallCandidate(candidates, windowsInstall{
			id:          "squirrel:" + normalizeWindowsPath(squirrelRoot),
			root:        filepath.Join(appData, "Claude"),
			executable:  squirrelExe,
			processRoot: squirrelRoot,
			kind:        installSquirrel,
		})
	}
	msixPackageDir := filepath.Join(local, "Packages", windowsPackageFamily)
	if info, err := os.Stat(msixPackageDir); err == nil && info.IsDir() {
		candidates = appendInstallCandidate(candidates, windowsInstall{
			id:          "msix:" + windowsPackageFamily,
			root:        msixRoot,
			processRoot: filepath.Join(os.Getenv("ProgramFiles"), "WindowsApps"),
			kind:        installMSIX,
			msix:        true,
		})
	}
	home, _ := os.UserHomeDir()
	for _, exe := range []string{
		filepath.Join(local, "ClaudeChatOnly", "app", "claude.exe"),
		filepath.Join(home, "ClaudeChatOnly", "app", "claude.exe"),
	} {
		if _, err := os.Stat(exe); err == nil {
			candidates = appendInstallCandidate(candidates, windowsInstall{
				id:          "portable:" + normalizeWindowsPath(exe),
				root:        filepath.Join(appData, "Claude"),
				executable:  exe,
				processRoot: exe,
				kind:        installPortable,
			})
		}
	}
	for _, candidate := range []string{
		filepath.Join(appData, "Claude"),
		filepath.Join(local, "Claude"),
		filepath.Join(local, "Programs", "Claude"),
		filepath.Join(os.Getenv("ProgramFiles"), "Claude"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Claude"),
	} {
		exe := filepath.Join(candidate, processName)
		if _, err := os.Stat(exe); err == nil {
			candidates = appendInstallCandidate(candidates, windowsInstall{
				id:          "win32:" + normalizeWindowsPath(exe),
				root:        candidate,
				executable:  exe,
				processRoot: candidate,
				kind:        installWin32,
			})
		}
	}
	if len(candidates) == 0 {
		return windowsInstall{}, nil
	}
	selectedIndex := chooseInstallCandidate(candidates)
	selected := candidates[selectedIndex]
	alternatives := make([]windowsInstall, 0, len(candidates)-1)
	for index, candidate := range candidates {
		if index != selectedIndex {
			alternatives = append(alternatives, candidate)
		}
	}
	return selected, alternatives
}

func appendInstallCandidate(candidates []windowsInstall, candidate windowsInstall) []windowsInstall {
	for _, existing := range candidates {
		if existing.id == candidate.id || normalizeWindowsPath(existing.executable) == normalizeWindowsPath(candidate.executable) {
			return candidates
		}
	}
	return append(candidates, candidate)
}

func chooseInstallCandidate(candidates []windowsInstall) int {
	selected := -1
	for index, candidate := range candidates {
		if !candidateHasProcess(candidate) {
			continue
		}
		if selected == -1 || installPriority(candidate.kind) < installPriority(candidates[selected].kind) {
			selected = index
		}
	}
	if selected >= 0 {
		return selected
	}
	for index := range candidates {
		if selected == -1 || installPriority(candidates[index].kind) < installPriority(candidates[selected].kind) {
			selected = index
		}
	}
	return selected
}

func installPriority(kind windowsInstallKind) int {
	switch kind {
	case installSquirrel:
		return 0
	case installMSIX:
		return 1
	case installWin32:
		return 2
	case installPortable:
		return 3
	default:
		return 4
	}
}

func candidateHasProcess(candidate windowsInstall) bool {
	if candidate.executable == "" && !candidate.msix {
		return false
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return false
	}
	for {
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), processName) && candidate.ownsProcess(processImagePath(entry.ProcessID)) {
			return true
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return false
		}
	}
}

func detectRegisteredWin32(appData string) (windowsInstall, bool) {
	locations := []struct {
		hive   registry.Key
		access uint32
	}{
		{registry.CURRENT_USER, 0},
		{registry.LOCAL_MACHINE, 0},
		{registry.LOCAL_MACHINE, registry.WOW64_64KEY},
		{registry.LOCAL_MACHINE, registry.WOW64_32KEY},
	}
	keys := []string{
		`Software\Microsoft\Windows\CurrentVersion\Uninstall\AnthropicClaude`,
		`Software\Microsoft\Windows\CurrentVersion\Uninstall\Claude`,
	}
	for _, location := range locations {
		for _, keyPath := range keys {
			key, err := registry.OpenKey(location.hive, keyPath, registry.QUERY_VALUE|location.access)
			if err != nil {
				continue
			}
			installLocation, _, valueErr := key.GetStringValue("InstallLocation")
			key.Close()
			if valueErr != nil || strings.TrimSpace(installLocation) == "" {
				continue
			}
			kind := installWin32
			launcher := filepath.Join(installLocation, processName)
			if strings.HasSuffix(strings.ToLower(keyPath), "anthropicclaude") {
				kind = installSquirrel
				launcher = squirrelExecutable(installLocation)
			}
			if packageInstalled(launcher) {
				return windowsInstall{
					id:          fmt.Sprintf("install-%d:%s", kind, normalizeWindowsPath(installLocation)),
					root:        filepath.Join(appData, "Claude"),
					executable:  launcher,
					processRoot: installLocation,
					kind:        kind,
				}, true
			}
		}
	}
	return windowsInstall{}, false
}

func squirrelExecutable(root string) string {
	launcher := filepath.Join(root, processName)
	if packageInstalled(launcher) {
		return launcher
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var selected string
	var selectedTime time.Time
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(strings.ToLower(entry.Name()), "app-") {
			continue
		}
		executable := filepath.Join(root, entry.Name(), processName)
		info, err := os.Stat(executable)
		if err != nil || info.IsDir() {
			continue
		}
		if selected == "" || info.ModTime().After(selectedTime) || info.ModTime().Equal(selectedTime) && strings.Compare(executable, selected) > 0 {
			selected = executable
			selectedTime = info.ModTime()
		}
	}
	return selected
}

func packageInstalled(executable string) bool {
	if executable == "" {
		return false
	}
	_, err := os.Stat(executable)
	return err == nil
}

func cookiesPath(appDataPath string) string {
	network := filepath.Join(appDataPath, "Network", "Cookies")
	if _, err := os.Stat(network); err == nil {
		return network
	}
	return filepath.Join(appDataPath, "Cookies")
}

func normalizeWindowsPath(path string) string {
	path = strings.TrimSpace(strings.Trim(path, "\r"))
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	return strings.ToLower(filepath.Clean(path))
}
