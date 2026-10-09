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

func TestMSIXDataPathFallsBackToExistingRoamingProfile(t *testing.T) {
	for _, tc := range []struct {
		name        string
		packaged    bool
		roaming     bool
		roamingFile bool
		wantRoaming bool
	}{
		{name: "existing roaming profile", roaming: true, wantRoaming: true},
		{name: "packaged profile takes precedence", packaged: true, roaming: true},
		{name: "uninitialized profiles keep packaged path"},
		{name: "roaming path must be a directory", roamingFile: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := t.TempDir()
			roaming := t.TempDir()
			t.Setenv("LOCALAPPDATA", local)
			t.Setenv("APPDATA", roaming)
			packageDir := filepath.Join(local, "Packages", windowsPackageFamily)
			packagedRoot := filepath.Join(packageDir, "LocalCache", "Roaming", "Claude")
			roamingRoot := filepath.Join(roaming, "Claude")
			paths := []string{packageDir}
			if tc.packaged {
				paths = append(paths, packagedRoot)
			}
			if tc.roaming {
				paths = append(paths, roamingRoot)
			}
			for _, path := range paths {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.roamingFile {
				if err := os.WriteFile(roamingRoot, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			want := packagedRoot
			if tc.wantRoaming {
				want = roamingRoot
			}
			selected, alternatives := detectWindowsInstall()
			for _, candidate := range append(alternatives, selected) {
				if candidate.kind == installMSIX {
					if candidate.root != want {
						t.Fatalf("MSIX data path = %q, want %q", candidate.root, want)
					}
					return
				}
			}
			t.Fatal("MSIX installation was not detected")
		})
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
