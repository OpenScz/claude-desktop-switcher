package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathRollbackRetainsOriginalsAndCanRetry(t *testing.T) {
	root := t.TempDir()
	backup := filepath.Join(root, "backup")
	parent := filepath.Join(root, "parent")
	if err := os.MkdirAll(backup, dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(parent, dirPerm); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(parent, "state")
	other := filepath.Join(root, "other")
	mustWriteFile(t, blocked, "blocked-original")
	mustWriteFile(t, other, "other-original")
	recovery := pathRollback{directory: backup}
	for _, path := range []string{blocked, other} {
		if err := recovery.move(path, filepath.Join(backup, filepath.Base(path))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, parent, "obstruct the original parent directory")
	mustWriteFile(t, other, "replacement")
	cause := errors.New("commit failed")
	err := recovery.restore(cause)
	if !errors.Is(err, cause) || !errors.Is(err, ErrRecoveryRequired) || !strings.Contains(err.Error(), backup) {
		t.Fatalf("recovery error lost cause or retained location: %v", err)
	}
	recovery.cleanup()
	if data, err := os.ReadFile(filepath.Join(backup, "state")); err != nil || string(data) != "blocked-original" {
		t.Fatalf("original was not retained: %q/%v", data, err)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := recovery.restore(cause); err != cause {
		t.Fatalf("retry = %v", err)
	}
	recovery.cleanup()
	for path, want := range map[string]string{blocked: "blocked-original", other: "other-original"} {
		if data, err := os.ReadFile(path); err != nil || string(data) != want {
			t.Fatalf("retry lost original: %q/%v", data, err)
		}
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful recovery did not clean up: %v", err)
	}
}

func TestPathRollbackMissingBackupDoesNotDeleteLiveState(t *testing.T) {
	root := t.TempDir()
	backup := filepath.Join(root, "backup")
	if err := os.Mkdir(backup, dirPerm); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(root, "live")
	mustWriteFile(t, live, "original")
	recovery := pathRollback{directory: backup}
	retained := filepath.Join(backup, "state")
	if err := recovery.move(live, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(retained); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, live, "last remaining state")
	if err := recovery.restore(errors.New("failed")); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("missing original not reported: %v", err)
	}
	recovery.cleanup()
	if data, err := os.ReadFile(live); err != nil || string(data) != "last remaining state" {
		t.Fatalf("live state was destroyed without a backup: %q/%v", data, err)
	}
}
