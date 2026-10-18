//go:build windows

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestCookiesPathPrefersNetwork(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Network"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Network", "Cookies"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := cookiesPath(root); got != filepath.Join(root, "Network", "Cookies") {
		t.Fatalf("cookiesPath = %q", got)
	}
}

func TestCookiesPathFallsBackToRoot(t *testing.T) {
	root := t.TempDir()
	if got := cookiesPath(root); got != filepath.Join(root, "Cookies") {
		t.Fatalf("cookiesPath = %q", got)
	}
}

func TestSquirrelOwnsVersionedDesktopProcessesOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "AnthropicClaude")
	platform := &windowsPlatform{
		executable:  filepath.Join(root, "claude.exe"),
		processRoot: root,
		kind:        installSquirrel,
	}
	if !platform.ownsProcess(filepath.Join(root, "app-1.20186.1", "claude.exe")) {
		t.Fatal("versioned Squirrel process was not recognized")
	}
	if !platform.ownsProcess(filepath.Join(root, "claude.exe")) {
		t.Fatal("Squirrel launcher was not recognized")
	}
	if platform.ownsProcess(filepath.Join(root, "resources", "claude.exe")) {
		t.Fatal("non-app Squirrel executable was recognized")
	}
	if platform.ownsProcess(filepath.Join(t.TempDir(), "claude.exe")) {
		t.Fatal("unrelated Claude executable was recognized")
	}
}

func TestSquirrelExecutableUsesStableLauncher(t *testing.T) {
	root := t.TempDir()
	launcher := filepath.Join(root, processName)
	if err := os.WriteFile(launcher, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := squirrelExecutable(root); got != launcher {
		t.Fatalf("squirrelExecutable = %q", got)
	}
}

func TestSquirrelExecutableFallsBackToNewestVersion(t *testing.T) {
	root := t.TempDir()
	oldExecutable := filepath.Join(root, "app-1.9.0", processName)
	newExecutable := filepath.Join(root, "app-1.10.0", processName)
	for _, executable := range []string{oldExecutable, newExecutable} {
		if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(executable, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := os.Chtimes(oldExecutable, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newExecutable, now, now); err != nil {
		t.Fatal(err)
	}
	if got := squirrelExecutable(root); got != newExecutable {
		t.Fatalf("squirrelExecutable = %q", got)
	}
}

func TestMSIXFallbackOwnsOnlyClaudePackageProcesses(t *testing.T) {
	root := filepath.Join(`C:\Program Files`, "WindowsApps")
	install := windowsInstall{kind: installMSIX, msix: true, processRoot: root}
	if !install.ownsProcess(filepath.Join(root, "Claude_1.0.0.0_x64__pzs8sxrjxfjjc", "app", "Claude.exe")) {
		t.Fatal("Claude MSIX process was not recognized")
	}
	if install.ownsProcess(filepath.Join(root, "Other_1.0.0.0_x64__example", "app", "Claude.exe")) {
		t.Fatal("unrelated MSIX process was recognized")
	}
	if !install.ownsProcess(filepath.Join(`D:\WindowsApps`, "Claude_1.0.0.0_x64__pzs8sxrjxfjjc", "app", "Claude.exe")) {
		t.Fatal("Claude MSIX process on another drive was not recognized")
	}
}

func TestPathWithinRejectsSiblingDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "AnthropicClaude")
	if !pathWithin(filepath.Join(root, "app-1", "claude.exe"), root) {
		t.Fatal("child path was rejected")
	}
	if pathWithin(root+"-old", root) {
		t.Fatal("sibling path was accepted")
	}
}

func TestStopWindowsProcessesAcceptsLateExit(t *testing.T) {
	var events []string
	waits := []bool{false, false, true}
	wait := func(timeout time.Duration) (bool, error) {
		events = append(events, "wait:"+timeout.String())
		exited := waits[0]
		waits = waits[1:]
		return exited, nil
	}
	taskkill := func(force bool) {
		events = append(events, "taskkill:"+strconv.FormatBool(force))
	}
	force := func(timeout time.Duration) (bool, error) {
		events = append(events, "force:"+timeout.String())
		return false, nil
	}

	if err := stopWindowsProcesses(wait, taskkill, force); err != nil {
		t.Fatal(err)
	}
	want := []string{"wait:2s", "taskkill:false", "wait:2s", "force:3s", "wait:5s"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestStopWindowsProcessesStillRejectsStuckProcess(t *testing.T) {
	wait := func(time.Duration) (bool, error) { return false, nil }
	err := stopWindowsProcesses(wait, func(bool) {}, func(time.Duration) (bool, error) {
		return false, nil
	})
	if err == nil || err.Error() != "claude desktop processes did not exit before timeout" {
		t.Fatalf("error = %v", err)
	}
}

func TestStopWindowsProcessesReturnsInspectionError(t *testing.T) {
	want := errors.New("process inspection failed")
	err := stopWindowsProcesses(func(time.Duration) (bool, error) {
		return false, want
	}, func(bool) {}, func(time.Duration) (bool, error) {
		return false, nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestStopGracefullyWaitsForClaudeToQuit(t *testing.T) {
	var waited []time.Duration
	exited, err := stopGracefully(func() bool { return true }, func(timeout time.Duration) (bool, error) {
		waited = append(waited, timeout)
		return true, nil
	})
	if err != nil || !exited {
		t.Fatalf("stopGracefully = %v, %v; want exited", exited, err)
	}
	if !reflect.DeepEqual(waited, []time.Duration{sessionEndExitTimeout}) {
		t.Fatalf("waits = %v", waited)
	}
}

func TestStopGracefullyFallsBackWithoutWaitingWhenNotAccepted(t *testing.T) {
	exited, err := stopGracefully(func() bool { return false }, func(time.Duration) (bool, error) {
		t.Fatal("waited although Claude did not accept the request")
		return false, nil
	})
	if err != nil || exited {
		t.Fatalf("stopGracefully = %v, %v; want fallback", exited, err)
	}
}

func TestStopGracefullyFallsBackWhenClaudeKeepsRunning(t *testing.T) {
	exited, err := stopGracefully(func() bool { return true }, func(time.Duration) (bool, error) {
		return false, nil
	})
	if err != nil || exited {
		t.Fatalf("stopGracefully = %v, %v; want fallback", exited, err)
	}
}

func TestStopGracefullyReturnsInspectionError(t *testing.T) {
	want := errors.New("process inspection failed")
	_, err := stopGracefully(func() bool { return true }, func(time.Duration) (bool, error) {
		return false, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

type sessionMessage struct {
	hwnd, message, ending uintptr
}

func recordSessionMessages(refuse uintptr, hung uintptr) (*[]sessionMessage, func(hwnd, message, ending uintptr) (uintptr, bool)) {
	var sent []sessionMessage
	return &sent, func(hwnd, message, ending uintptr) (uintptr, bool) {
		sent = append(sent, sessionMessage{hwnd, message, ending})
		if hwnd == hung {
			return 0, false
		}
		if message == windowsWMQueryEndSession && hwnd == refuse {
			return 0, true
		}
		return 1, true
	}
}

func TestEndWindowsSessionAsksEveryWindowBeforeEndingIt(t *testing.T) {
	sent, send := recordSessionMessages(0, 0)
	if !endWindowsSession([]uintptr{11, 22}, send) {
		t.Fatal("session end was not requested")
	}
	want := []sessionMessage{
		{11, windowsWMQueryEndSession, 0},
		{22, windowsWMQueryEndSession, 0},
		{11, windowsWMEndSession, 1},
		{22, windowsWMEndSession, 1},
	}
	if !reflect.DeepEqual(*sent, want) {
		t.Fatalf("messages = %v, want %v", *sent, want)
	}
}

func TestEndWindowsSessionCancelsWhenAWindowRefuses(t *testing.T) {
	sent, send := recordSessionMessages(22, 0)
	if endWindowsSession([]uintptr{11, 22, 33}, send) {
		t.Fatal("session end continued after a refusal")
	}
	want := []sessionMessage{
		{11, windowsWMQueryEndSession, 0},
		{22, windowsWMQueryEndSession, 0},
		{11, windowsWMEndSession, 0},
	}
	if !reflect.DeepEqual(*sent, want) {
		t.Fatalf("messages = %v, want %v", *sent, want)
	}
}

func TestEndWindowsSessionTreatsAHungWindowAsARefusal(t *testing.T) {
	sent, send := recordSessionMessages(0, 11)
	if endWindowsSession([]uintptr{11, 22}, send) {
		t.Fatal("session end continued after a hung window")
	}
	if want := []sessionMessage{{11, windowsWMQueryEndSession, 0}}; !reflect.DeepEqual(*sent, want) {
		t.Fatalf("messages = %v, want %v", *sent, want)
	}
}

func TestEndWindowsSessionNeedsAWindow(t *testing.T) {
	sent, send := recordSessionMessages(0, 0)
	if endWindowsSession(nil, send) || len(*sent) != 0 {
		t.Fatalf("session end without windows sent %v", *sent)
	}
}

func TestShowCommandKeepsAMaximizedWindowMaximized(t *testing.T) {
	if got := showCommand(false); got != windowsSWShow {
		t.Fatalf("showCommand(visible) = %d, want SW_SHOW", got)
	}
	if got := showCommand(true); got != windowsSWRestore {
		t.Fatalf("showCommand(minimized) = %d, want SW_RESTORE", got)
	}
}

func TestEnumTopLevelWindowsCanRunMoreTimesThanGoAllowsCallbacks(t *testing.T) {
	for range 2500 {
		enumTopLevelWindows(func(uintptr) bool { return false })
	}
}
