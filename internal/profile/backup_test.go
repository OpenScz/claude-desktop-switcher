package profile

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEncryptedBackupRoundTripRestoresProfilesAndTracking(t *testing.T) {
	source := newTestStore(t)
	appData := syntheticAppData(t, "backup-secret")
	if err := source.Checkpoint("personal", appData); err != nil {
		t.Fatal(err)
	}
	if err := source.SetCurrent("personal"); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "accounts.csb")
	if err := os.WriteFile(backup, []byte("old backup"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := source.Export(backup, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("backup-secret")) || bytes.Contains(data, []byte("sessionKey")) {
		t.Fatal("backup leaked profile contents in cleartext")
	}
	if protection, err := DetectBackupProtection(backup); err != nil || protection != BackupProtectionPassword {
		t.Fatalf("protection = %q/%v", protection, err)
	}

	destination, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := destination.ImportAuto(backup, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if !destination.Exists("personal") {
		t.Fatal("imported profile is missing")
	}
	if current, err := destination.Current(); err != nil || current != "personal" {
		t.Fatalf("current = %q/%v, want personal", current, err)
	}
	if got := destination.Inspect("personal").Health; got != HealthUsable {
		t.Fatalf("imported health = %s, want usable", got)
	}
	if _, err := readAccountState(filepath.Join(destination.profileDir("personal"), accountStateFile)); err != nil {
		t.Fatalf("imported account authentication state: %v", err)
	}
	local := filepath.Join(destination.profileDir("personal"), localStorageDir, leveldbDir, "CURRENT")
	if got, err := os.ReadFile(local); err != nil || string(got) != "backup-secret" {
		t.Fatalf("imported Local Storage = %q/%v", got, err)
	}
	for _, path := range []string{filepath.Join(indexedDBDir, "data"), filepath.Join(sessionStorageDir, "data")} {
		if got, err := os.ReadFile(filepath.Join(destination.profileDir("personal"), path)); err != nil || string(got) != "backup-secret" {
			t.Fatalf("imported %s = %q/%v", path, got, err)
		}
	}
}

func TestBackupOmitsStaleActiveProfileMarker(t *testing.T) {
	source := newTestStore(t)
	if err := source.Checkpoint("personal", syntheticAppData(t, "stale-current")); err != nil {
		t.Fatal(err)
	}
	if err := source.SetCurrent("missing-profile"); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "accounts.csb")
	if err := source.Export(backup, "secret"); err != nil {
		t.Fatal(err)
	}
	destination := newTestStore(t)
	if err := destination.Import(backup, "secret"); err != nil {
		t.Fatal(err)
	}
	if !destination.Exists("personal") {
		t.Fatal("valid profile was not restored")
	}
	if current, err := destination.Current(); err != nil || current != "" {
		t.Fatalf("current = %q/%v, want empty marker", current, err)
	}
}

func TestBackupImportRejectsDuplicatePrimaryAccounts(t *testing.T) {
	profiles := t.TempDir()
	hash := identityHash([]byte("11111111-1111-4111-8111-111111111111"))
	for _, name := range []string{"first", "second"} {
		path := filepath.Join(profiles, name)
		if err := os.Mkdir(path, dirPerm); err != nil {
			t.Fatal(err)
		}
		if err := writeJSONAtomic(filepath.Join(path, metaFile), Meta{Name: name, AccountUUIDHash: hash}); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateUniqueBackupAccounts(profiles); err == nil {
		t.Fatal("duplicate account backup was accepted")
	}
}

func TestBackupImportRejectsDuplicateSessionsWithoutPrimaryIdentity(t *testing.T) {
	profiles := t.TempDir()
	for _, name := range []string{"first", "second"} {
		path := filepath.Join(profiles, name)
		if err := os.Mkdir(path, dirPerm); err != nil {
			t.Fatal(err)
		}
		if err := writeJSONAtomic(filepath.Join(path, metaFile), Meta{Name: name, SessionDigest: "same-session"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateUniqueBackupAccounts(profiles); err == nil {
		t.Fatal("duplicate session backup was accepted")
	}
}

func TestEncryptedBackupRejectsWrongPasswordWithoutReplacingProfiles(t *testing.T) {
	source := newTestStore(t)
	if err := source.Checkpoint("source", syntheticAppData(t, "source")); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "accounts.csb")
	if err := source.Export(backup, "secret"); err != nil {
		t.Fatal(err)
	}
	destination := newTestStore(t)
	if err := destination.Checkpoint("keep", syntheticAppData(t, "keep")); err != nil {
		t.Fatal(err)
	}
	if err := destination.Import(backup, "wrong"); err == nil {
		t.Fatal("wrong password should fail")
	}
	if !destination.Exists("keep") || destination.Exists("source") {
		t.Fatal("wrong-password import changed existing profiles")
	}
}

func TestSuccessfulImportReplacesExistingProfiles(t *testing.T) {
	source := newTestStore(t)
	if err := source.Checkpoint("source", syntheticAppData(t, "source-replacement")); err != nil {
		t.Fatal(err)
	}
	if err := source.SetCurrent("source"); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "accounts.csb")
	if err := source.Export(backup, "secret"); err != nil {
		t.Fatal(err)
	}

	destination := newTestStore(t)
	if err := destination.Checkpoint("old", syntheticAppData(t, "old-replacement")); err != nil {
		t.Fatal(err)
	}
	if err := destination.SetCurrent("old"); err != nil {
		t.Fatal(err)
	}
	if err := destination.Import(backup, "secret"); err != nil {
		t.Fatal(err)
	}
	if destination.Exists("old") || !destination.Exists("source") {
		t.Fatal("successful import did not replace the previous profile set")
	}
	if current, err := destination.Current(); err != nil || current != "source" {
		t.Fatalf("current = %q/%v, want source", current, err)
	}
}

func TestExportRejectsLegacyProfile(t *testing.T) {
	store := newTestStore(t)
	if err := store.Checkpoint("legacy", syntheticAppData(t, "legacy")); err != nil {
		t.Fatal(err)
	}
	meta, err := store.loadMeta("legacy")
	if err != nil {
		t.Fatal(err)
	}
	meta.FormatVersion = 2
	if err := store.saveMeta("legacy", meta); err != nil {
		t.Fatal(err)
	}

	err = store.Export(filepath.Join(t.TempDir(), "legacy.csb"), "secret")
	if err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("Export error = %v, want incomplete legacy profile", err)
	}
}

func TestReadBackupFileRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.csb")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxBackupFileSize + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readBackupFile(path); err == nil {
		t.Fatal("oversized backup was accepted")
	}
}

func TestImportRejectsBackupWithTooManyEntries(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for index := 0; index <= maxBackupEntries; index++ {
		entry, err := writer.Create("profiles/p" + strconv.Itoa(index) + "/meta.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t)
	if err := store.installBackupArchive(archive.Bytes()); err == nil {
		t.Fatal("backup with too many entries was accepted")
	}
}

func TestStoreRollsBackImportInterruptedBeforeCurrentMarker(t *testing.T) {
	base := t.TempDir()
	profiles := filepath.Join(base, profilesDirName)
	writeRecoveryProfile(t, filepath.Join(profiles, "old"))
	mustWriteFile(t, filepath.Join(base, currentFileName), "old")
	oldProfiles := filepath.Join(base, importProfilesBackupName)
	oldCurrent := filepath.Join(base, importCurrentBackupName)
	if err := os.Rename(profiles, oldProfiles); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(base, currentFileName), oldCurrent); err != nil {
		t.Fatal(err)
	}
	writeRecoveryProfile(t, filepath.Join(profiles, "new"))
	stage := filepath.Join(base, importStagePrefix+"dead")
	if err := os.MkdirAll(stage, dirPerm); err != nil {
		t.Fatal(err)
	}

	store, err := newStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if !store.Exists("old") || store.Exists("new") {
		t.Fatal("interrupted import did not restore the previous profile set")
	}
	if current, err := store.Current(); err != nil || current != "old" {
		t.Fatalf("current = %q/%v, want old", current, err)
	}
	for _, path := range []string{oldProfiles, oldCurrent, stage} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("recovery artifact still exists at %s: %v", path, err)
		}
	}
}

func TestStoreFinishesImportInterruptedAfterCurrentMarker(t *testing.T) {
	base := t.TempDir()
	profiles := filepath.Join(base, profilesDirName)
	oldProfiles := filepath.Join(base, importProfilesBackupName)
	oldCurrent := filepath.Join(base, importCurrentBackupName)
	writeRecoveryProfile(t, filepath.Join(oldProfiles, "old"))
	writeRecoveryProfile(t, filepath.Join(profiles, "new"))
	mustWriteFile(t, oldCurrent, "old")
	mustWriteFile(t, filepath.Join(base, currentFileName), "new")

	store, err := newStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if !store.Exists("new") || store.Exists("old") {
		t.Fatal("completed imported profile set was not preserved")
	}
	if current, err := store.Current(); err != nil || current != "new" {
		t.Fatalf("current = %q/%v, want new", current, err)
	}
	for _, path := range []string{oldProfiles, oldCurrent} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("completed import backup still exists at %s: %v", path, err)
		}
	}
}

func TestStoreRestoresImportBackupWhenProfilesWereRecreatedEmpty(t *testing.T) {
	base := t.TempDir()
	profiles := filepath.Join(base, profilesDirName)
	oldProfiles := filepath.Join(base, importProfilesBackupName)
	writeRecoveryProfile(t, filepath.Join(oldProfiles, "old"))
	if err := os.MkdirAll(profiles, dirPerm); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(base, currentFileName), "old")

	store, err := newStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if !store.Exists("old") {
		t.Fatal("previous profiles were not restored from the interrupted import")
	}
	if current, err := store.Current(); err != nil || current != "old" {
		t.Fatalf("current = %q/%v, want old", current, err)
	}
	if _, err := os.Stat(oldProfiles); !os.IsNotExist(err) {
		t.Fatalf("import backup still exists: %v", err)
	}
}

func writeRecoveryProfile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, dirPerm); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(path, metaFile), `{}`)
}

func TestEncryptedImportRejectsMismatchedMetadataName(t *testing.T) {
	store := newTestStore(t)
	writeRecoveryProfile(t, store.profileDir("keep"))
	outside := filepath.Join(store.baseDir, "outside")
	writeRecoveryProfile(t, outside)
	outsidePath := filepath.Join(outside, metaFile)
	before, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", outside, "other"} {
		t.Run(name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := zip.NewWriter(&archive)
			if err := addBackupBytes(writer, "manifest.json", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			if err := addBackupBytes(writer, currentFileName, nil); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(Meta{Name: name})
			if err != nil {
				t.Fatal(err)
			}
			if err := addBackupBytes(writer, "profiles/safe/meta.json", data); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			encrypted, err := encryptBackup(archive.Bytes(), "test password")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "crafted.csb")
			if err := os.WriteFile(path, encrypted, filePerm); err != nil {
				t.Fatal(err)
			}
			if err := store.Import(path, "test password"); err == nil {
				profiles, listErr := store.List()
				if listErr != nil {
					t.Fatal(listErr)
				}
				for _, meta := range profiles {
					_, _ = store.BackfillAccountMetadata(meta.Name)
				}
				t.Error("import accepted mismatched metadata name")
			}
			if !store.Exists("keep") || store.Exists("safe") {
				t.Error("rejected import replaced existing profiles")
			}
			after, err := os.ReadFile(outsidePath)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatalf("outside metadata changed: %q/%v", after, err)
			}
		})
	}
}

func TestImportPreservesLegacyMetadataFallback(t *testing.T) {
	for _, metadata := range []string{"", `{}`, `{`, `{"name":"safe"}`} {
		t.Run(metadata, func(t *testing.T) {
			var archive bytes.Buffer
			writer := zip.NewWriter(&archive)
			for _, entry := range []struct{ name, data string }{
				{"manifest.json", `{}`},
				{currentFileName, ""},
				{"profiles/safe/placeholder", ""},
			} {
				if err := addBackupBytes(writer, entry.name, []byte(entry.data)); err != nil {
					t.Fatal(err)
				}
			}
			if metadata != "" {
				if err := addBackupBytes(writer, "profiles/safe/meta.json", []byte(metadata)); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			store := newTestStore(t)
			if err := store.installBackupArchive(archive.Bytes()); err != nil {
				t.Fatal(err)
			}
			profiles, err := store.List()
			if err != nil || len(profiles) != 1 || profiles[0].Name != "safe" {
				t.Fatalf("legacy imported profiles = %+v/%v", profiles, err)
			}
		})
	}
}
