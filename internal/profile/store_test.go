package profile

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCheckpointCreatesSecureV4ProfileWithAccountState(t *testing.T) {
	appData := syntheticAppData(t, "live")
	store := newTestStore(t)
	if err := store.Checkpoint("work", appData); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	entries, err := os.ReadDir(store.profileDir("work"))
	if err != nil {
		t.Fatal(err)
	}
	if got := entryNames(entries); len(got) != 6 || got[0] != cookiesFile || got[1] != indexedDBDir || got[2] != localStorageDir || got[3] != sessionStorageDir || got[4] != accountStateFile || got[5] != metaFile {
		t.Fatalf("profile artifacts = %v, want complete account state", got)
	}
	assertMode(t, store.profileDir("work"), dirPerm)
	assertMode(t, filepath.Join(store.profileDir("work"), cookiesFile), filePerm)
	assertMode(t, filepath.Join(store.profileDir("work"), accountStateFile), filePerm)
	assertMode(t, filepath.Join(store.profileDir("work"), metaFile), filePerm)
	snapshot := filepath.Join(store.profileDir("work"), localStorageDir, leveldbDir, "CURRENT")
	assertMode(t, filepath.Join(store.profileDir("work"), localStorageDir, leveldbDir), dirPerm)
	assertMode(t, snapshot, filePerm)
	assertMode(t, filepath.Join(store.profileDir("work"), indexedDBDir, "data"), filePerm)
	assertMode(t, filepath.Join(store.profileDir("work"), sessionStorageDir, "data"), filePerm)
	meta, err := store.loadMeta("work")
	if err != nil {
		t.Fatal(err)
	}
	if meta.FormatVersion != formatVersion || meta.ObservedHealth != HealthUsable || meta.CookieDigest == "" || meta.AccountStateDigest == "" || meta.SavedAt.IsZero() {
		t.Fatalf("incomplete v4 metadata: %+v", meta)
	}
}

func TestCheckpointStoresEmailAndPrimaryAccountIdentityFromSnapshot(t *testing.T) {
	store := newTestStore(t)
	appData := syntheticAppData(t, "saved")
	accountID := "11111111-1111-4111-8111-111111111111"
	setLocalIdentity(t, appData, "Person@Example.Test", accountID)
	writeAccountFixture(t, appData, "token-a", "token-v2-a", accountID, accountID, "dark", true)

	if err := store.Checkpoint("work", appData); err != nil {
		t.Fatal(err)
	}
	meta, err := store.loadMeta("work")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Email != "person@example.test" {
		t.Fatalf("Email = %q", meta.Email)
	}
	if meta.AccountUUIDHash != identityHash([]byte(accountID)) || strings.Contains(meta.AccountUUIDHash, accountID) {
		t.Fatalf("unsafe primary account identity = %q", meta.AccountUUIDHash)
	}
}

func TestCheckpointRejectsDuplicatePrimaryAccountWithoutCreatingProfile(t *testing.T) {
	store := newTestStore(t)
	accountID := "11111111-1111-4111-8111-111111111111"
	first := syntheticAppData(t, "first-session")
	writeAccountFixture(t, first, "token-a", "token-v2-a", accountID, accountID, "dark", true)
	if err := store.Checkpoint("first", first); err != nil {
		t.Fatal(err)
	}

	second := syntheticAppData(t, "renewed-session")
	writeAccountFixture(t, second, "token-b", "token-v2-b", accountID, accountID, "light", true)
	err := store.Checkpoint("second", second)
	var duplicate *DuplicateAccountError
	if !errors.As(err, &duplicate) || duplicate.ExistingName != "first" {
		t.Fatalf("Checkpoint error = %v", err)
	}
	if store.Exists("second") {
		t.Fatal("duplicate profile was created")
	}
}

func TestFindByAccountIdentityAtMatchesRenewedLiveSession(t *testing.T) {
	store := newTestStore(t)
	accountID := "11111111-1111-4111-8111-111111111111"
	saved := syntheticAppData(t, "saved-session")
	writeAccountFixture(t, saved, "token-a", "token-v2-a", accountID, accountID, "dark", true)
	if err := store.Checkpoint("first", saved); err != nil {
		t.Fatal(err)
	}

	renewed := syntheticAppData(t, "renewed-session")
	writeAccountFixture(t, renewed, "token-b", "token-v2-b", accountID, accountID, "light", true)
	if name, err := store.FindByAccountIdentityAt(renewed); err != nil || name != "first" {
		t.Fatalf("FindByAccountIdentityAt = %q, %v", name, err)
	}

	other := syntheticAppData(t, "other-session")
	writeAccountFixture(t, other, "token-c", "token-v2-c", "other-account", "other-account", "dark", true)
	if name, err := store.FindByAccountIdentityAt(other); err != nil || name != "" {
		t.Fatalf("different account match = %q, %v", name, err)
	}
}

func TestFindByAccountIdentityAtRejectsAmbiguousProfiles(t *testing.T) {
	store := newTestStore(t)
	accountID := "11111111-1111-4111-8111-111111111111"
	saved := syntheticAppData(t, "saved-session")
	writeAccountFixture(t, saved, "token-a", "token-v2-a", accountID, accountID, "dark", true)
	if err := store.Checkpoint("first", saved); err != nil {
		t.Fatal(err)
	}
	if err := copyDir(store.profileDir("first"), store.profileDir("second")); err != nil {
		t.Fatal(err)
	}
	meta, err := store.loadMeta("second")
	if err != nil {
		t.Fatal(err)
	}
	meta.Name = "second"
	if err := store.saveMeta("second", meta); err != nil {
		t.Fatal(err)
	}

	renewed := syntheticAppData(t, "renewed-session")
	writeAccountFixture(t, renewed, "token-b", "token-v2-b", accountID, accountID, "light", true)
	if name, err := store.FindByAccountIdentityAt(renewed); err != nil || name != "" {
		t.Fatalf("ambiguous identity match = %q, %v", name, err)
	}
}

func TestMatchLiveAtResolvesIdentityAboveNetworkCookies(t *testing.T) {
	store := newTestStore(t)
	accountID := "11111111-1111-4111-8111-111111111111"
	saved := syntheticAppData(t, "saved-session")
	writeAccountFixture(t, saved, "token-a", "token-v2-a", accountID, accountID, "dark", true)
	if err := store.Checkpoint("personal", saved); err != nil {
		t.Fatal(err)
	}

	live := syntheticAppData(t, "renewed-session")
	writeAccountFixture(t, live, "token-b", "token-v2-b", accountID, accountID, "light", true)
	network := filepath.Join(live, "Network")
	if err := os.MkdirAll(network, dirPerm); err != nil {
		t.Fatal(err)
	}
	networkCookies := filepath.Join(network, cookiesFile)
	if err := os.Rename(filepath.Join(live, cookiesFile), networkCookies); err != nil {
		t.Fatal(err)
	}

	if name, health := store.MatchLiveAt(networkCookies); name != "personal" || health != HealthUsable {
		t.Fatalf("MatchLiveAt(Network/Cookies) = %q/%s, want personal/usable", name, health)
	}
}

func TestUpdateAccountEmailFromLiveRequiresMatchingPrimaryIdentity(t *testing.T) {
	store := newTestStore(t)
	accountID := "11111111-1111-4111-8111-111111111111"
	saved := syntheticAppData(t, "saved-session")
	writeAccountFixture(t, saved, "token-a", "token-v2-a", accountID, accountID, "dark", true)
	if err := store.Checkpoint("first", saved); err != nil {
		t.Fatal(err)
	}

	other := syntheticAppData(t, "other-session")
	setLocalIdentity(t, other, "wrong@example.test", "other-account")
	writeAccountFixture(t, other, "token-b", "token-v2-b", "other-account", "other-account", "light", true)
	if _, err := store.UpdateAccountEmailFromLive("first", other); err == nil {
		t.Fatal("email was updated from a different account")
	}

	live := syntheticAppData(t, "renewed-session")
	setLocalIdentity(t, live, "Person@Example.Test", accountID)
	writeAccountFixture(t, live, "token-c", "token-v2-c", accountID, accountID, "light", true)
	email, err := store.UpdateAccountEmailFromLive("first", live)
	if err != nil {
		t.Fatal(err)
	}
	if email != "person@example.test" {
		t.Fatalf("email = %q", email)
	}
	meta, err := store.loadMeta("first")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Email != email || !meta.EmailLookupDone || len(meta.IdentityHashes) != 1 {
		t.Fatalf("updated metadata = %+v", meta)
	}
}

func TestCheckpointRejectsDuplicateSessionWhenPrimaryIdentityIsUnavailable(t *testing.T) {
	store := newTestStore(t)
	first := syntheticAppData(t, "same-session")
	if err := store.Checkpoint("first", first); err != nil {
		t.Fatal(err)
	}
	second := syntheticAppData(t, "same-session")
	err := store.Checkpoint("second", second)
	var duplicate *DuplicateAccountError
	if !errors.As(err, &duplicate) || duplicate.ExistingName != "first" {
		t.Fatalf("Checkpoint error = %v", err)
	}
	if store.Exists("second") {
		t.Fatal("duplicate session profile was created")
	}
}

func TestCheckpointPrimaryIdentityAllowsDifferentAccounts(t *testing.T) {
	store := newTestStore(t)
	first := syntheticAppData(t, "first-session")
	second := syntheticAppData(t, "second-session")
	sharedUUIDs := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}
	setLocalIdentity(t, first, "same@example.test", sharedUUIDs...)
	setLocalIdentity(t, second, "same@example.test", sharedUUIDs...)
	writeAccountFixture(t, first, "token-a", "token-v2-a", "account-a", "owner-a", "dark", true)
	writeAccountFixture(t, second, "token-b", "token-v2-b", "account-b", "owner-b", "light", true)

	if err := store.Checkpoint("first", first); err != nil {
		t.Fatal(err)
	}
	if err := store.Checkpoint("second", second); err != nil {
		t.Fatalf("different primary account was rejected: %v", err)
	}
}

func TestBackfillAccountMetadataRecoversLegacyLabelAndPrimaryIdentity(t *testing.T) {
	store := newTestStore(t)
	appData := syntheticAppData(t, "saved")
	accountID := "11111111-1111-4111-8111-111111111111"
	setLocalIdentity(t, appData, "person@example.test", accountID)
	writeAccountFixture(t, appData, "token-a", "token-v2-a", accountID, accountID, "dark", true)
	if err := store.Checkpoint("work", appData); err != nil {
		t.Fatal(err)
	}
	meta, err := store.loadMeta("work")
	if err != nil {
		t.Fatal(err)
	}
	meta.Email = ""
	meta.EmailLookupDone = false
	meta.AccountUUIDHash = ""
	if err := store.saveMeta("work", meta); err != nil {
		t.Fatal(err)
	}

	meta, err = store.BackfillAccountMetadata("work")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Email != "person@example.test" || meta.AccountUUIDHash != identityHash([]byte(accountID)) {
		t.Fatalf("backfilled metadata = %+v", meta)
	}
}

func TestBackfillAccountMetadataMarksUnavailableEmailAsChecked(t *testing.T) {
	store := newTestStore(t)
	appData := syntheticAppData(t, "saved")
	accountID := "11111111-1111-4111-8111-111111111111"
	writeAccountFixture(t, appData, "token-a", "token-v2-a", accountID, accountID, "dark", true)
	if err := store.Checkpoint("work", appData); err != nil {
		t.Fatal(err)
	}
	meta, err := store.loadMeta("work")
	if err != nil {
		t.Fatal(err)
	}
	meta.EmailLookupDone = false
	if err := store.saveMeta("work", meta); err != nil {
		t.Fatal(err)
	}

	meta, err = store.BackfillAccountMetadata("work")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Email != "" || !meta.EmailLookupDone {
		t.Fatalf("email discovery state = %q/%v", meta.Email, meta.EmailLookupDone)
	}
}

func TestCheckpointRequiresExplicitMetadataUpdateForChangedEmail(t *testing.T) {
	store := newTestStore(t)
	accountID := "11111111-1111-4111-8111-111111111111"
	saved := syntheticAppData(t, "old-session")
	setLocalIdentity(t, saved, "old@example.test", accountID)
	writeAccountFixture(t, saved, "token-a", "token-v2-a", accountID, accountID, "dark", true)
	if err := store.Checkpoint("work", saved); err != nil {
		t.Fatal(err)
	}

	renewed := syntheticAppData(t, "renewed-session")
	setLocalIdentity(t, renewed, "new@example.test", accountID)
	writeAccountFixture(t, renewed, "token-b", "token-v2-b", accountID, accountID, "light", true)
	if err := store.Checkpoint("work", renewed); err != nil {
		t.Fatal(err)
	}
	meta, err := store.loadMeta("work")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Email != "old@example.test" || !store.IdentityEmailChangedAt("work", filepath.Join(renewed, cookiesFile)) {
		t.Fatalf("email change confirmation was bypassed: %+v", meta)
	}
	if email, err := store.UpdateAccountEmailFromProfile("work"); err != nil || email != "new@example.test" {
		t.Fatalf("UpdateAccountEmailFromProfile = %q/%v", email, err)
	}
	if store.IdentityEmailChangedAt("work", filepath.Join(renewed, cookiesFile)) {
		t.Fatal("confirmed email update remains pending")
	}
}

func TestCheckpointRecreatesDeletedStoreDirectory(t *testing.T) {
	store := newTestStore(t)
	appData := syntheticAppData(t, "live")
	if err := os.RemoveAll(store.baseDir); err != nil {
		t.Fatal(err)
	}
	if err := store.Checkpoint("work", appData); err != nil {
		t.Fatalf("Checkpoint after store deletion: %v", err)
	}
	if !store.Exists("work") {
		t.Fatal("profile was not recreated")
	}
}

func TestCheckpointRefusesUnusableLiveStateWithoutOverwritingProfile(t *testing.T) {
	store := newTestStore(t)
	healthy := syntheticAppData(t, "healthy")
	if err := store.Checkpoint("work", healthy); err != nil {
		t.Fatal(err)
	}
	before, _ := cookieDigest(filepath.Join(store.profileDir("work"), cookiesFile))

	unusable := t.TempDir()
	createCookiesDB(t, filepath.Join(unusable, cookiesFile), ".claude.ai", "other", 0)
	if err := store.Checkpoint("work", unusable); err == nil {
		t.Fatal("Checkpoint should reject missing session evidence")
	}
	after, _ := cookieDigest(filepath.Join(store.profileDir("work"), cookiesFile))
	if after != before {
		t.Fatal("unusable live state overwrote the saved usable profile")
	}
}

func TestRestoreRefusesUnsafePermissionsAndIntegrityMismatchBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Store)
	}{
		{"unsafe permissions", func(t *testing.T, s *Store) {
			if runtime.GOOS == "windows" {
				t.Skip("Windows has no POSIX mode bits to make unsafe")
			}
			if err := os.Chmod(filepath.Join(s.profileDir("work"), cookiesFile), 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{"digest mismatch", func(t *testing.T, s *Store) {
			path := filepath.Join(s.profileDir("work"), cookiesFile)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			createCookiesDB(t, path, ".claude.ai", "sessionKey", 0)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			appData := syntheticAppData(t, "saved")
			if err := store.Checkpoint("work", appData); err != nil {
				t.Fatal(err)
			}
			live := syntheticAppData(t, "live-before")
			before, _ := cookieDigest(filepath.Join(live, cookiesFile))
			tt.mutate(t, store)
			if err := store.Restore("work", live); err == nil {
				t.Fatal("Restore should refuse invalid profile")
			}
			after, _ := cookieDigest(filepath.Join(live, cookiesFile))
			if after != before {
				t.Fatal("live Cookies changed before validation completed")
			}
		})
	}
}

func TestV2CookieIntegrityRemainsEnforced(t *testing.T) {
	store := newTestStore(t)
	appData := syntheticAppData(t, "saved")
	if err := store.Checkpoint("work", appData); err != nil {
		t.Fatal(err)
	}
	meta, err := store.loadMeta("work")
	if err != nil {
		t.Fatal(err)
	}
	meta.FormatVersion = 2
	if err := store.saveMeta("work", meta); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.profileDir("work"), cookiesFile)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	createCookiesDBWithMarker(t, path, "tampered")

	if got := store.Inspect("work"); got.Health != HealthUnknown {
		t.Fatalf("v2 integrity health = %s, want unknown", got.Health)
	}
}

func TestHealthyV1RestoresAndMigratesOnlyOnCheckpoint(t *testing.T) {
	store := newTestStore(t)
	v1 := store.profileDir("legacy")
	if err := os.MkdirAll(filepath.Join(v1, "leveldb"), dirPerm); err != nil {
		t.Fatal(err)
	}
	createCookiesDB(t, filepath.Join(v1, cookiesFile), ".claude.ai", "sessionKey", 0)
	mustWriteFile(t, filepath.Join(v1, "leveldb", "legacy"), "keep-until-commit")
	mustWriteMeta(t, filepath.Join(v1, metaFile), Meta{Name: "legacy", CreatedAt: time.Now()})
	live := syntheticAppData(t, "other")

	if err := store.Restore("legacy", live); err != nil {
		t.Fatalf("restore v1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v1, "leveldb", "legacy")); err != nil {
		t.Fatal("restore eagerly migrated v1")
	}
	if err := store.Checkpoint("legacy", live); err != nil {
		t.Fatalf("checkpoint migrated v1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v1, "leveldb")); !os.IsNotExist(err) {
		t.Fatal("legacy payload remains after v2 commit")
	}
	meta, _ := store.loadMeta("legacy")
	if meta.FormatVersion != formatVersion {
		t.Fatalf("format version = %d", meta.FormatVersion)
	}
}

func TestUnusableV1IsNotRepaired(t *testing.T) {
	store := newTestStore(t)
	v1 := store.profileDir("expired")
	if err := os.MkdirAll(v1, dirPerm); err != nil {
		t.Fatal(err)
	}
	createCookiesDB(t, filepath.Join(v1, cookiesFile), ".claude.ai", "sessionKey", chromiumTime(store.now().Add(-time.Hour)))
	live := syntheticAppData(t, "live")
	if err := store.Restore("expired", live); err == nil {
		t.Fatal("expired v1 should require reauthentication")
	}
	meta, _ := store.loadMeta("expired")
	if meta.FormatVersion == 2 {
		t.Fatal("expired v1 was synthesized as v2")
	}
}

func TestStoreRecoversBackupAndRemovesOrphanStage(t *testing.T) {
	base := t.TempDir()
	profiles := filepath.Join(base, profilesDirName)
	backup := filepath.Join(profiles, ".work.backup")
	stage := filepath.Join(profiles, ".work.stage-dead")
	if err := os.MkdirAll(backup, dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stage, dirPerm); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(backup, metaFile), `{}`)
	store, err := newStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if !store.Exists("work") {
		t.Fatal("backup was not recovered")
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("orphan stage was not removed")
	}
}

func TestDeleteCurrentProfileClearsTrackingAndTemporaryData(t *testing.T) {
	store := newTestStore(t)
	if err := store.Checkpoint("work", syntheticAppData(t, "delete-current")); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent("work"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("work"); err != nil {
		t.Fatal(err)
	}
	if store.Exists("work") {
		t.Fatal("deleted profile still exists")
	}
	if _, err := store.Current(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current marker error = %v, want not exist", err)
	}
	deletedPath := filepath.Join(store.profilesPath(), ".work"+deleteSuffix)
	if _, err := os.Stat(deletedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary deletion path still exists: %v", err)
	}
}

func TestDeletePreservesProfileWhenCurrentMarkerCannotBeRead(t *testing.T) {
	store := newTestStore(t)
	if err := store.Checkpoint("work", syntheticAppData(t, "delete-marker-error")); err != nil {
		t.Fatal(err)
	}
	currentPath := filepath.Join(store.baseDir, currentFileName)
	if err := os.Mkdir(currentPath, dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("work"); err == nil {
		t.Fatal("delete succeeded with an unreadable current marker")
	}
	if !store.Exists("work") {
		t.Fatal("profile was changed despite the marker read error")
	}
}

func TestStoreRollsBackInterruptedCurrentProfileDeletion(t *testing.T) {
	base := t.TempDir()
	profiles := filepath.Join(base, profilesDirName)
	deletedPath := filepath.Join(profiles, ".work"+deleteSuffix)
	if err := os.MkdirAll(deletedPath, dirPerm); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(deletedPath, metaFile), `{}`)
	mustWriteFile(t, filepath.Join(base, currentFileName), "work")

	store, err := newStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if !store.Exists("work") {
		t.Fatal("interrupted current-profile deletion was not rolled back")
	}
	if _, err := os.Stat(deletedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary deletion path still exists: %v", err)
	}
	if current, err := store.Current(); err != nil || current != "work" {
		t.Fatalf("current = %q/%v, want work", current, err)
	}
}

func TestStoreCompletesInterruptedInactiveProfileDeletion(t *testing.T) {
	base := t.TempDir()
	profiles := filepath.Join(base, profilesDirName)
	deletedPath := filepath.Join(profiles, ".work"+deleteSuffix)
	if err := os.MkdirAll(deletedPath, dirPerm); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(deletedPath, metaFile), `{}`)
	mustWriteFile(t, filepath.Join(base, currentFileName), "other")

	store, err := newStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if store.Exists("work") {
		t.Fatal("interrupted inactive-profile deletion was rolled back")
	}
	if _, err := os.Stat(deletedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary deletion path still exists: %v", err)
	}
	if current, err := store.Current(); err != nil || current != "other" {
		t.Fatalf("current = %q/%v, want other", current, err)
	}
}

func TestRestoreReplacesCompleteAccountStateAndPreservesGlobalState(t *testing.T) {
	store := newTestStore(t)
	saved := syntheticAppData(t, "saved")
	db := openSQLite(t, filepath.Join(saved, cookiesFile))
	mustExec(t, db, `INSERT INTO cookies(host_key, name, expires_utc, value, encrypted_value) VALUES ('.claude.ai', 'cf_clearance', 0, '', x'03')`)
	mustExec(t, db, `INSERT INTO cookies(host_key, name, expires_utc, value, encrypted_value) VALUES ('.claude.ai', '__cf_bm', 0, '', x'04')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Checkpoint("work", saved); err != nil {
		t.Fatal(err)
	}
	live := syntheticAppData(t, "live")
	for _, name := range []string{cookiesJournalFile, cookiesWALFile, cookiesSHMFile} {
		mustWriteFile(t, filepath.Join(live, name), "stale")
	}
	global := map[string]string{"WebStorage/state": "web", "partitions/p": "partition", deviceIDFile: "machine"}
	for path, content := range global {
		mustWriteFile(t, filepath.Join(live, path), content)
	}
	mustWriteFile(t, filepath.Join(live, claudeConfigFile), `{"global":"keep"}`)

	if err := store.Restore("work", live); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restored := filepath.Join(live, localStorageDir, leveldbDir, "CURRENT")
	if got, err := os.ReadFile(restored); err != nil || string(got) != "saved" {
		t.Fatalf("Local Storage not restored from snapshot: %q %v", got, err)
	}
	for _, path := range []string{filepath.Join(indexedDBDir, "data"), filepath.Join(sessionStorageDir, "data")} {
		if got, err := os.ReadFile(filepath.Join(live, path)); err != nil || string(got) != "saved" {
			t.Fatalf("%s not restored from snapshot: %q %v", path, got, err)
		}
	}
	db = openSQLite(t, filepath.Join(live, cookiesFile))
	defer db.Close()
	var securityCookies int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookies WHERE name IN ('cf_clearance', '__cf_bm')`).Scan(&securityCookies); err != nil {
		t.Fatal(err)
	}
	if securityCookies != 2 {
		t.Fatalf("restored security cookies = %d, want 2", securityCookies)
	}
	for _, name := range []string{cookiesJournalFile, cookiesWALFile, cookiesSHMFile} {
		if _, err := os.Stat(filepath.Join(live, name)); !os.IsNotExist(err) {
			t.Fatalf("sidecar remains: %s", name)
		}
	}
	for path, want := range global {
		got, err := os.ReadFile(filepath.Join(live, path))
		if err != nil || string(got) != want {
			t.Fatalf("global %s changed: %q %v", path, got, err)
		}
	}
	config, _, err := readJSONObject(filepath.Join(live, claudeConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	assertJSONString(t, config, "global", "keep")
}

func TestRestoreWithoutSnapshotLeavesAccountStateCleared(t *testing.T) {
	store := newTestStore(t)
	saved := t.TempDir()
	createCookiesDBWithMarker(t, filepath.Join(saved, cookiesFile), "saved")
	if err := store.Checkpoint("work", saved); err != nil {
		t.Fatal(err)
	}
	live := syntheticAppData(t, "live")
	if err := store.Restore("work", live); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for _, path := range []string{filepath.Join(localStorageDir, leveldbDir), indexedDBDir, sessionStorageDir} {
		if _, err := os.Stat(filepath.Join(live, path)); !os.IsNotExist(err) {
			t.Fatalf("live %s should stay cleared when the profile has no snapshot", path)
		}
	}
}

func TestRestoreTrackingFailureRollsBackCookies(t *testing.T) {
	store := newTestStore(t)
	saved := syntheticAppData(t, "saved")
	writeAccountFixture(t, saved, "saved-token", "saved-token-v2", "saved-account", "saved-owner", "saved-theme", true)
	if err := store.Checkpoint("work", saved); err != nil {
		t.Fatal(err)
	}
	live := syntheticAppData(t, "live")
	writeAccountFixture(t, live, "live-token", "live-token-v2", "live-account", "live-owner", "live-theme", true)
	before, _ := cookieDigest(filepath.Join(live, cookiesFile))
	configBefore, _ := os.ReadFile(filepath.Join(live, claudeConfigFile))
	coworkBefore, _ := os.ReadFile(filepath.Join(live, coworkOpsFile))
	localPath := filepath.Join(live, localStorageDir, leveldbDir, "CURRENT")
	localBefore, _ := os.ReadFile(localPath)
	indexedPath := filepath.Join(live, indexedDBDir, "data")
	indexedBefore, _ := os.ReadFile(indexedPath)
	sessionPath := filepath.Join(live, sessionStorageDir, "data")
	sessionBefore, _ := os.ReadFile(sessionPath)
	if err := os.Mkdir(filepath.Join(store.baseDir, currentFileName), dirPerm); err != nil {
		t.Fatal(err)
	}

	if err := store.Restore("work", live); err == nil {
		t.Fatal("Restore should fail when tracking cannot commit")
	}
	after, _ := cookieDigest(filepath.Join(live, cookiesFile))
	if after != before {
		t.Fatal("live Cookies were not rolled back")
	}
	localAfter, _ := os.ReadFile(localPath)
	if string(localAfter) != string(localBefore) {
		t.Fatal("live Local Storage was not rolled back")
	}
	indexedAfter, _ := os.ReadFile(indexedPath)
	if string(indexedAfter) != string(indexedBefore) {
		t.Fatal("live IndexedDB was not rolled back")
	}
	sessionAfter, _ := os.ReadFile(sessionPath)
	if string(sessionAfter) != string(sessionBefore) {
		t.Fatal("live Session Storage was not rolled back")
	}
	configAfter, _ := os.ReadFile(filepath.Join(live, claudeConfigFile))
	coworkAfter, _ := os.ReadFile(filepath.Join(live, coworkOpsFile))
	if string(configAfter) != string(configBefore) || string(coworkAfter) != string(coworkBefore) {
		t.Fatal("live account configuration was not rolled back")
	}
}

func TestRestoreFailureRemovesNewAccountStateWhenLiveHadNone(t *testing.T) {
	store := newTestStore(t)
	saved := syntheticAppData(t, "saved")
	writeAccountFixture(t, saved, "saved-token", "saved-token-v2", "saved-account", "saved-owner", "saved-theme", true)
	if err := store.Checkpoint("work", saved); err != nil {
		t.Fatal(err)
	}
	live := t.TempDir()
	createCookiesDBWithMarker(t, filepath.Join(live, cookiesFile), "live")
	before, _ := cookieDigest(filepath.Join(live, cookiesFile))
	if err := os.Mkdir(filepath.Join(store.baseDir, currentFileName), dirPerm); err != nil {
		t.Fatal(err)
	}

	if err := store.Restore("work", live); err == nil {
		t.Fatal("Restore should fail when tracking cannot commit")
	}
	after, _ := cookieDigest(filepath.Join(live, cookiesFile))
	if after != before {
		t.Fatal("live Cookies were not rolled back")
	}
	for _, path := range []string{filepath.Join(localStorageDir, leveldbDir), indexedDBDir, sessionStorageDir} {
		if _, err := os.Stat(filepath.Join(live, path)); !os.IsNotExist(err) {
			t.Fatalf("new account state remains after rollback: %s", path)
		}
	}
	for _, path := range []string{claudeConfigFile, coworkOpsFile} {
		if _, err := os.Stat(filepath.Join(live, path)); !os.IsNotExist(err) {
			t.Fatalf("new account configuration remains after rollback: %s", path)
		}
	}
}

func TestMatchLiveDoesNotTrustStaleCurrent(t *testing.T) {
	store := newTestStore(t)
	a := syntheticAppData(t, "a")
	b := syntheticAppData(t, "b")
	if err := store.Checkpoint("a", a); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent("a"); err != nil {
		t.Fatal(err)
	}
	if name, health := store.MatchLive(b); name != "" || health != HealthUsable {
		t.Fatalf("match = %q/%s, want unknown usable", name, health)
	}
	if name, health := store.MatchLive(a); name != "a" || health != HealthUsable {
		t.Fatalf("match = %q/%s, want a/usable", name, health)
	}
}

func TestMatchLiveRecognizesRenewedSessionByLocalIdentity(t *testing.T) {
	store := newTestStore(t)
	saved := syntheticAppData(t, "old-session")
	setLocalIdentity(t, saved, "person@example.test", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
	if err := store.Checkpoint("personal", saved); err != nil {
		t.Fatal(err)
	}
	meta, err := store.loadMeta("personal")
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.AccountUUIDHashes) != 2 || len(meta.IdentityHashes) != 1 || len(meta.AccountUUIDHashes[0]) != sha256.Size*2 || strings.Contains(meta.IdentityHashes[0], "person") {
		t.Fatalf("unsafe identity hashes: %v/%v", meta.AccountUUIDHashes, meta.IdentityHashes)
	}
	live := syntheticAppData(t, "renewed-session")
	setLocalIdentity(t, live, "renamed@example.test", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")

	if name, health := store.MatchLive(live); name != "personal" || health != HealthUsable {
		t.Fatalf("match = %q/%s, want personal/usable", name, health)
	}
	differentAccount := syntheticAppData(t, "different-session")
	setLocalIdentity(t, differentAccount, "other@example.test", "11111111-1111-4111-8111-111111111111", "33333333-3333-4333-8333-333333333333")
	if name, health := store.MatchLive(differentAccount); name != "" || health != HealthUsable {
		t.Fatalf("match = %q/%s, want different usable account", name, health)
	}
}

func TestMatchLiveRecognizesRenewedSessionByPrimaryAccountIdentity(t *testing.T) {
	store := newTestStore(t)
	accountID := "11111111-1111-4111-8111-111111111111"
	saved := syntheticAppData(t, "old-session")
	writeAccountFixture(t, saved, "token-a", "token-v2-a", accountID, accountID, "dark", true)
	if err := store.Checkpoint("personal", saved); err != nil {
		t.Fatal(err)
	}
	live := syntheticAppData(t, "renewed-session")
	writeAccountFixture(t, live, "token-b", "token-v2-b", accountID, accountID, "light", true)

	if name, health := store.MatchLive(live); name != "personal" || health != HealthUsable {
		t.Fatalf("match = %q/%s, want personal/usable", name, health)
	}
}

func TestHasLoginEvidenceAtAcceptsAccountIdentityWithoutReadableCookies(t *testing.T) {
	appData := t.TempDir()
	setLocalIdentity(t, appData, "person@example.test", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
	if !HasLoginEvidenceAt(appData, filepath.Join(appData, cookiesFile)) {
		t.Fatal("complete local account identity should be accepted as login evidence")
	}
}

func TestHasLoginEvidenceAtRejectsIncompleteIdentity(t *testing.T) {
	appData := t.TempDir()
	setLocalIdentity(t, appData, "", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
	if HasLoginEvidenceAt(appData, filepath.Join(appData, cookiesFile)) {
		t.Fatal("identity without an email should not be accepted as login evidence")
	}
}

func TestAccountEmailAtReturnsSingleLocalAccountEmail(t *testing.T) {
	appData := t.TempDir()
	setLocalIdentity(t, appData, "Person@Example.Test", "11111111-1111-4111-8111-111111111111")
	if got := AccountEmailAt(appData); got != "person@example.test" {
		t.Fatalf("AccountEmailAt = %q", got)
	}
}

func TestAccountEmailAtReadsLocalStorageFallback(t *testing.T) {
	appData := t.TempDir()
	mustWriteFile(t, filepath.Join(appData, localStorageDir, leveldbDir, "state.log"), "11111111-1111-4111-8111-111111111111 emailAddress=person@example.test")
	if got := AccountEmailAt(appData); got != "person@example.test" {
		t.Fatalf("Local Storage AccountEmailAt = %q", got)
	}
}

func TestAccountEmailAtIgnoresEmailOutsideAccountRecord(t *testing.T) {
	appData := t.TempDir()
	data := "email_address 11111111-1111-4111-8111-111111111111 person@example.test\x00" + strings.Repeat("x", identityRecordWindow*2) + "\x00stale@example.test"
	mustWriteFile(t, filepath.Join(appData, indexedDBDir, "state"), data)
	if got := AccountEmailAt(appData); got != "person@example.test" {
		t.Fatalf("AccountEmailAt with unrelated stale email = %q", got)
	}
}

func TestAccountEmailAtRejectsMarkerWithoutAccountIdentity(t *testing.T) {
	appData := t.TempDir()
	mustWriteFile(t, filepath.Join(appData, indexedDBDir, "state"), "email_address=stale@example.test")
	if got := AccountEmailAt(appData); got != "" {
		t.Fatalf("AccountEmailAt without account UUID = %q", got)
	}
}

func TestAccountEmailAtRejectsAmbiguousOrUnmarkedData(t *testing.T) {
	appData := t.TempDir()
	mustWriteFile(t, filepath.Join(appData, indexedDBDir, "ambiguous"), "account_profile 11111111-1111-4111-8111-111111111111 email_address=one@example.test other@example.test")
	if got := AccountEmailAt(appData); got != "" {
		t.Fatalf("ambiguous AccountEmailAt = %q", got)
	}
	unmarked := t.TempDir()
	mustWriteFile(t, filepath.Join(unmarked, indexedDBDir, "cache"), "person@example.test")
	if got := AccountEmailAt(unmarked); got != "" {
		t.Fatalf("unmarked AccountEmailAt = %q", got)
	}
}

func TestLatestAccountLogStateUsesLastStartupState(t *testing.T) {
	appData := t.TempDir()
	logs := filepath.Join(appData, "logs")
	if err := os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(logs, "main.log")
	offset := LoginLogOffsetAt(appData)
	startedAt := time.Date(2026, 7, 23, 20, 58, 45, 0, time.Local)
	data := strings.Join([]string{
		"2026-07-23 20:58:47 [info] claude.ai account active and logged in",
		"2026-07-23 20:58:47 [info] [account] User logged out during IPC wait, stopping early",
	}, "\n")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if got := LatestAccountLogStateSince(appData, offset, startedAt); got != AccountLogSignedOut {
		t.Fatalf("account log state = %v, want signed out", got)
	}
}

func TestLatestAccountLogStateAcceptsStableSignedInStartup(t *testing.T) {
	appData := t.TempDir()
	logs := filepath.Join(appData, "logs")
	if err := os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 23, 20, 58, 45, 0, time.Local)
	if err := os.WriteFile(
		filepath.Join(logs, "main.log"),
		[]byte("2026-07-23 20:58:47 [info] claude.ai account active and logged in\n"),
		0600,
	); err != nil {
		t.Fatal(err)
	}
	if got := LatestAccountLogStateSince(appData, 0, startedAt); got != AccountLogSignedIn {
		t.Fatalf("account log state = %v, want signed in", got)
	}
}

func TestLatestAccountLogStateUsesCurrentWindowsLocalLog(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows log layout")
	}
	roaming := t.TempDir()
	local := t.TempDir()
	t.Setenv("APPDATA", roaming)
	t.Setenv("LOCALAPPDATA", local)
	appData := filepath.Join(roaming, "Claude")
	legacyLog := filepath.Join(appData, "logs", "main.log")
	currentLog := filepath.Join(local, "Claude", "Logs", "main.log")
	startedAt := time.Date(2026, 10, 9, 18, 0, 0, 0, time.Local)
	mustWriteFile(t, legacyLog, "2026-09-09 18:00:00 [info] User logged out\n")
	mustWriteFile(t, currentLog, "")
	if err := os.Chtimes(legacyLog, startedAt.Add(-time.Hour), startedAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	offset := LoginLogOffsetAt(appData)
	mustWriteFile(t, currentLog, "2026-10-09 18:00:01 [info] claude.ai account active and logged in\n")
	if got := LatestAccountLogStateSince(appData, offset, startedAt); got != AccountLogSignedIn {
		t.Fatalf("account log state = %v, want signed in from Local logs", got)
	}
}

func TestWindowsAccountLogPathKeepsLegacyProfiles(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows log layout")
	}
	for _, tc := range []struct {
		name      string
		isolated  bool
		localFile bool
		localDir  bool
	}{
		{name: "local log missing"},
		{name: "isolated profile", isolated: true, localFile: true},
		{name: "local log is a directory", localDir: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roaming := t.TempDir()
			local := t.TempDir()
			t.Setenv("APPDATA", roaming)
			t.Setenv("LOCALAPPDATA", local)
			appData := filepath.Join(roaming, "Claude")
			if tc.isolated {
				appData = t.TempDir()
			}
			localLog := filepath.Join(local, "Claude", "Logs", "main.log")
			if tc.localFile {
				mustWriteFile(t, localLog, "unrelated login")
			}
			if tc.localDir {
				if err := os.MkdirAll(localLog, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if got, want := accountLogPath(appData), filepath.Join(appData, "logs", "main.log"); got != want {
				t.Fatalf("log path = %q, want %q", got, want)
			}
		})
	}
}

func TestIdentityEmailChangedAtRequiresStableIdentity(t *testing.T) {
	store := newTestStore(t)
	saved := syntheticAppData(t, "saved")
	setLocalIdentity(t, saved, "old@example.test", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
	if err := store.Checkpoint("personal", saved); err != nil {
		t.Fatal(err)
	}
	live := syntheticAppData(t, "live")
	setLocalIdentity(t, live, "new@example.test", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
	if !store.IdentityEmailChangedAt("personal", filepath.Join(live, cookiesFile)) {
		t.Fatal("email change was not detected for the same internal identity")
	}

	other := syntheticAppData(t, "other")
	setLocalIdentity(t, other, "new@example.test", "11111111-1111-4111-8111-111111111111", "33333333-3333-4333-8333-333333333333")
	if store.IdentityEmailChangedAt("personal", filepath.Join(other, cookiesFile)) {
		t.Fatal("email change was reported without the stable composite identity")
	}
}

func TestMatchLiveReadsIdentityFromLegacyProfileSnapshot(t *testing.T) {
	store := newTestStore(t)
	saved := syntheticAppData(t, "old-session")
	setLocalIdentity(t, saved, "person@example.test", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
	if err := store.Checkpoint("personal", saved); err != nil {
		t.Fatal(err)
	}
	meta, err := store.loadMeta("personal")
	if err != nil {
		t.Fatal(err)
	}
	meta.AccountUUIDHashes = nil
	meta.IdentityHashes = nil
	if err := store.saveMeta("personal", meta); err != nil {
		t.Fatal(err)
	}
	live := syntheticAppData(t, "renewed-session")
	setLocalIdentity(t, live, "person@example.test", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
	if name, health := store.MatchLive(live); name != "personal" || health != HealthUsable {
		t.Fatalf("match = %q/%s, want personal/usable", name, health)
	}
}

func TestMatchLiveRejectsAmbiguousLocalIdentity(t *testing.T) {
	store := newTestStore(t)
	for _, name := range []string{"first", "second"} {
		appData := syntheticAppData(t, name+"-session")
		setLocalIdentity(t, appData, "shared@example.test", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
		writeAccountFixture(t, appData, "token-"+name, "token-v2-"+name, "account-"+name, "owner-"+name, "dark", true)
		if err := store.Checkpoint(name, appData); err != nil {
			t.Fatal(err)
		}
	}
	live := syntheticAppData(t, "renewed-session")
	setLocalIdentity(t, live, "shared@example.test", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")

	if name, health := store.MatchLive(live); name != "" || health != HealthUsable {
		t.Fatalf("match = %q/%s, want ambiguous usable session", name, health)
	}
}

func TestMatchLivePrefersTrackedCurrentProfileWhenIdentityIsAmbiguous(t *testing.T) {
	store := newTestStore(t)
	for _, name := range []string{"first", "second"} {
		appData := syntheticAppData(t, name+"-session")
		setLocalIdentity(t, appData, "shared@example.test", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
		writeAccountFixture(t, appData, "token-"+name, "token-v2-"+name, "account-"+name, "owner-"+name, "dark", true)
		if err := store.Checkpoint(name, appData); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetCurrent("second"); err != nil {
		t.Fatal(err)
	}
	live := syntheticAppData(t, "renewed-session")
	setLocalIdentity(t, live, "shared@example.test", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")

	if name, health := store.MatchLive(live); name != "second" || health != HealthUsable {
		t.Fatalf("match = %q/%s, want second/usable", name, health)
	}
}

func TestWipePreservesMachineAndGlobalFiles(t *testing.T) {
	store := newTestStore(t)
	appData := syntheticAppData(t, "live")
	for path, content := range map[string]string{deviceIDFile: "device", "config.json": `{"global":"config"}`} {
		mustWriteFile(t, filepath.Join(appData, path), content)
	}
	if err := store.Wipe(appData); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{deviceIDFile, "config.json"} {
		if _, err := os.Stat(filepath.Join(appData, path)); err != nil {
			t.Fatalf("%s was removed", path)
		}
	}
}

func TestProfileCookiesPathResolvesUnderProfile(t *testing.T) {
	store := newTestStore(t)
	got := store.ProfileCookiesPath("work")
	want := filepath.Join(store.profileDir("work"), cookiesFile)
	if got != want {
		t.Fatalf("ProfileCookiesPath = %q, want %q", got, want)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) }
	return store
}

func syntheticAppData(t *testing.T, marker string) string {
	t.Helper()
	dir := t.TempDir()
	createCookiesDBWithMarker(t, filepath.Join(dir, cookiesFile), marker)
	for _, path := range []string{filepath.Join(localStorageDir, leveldbDir, "CURRENT"), filepath.Join(indexedDBDir, "data"), filepath.Join(sessionStorageDir, "data")} {
		mustWriteFile(t, filepath.Join(dir, path), marker)
	}
	return dir
}

func createCookiesDBWithMarker(t *testing.T, path, marker string) {
	t.Helper()
	db := openSQLite(t, path)
	mustExec(t, db, `CREATE TABLE cookies (host_key TEXT, name TEXT, expires_utc INTEGER, value TEXT, encrypted_value BLOB)`)
	mustExec(t, db, `CREATE TABLE fixture_marker (marker TEXT)`)
	mustExec(t, db, `INSERT INTO cookies(host_key, name, expires_utc, value, encrypted_value) VALUES ('.claude.ai', 'sessionKey', 0, ?, ?)`, marker, []byte(marker))
	mustExec(t, db, `INSERT INTO fixture_marker VALUES (?)`, marker)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func setLocalIdentity(t *testing.T, appData, email string, uuids ...string) {
	t.Helper()
	content := "account_profile email_address=" + email + " accountUuid=" + strings.Join(uuids, " organizationUuid=")
	mustWriteFile(t, filepath.Join(appData, indexedDBDir, "https_claude.ai_0.indexeddb.blob", "1", "00", "1"), content)
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), filePerm); err != nil {
		t.Fatal(err)
	}
}

func mustWriteMeta(t *testing.T, path string, meta Meta) {
	t.Helper()
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, path, string(data))
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}
