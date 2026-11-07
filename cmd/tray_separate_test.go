//go:build windows

package cmd

import (
	"path/filepath"
	"testing"
)

func TestSeparateMenuState(t *testing.T) {
	tests := []struct {
		name          string
		inMainWindow  bool
		state         separateWindowState
		enabled       bool
		wantTitle     string
		wantClickable bool
	}{
		{"in the main window", true, separateWindowState{}, true, "office@accounts.test (main window)", false},
		{"closed", false, separateWindowState{}, true, "office@accounts.test", true},
		{"open", false, separateWindowState{open: true}, true, "✓ office@accounts.test (open)", true},
		{"busy", false, separateWindowState{open: true}, false, "✓ office@accounts.test (open)", false},
		{"other account", false, separateWindowState{open: true, otherAccount: true}, true, "✓ office@accounts.test (open) - another account is signed in there", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			title, clickable := separateMenuState("office@accounts.test", test.inMainWindow, test.state, test.enabled)
			if title != test.wantTitle || clickable != test.wantClickable {
				t.Fatalf("separateMenuState() = %q, %v; want %q, %v", title, clickable, test.wantTitle, test.wantClickable)
			}
		})
	}
}

func TestSeparateWindowFolderSitsOutsideTheStorePackage(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	folder, err := separateWindowFolder("Main-account")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(local, "Claude-Accounts", "Main-account"); folder != want {
		t.Fatalf("folder = %q, want %q", folder, want)
	}
}

func TestSeparateWindowInUsePrompt(t *testing.T) {
	title, message := separateWindowInUsePrompt("private@accounts.test")
	if title != "private@accounts.test is open in a separate window" {
		t.Fatalf("title = %q", title)
	}
	want := "private@accounts.test is already signed in to a separate Claude window. Opening it in the main window too would let both windows change the same sessions.\n\n" +
		"Bring that window to the front?"
	if message != want {
		t.Fatalf("message = %q\nwant %q", message, want)
	}
}

func TestSeparateWindowPrompt(t *testing.T) {
	title, message := separateWindowPrompt("office2@accounts.test")
	if title != "Open office2@accounts.test in a separate window?" {
		t.Fatalf("title = %q", title)
	}
	want := "A second Claude Desktop window opens for office2@accounts.test, next to the main one.\n\n" +
		"The first time it opens signed out: choose “Sign in with code” and sign in as office2@accounts.test.\n\n" +
		"It uses about 2 GB of memory while it is open."
	if message != want {
		t.Fatalf("message = %q\nwant %q", message, want)
	}
}
