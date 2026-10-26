//go:build windows

package cmd

import (
	"testing"
	"time"
)

func TestTrayLockRejectsDuplicateImmediately(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	first, err := acquireTrayLock()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	started := time.Now()
	if _, err := acquireTrayLock(); err == nil {
		t.Fatal("second tray lock should be rejected")
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("duplicate tray lock took %s; expected immediate rejection", elapsed)
	}
}

func TestBackgroundReadsPauseWhileProfilesChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	state := &trayState{}
	if state.backgroundReadsPaused() {
		t.Fatal("background reads paused without an operation")
	}
	state.switching = true
	if !state.backgroundReadsPaused() {
		t.Fatal("background reads continued during a switch")
	}
	state.switching = false
	state.workflow = &addWorkflow{}
	if !state.backgroundReadsPaused() {
		t.Fatal("background reads continued while an account was being added")
	}
	state.workflow = nil
	lock, err := acquireOperationLock("operation")
	if err != nil {
		t.Fatal(err)
	}
	if !state.backgroundReadsPaused() {
		t.Fatal("background reads continued while another operation held the lock")
	}
	lock.Release()
	if state.backgroundReadsPaused() {
		t.Fatal("background reads stayed paused after the operation finished")
	}
}

func TestClaudeMenuState(t *testing.T) {
	tests := []struct {
		name                     string
		installed, running, busy bool
		wantTitle                string
		wantEnabled              bool
	}{
		{"not installed", false, false, false, "Claude Desktop: Not installed", false},
		{"closed", true, false, false, "Open Claude Desktop", true},
		{"open", true, true, false, "Close Claude Desktop", true},
		{"busy while closed", true, false, true, "Open Claude Desktop", false},
		{"busy while open", true, true, true, "Close Claude Desktop", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			title, enabled := claudeMenuState(test.installed, test.running, test.busy)
			if title != test.wantTitle || enabled != test.wantEnabled {
				t.Fatalf("claudeMenuState() = %q, %v; want %q, %v", title, enabled, test.wantTitle, test.wantEnabled)
			}
		})
	}
}
