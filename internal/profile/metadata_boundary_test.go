package profile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMetadataUsesDirectoryIdentity(t *testing.T) {
	for _, declared := range []string{"../outside", "other", ""} {
		t.Run(declared, func(t *testing.T) {
			store := newTestStore(t)
			outside := filepath.Join(store.baseDir, "outside")
			writeRecoveryProfile(t, outside)
			outsidePath := filepath.Join(outside, metaFile)
			before, err := os.ReadFile(outsidePath)
			if err != nil {
				t.Fatal(err)
			}
			writeRecoveryProfile(t, store.profileDir("safe"))
			mustWriteMeta(t, filepath.Join(store.profileDir("safe"), metaFile), Meta{Name: declared, Plan: "team"})

			profiles, err := store.List()
			if err != nil || len(profiles) != 1 || profiles[0].Name != "safe" || profiles[0].Plan != "team" {
				t.Fatalf("List = %+v/%v, want canonical directory and saved plan", profiles, err)
			}
			meta, err := store.BackfillAccountMetadata(profiles[0].Name)
			if err != nil || meta.Name != "safe" || !meta.EmailLookupDone {
				t.Fatalf("BackfillAccountMetadata = %+v/%v", meta, err)
			}
			if err := store.saveMeta("safe", Meta{Name: declared, Email: "updated@example.test"}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(store.profileDir("safe"), metaFile))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &meta); err != nil || meta.Name != "safe" {
				t.Fatalf("persisted name = %q/%v", meta.Name, err)
			}
			after, err := os.ReadFile(outsidePath)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatalf("outside metadata changed: %q/%v", after, err)
			}
		})
	}
}

func TestMetadataMethodsRejectUnsafeNames(t *testing.T) {
	store := newTestStore(t)
	writeRecoveryProfile(t, filepath.Join(store.baseDir, "outside"))
	outsidePath := filepath.Join(store.baseDir, "outside", metaFile)
	before, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"", ".", "..", "../outside", `..\outside`, filepath.Join(store.baseDir, "outside"), "C:outside", "safe:stream", "safe\x00", "safe.", "safe "}
	if runtime.GOOS == "windows" {
		names = append(names, "CON", "NUL", "COM1", "LPT¹")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if validProfileName(name) || store.Exists(name) || store.ProfileCookiesPath(name) != "" {
				t.Fatalf("unsafe name accepted: %q", name)
			}
			if _, err := store.loadMeta(name); err == nil {
				t.Fatal("loadMeta accepted unsafe name")
			}
			if err := store.saveMeta(name, Meta{}); err == nil {
				t.Fatal("saveMeta accepted unsafe name")
			}
			if _, err := store.BackfillAccountMetadata(name); err == nil {
				t.Fatal("BackfillAccountMetadata accepted unsafe name")
			}
			if _, err := store.UpdateAccountEmailFromProfile(name); err == nil {
				t.Fatal("UpdateAccountEmailFromProfile accepted unsafe name")
			}
			if _, err := store.UpdateAccountEmailFromLive(name, t.TempDir()); err == nil {
				t.Fatal("UpdateAccountEmailFromLive accepted unsafe name")
			}
		})
	}
	after, err := os.ReadFile(outsidePath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("outside metadata changed: %q/%v", after, err)
	}
}

func TestMetadataLegacyFallbackAndValidNames(t *testing.T) {
	store := newTestStore(t)
	for _, name := range []string{"legacy", "missing", "malformed", "Cont personal", "Pescuit-țară"} {
		writeRecoveryProfile(t, store.profileDir(name))
		if !validProfileName(name) || !store.Exists(name) || store.ProfileCookiesPath(name) != filepath.Join(store.profileDir(name), cookiesFile) {
			t.Fatalf("valid name rejected: %q", name)
		}
	}
	if err := os.Remove(filepath.Join(store.profileDir("missing"), metaFile)); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(store.profileDir("malformed"), metaFile), `{`)
	profiles, err := store.List()
	if err != nil || len(profiles) != 5 {
		t.Fatalf("List legacy entries = %+v/%v", profiles, err)
	}
	for _, meta := range profiles {
		if !validProfileName(meta.Name) || meta.FormatVersion != 0 {
			t.Fatalf("legacy metadata changed: %+v", meta)
		}
	}
	meta, err := store.BackfillAccountMetadata("legacy")
	if err != nil || meta.Name != "legacy" || !meta.EmailLookupDone {
		t.Fatalf("legacy backfill = %+v/%v", meta, err)
	}
}
