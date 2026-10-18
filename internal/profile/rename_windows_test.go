package profile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsRefusesProfileRenameWhileAFileInsideIsOpen(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "work")
	mustWriteFile(t, filepath.Join(profile, cookiesFile), "saved")
	reader, err := os.Open(filepath.Join(profile, cookiesFile))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	err = os.Rename(profile, filepath.Join(root, ".work.backup"))
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("rename with an open file = %v, want access denied", err)
	}
	if !transientRenameError(err) {
		t.Fatal("access denied by an open file must be retried")
	}
}

func TestRenamePathWaitsForABriefReader(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "work")
	mustWriteFile(t, filepath.Join(profile, cookiesFile), "saved")
	reader, err := os.Open(filepath.Join(profile, cookiesFile))
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(300 * time.Millisecond)
		_ = reader.Close()
	}()
	defer func() { <-released }()

	if err := renamePath(profile, filepath.Join(root, ".work.backup")); err != nil {
		t.Fatalf("renamePath during a brief read: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".work.backup", cookiesFile)); err != nil {
		t.Fatalf("renamed profile is incomplete: %v", err)
	}
}

func TestRenamePathKeepsTheSourceWhenTheLockPersists(t *testing.T) {
	previous := renameRetryTimeout
	renameRetryTimeout = 400 * time.Millisecond
	t.Cleanup(func() { renameRetryTimeout = previous })
	root := t.TempDir()
	profile := filepath.Join(root, "work")
	mustWriteFile(t, filepath.Join(profile, cookiesFile), "saved")
	reader, err := os.Open(filepath.Join(profile, cookiesFile))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	started := time.Now()
	err = renamePath(profile, filepath.Join(root, ".work.backup"))
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("renamePath with a persistent lock = %v, want access denied", err)
	}
	if elapsed := time.Since(started); elapsed < renameRetryTimeout {
		t.Fatalf("gave up after %v, before the %v retry window", elapsed, renameRetryTimeout)
	}
	if _, err := os.Stat(filepath.Join(profile, cookiesFile)); err != nil {
		t.Fatalf("source profile was not kept in place: %v", err)
	}
}

func TestRenamePathReturnsPermanentErrorsAtOnce(t *testing.T) {
	started := time.Now()
	err := renamePath(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "target"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("renamePath of a missing source = %v, want not exist", err)
	}
	if elapsed := time.Since(started); elapsed >= renameRetryInterval {
		t.Fatalf("permanent error was retried for %v", elapsed)
	}
}

func TestCheckpointSucceedsWhileTheSavedProfileIsBrieflyRead(t *testing.T) {
	store := newTestStore(t)
	if err := store.Checkpoint("work", syntheticAppData(t, "first")); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(filepath.Join(store.profileDir("work"), cookiesFile))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	refused := false
	previous := rename
	rename = func(from, to string) error {
		err := previous(from, to)
		if err != nil && from == store.profileDir("work") && !refused {
			refused = true
			_ = reader.Close()
		}
		return err
	}
	t.Cleanup(func() { rename = previous })

	updated := syntheticAppData(t, "second")
	if err := store.Checkpoint("work", updated); err != nil {
		t.Fatalf("Checkpoint while the profile was being read: %v", err)
	}
	if !refused {
		t.Fatal("the first rename was expected to be refused while the profile was open")
	}
	want, err := cookieDigest(filepath.Join(updated, cookiesFile))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := cookieDigest(filepath.Join(store.profileDir("work"), cookiesFile)); err != nil || got != want {
		t.Fatalf("saved profile was not updated: %v", err)
	}
}
