package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestoreRollbackFailurePreservesOriginals(t *testing.T) {
	store := newTestStore(t)
	saved := syntheticAppData(t, "saved")
	if err := store.Checkpoint("work", saved); err != nil {
		t.Fatal(err)
	}
	live := syntheticAppData(t, "live")
	liveCookies := filepath.Join(live, cookiesFile)
	before, err := cookieDigest(liveCookies)
	if err != nil {
		t.Fatal(err)
	}
	incoming, err := cookieDigest(filepath.Join(saved, cookiesFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(store.baseDir, currentFileName), dirPerm); err != nil {
		t.Fatal(err)
	}
	var locked *os.File
	t.Cleanup(func() {
		if locked != nil {
			_ = locked.Close()
		}
	})
	store.now = func() time.Time {
		if digest, _ := cookieDigest(liveCookies); digest == incoming && locked == nil {
			var openErr error
			locked, openErr = os.Open(liveCookies)
			if openErr != nil {
				t.Fatal(openErr)
			}
		}
		return time.Now()
	}
	err = store.Restore("work", live)
	if locked == nil {
		t.Fatal("replacement Cookies were not locked during the tracking failure")
	}
	if err == nil || !strings.Contains(err.Error(), "recovery") {
		t.Errorf("expected a recovery-required error, got %v", err)
	}
	retained, globErr := filepath.Glob(filepath.Join(live, ".claude-restore-rollback-*", "cookies", cookiesFile))
	if globErr != nil || len(retained) != 1 {
		t.Fatalf("original Cookies backup was deleted after failed rollback: %v (%v)", retained, globErr)
	}
	if digest, err := cookieDigest(retained[0]); err != nil || digest != before {
		t.Fatalf("retained original differs: %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Dir(filepath.Dir(retained[0]))) {
		t.Fatal("error does not identify the retained recovery directory")
	}
}
