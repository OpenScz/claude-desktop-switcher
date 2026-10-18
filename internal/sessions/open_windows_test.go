//go:build windows

package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadingARecordDoesNotStopClaudeFromDeletingIt(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(`{"sessionId":"local_a1"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	path := write("local_a1.json")
	shared, err := openShared(path)
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	if err := os.Remove(path); err != nil {
		t.Fatalf("deleting a record while the switcher reads it: %v", err)
	}

	other := write("local_a2.json")
	plain, err := os.Open(other)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if err := os.Remove(other); err == nil {
		t.Fatal("os.Open no longer blocks a delete, so this test proves nothing")
	}
}
