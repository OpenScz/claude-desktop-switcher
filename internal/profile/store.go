package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	storeDirName        = ".claude-swap"
	profilesDirName     = "profiles"
	currentFileName     = "current"
	cookiesFile         = "Cookies"
	cookiesJournalFile  = "Cookies-journal"
	cookiesWALFile      = "Cookies-wal"
	cookiesSHMFile      = "Cookies-shm"
	localStorageDir     = "Local Storage"
	leveldbDir          = "leveldb"
	indexedDBDir        = "IndexedDB"
	sessionStorageDir   = "Session Storage"
	deviceIDFile        = "ant-did"
	metaFile            = "meta.json"
	formatVersion       = 4
	digestFormatVersion = 2
	deleteSuffix        = ".delete"

	dirPerm  os.FileMode = 0700
	filePerm os.FileMode = 0600
)

type Meta struct {
	Name               string    `json:"name"`
	CreatedAt          time.Time `json:"created_at"`
	LastUsed           time.Time `json:"last_used,omitempty"`
	Email              string    `json:"email,omitempty"`
	EmailLookupDone    bool      `json:"email_lookup_done,omitempty"`
	Plan               string    `json:"plan,omitempty"`
	FormatVersion      int       `json:"format_version,omitempty"`
	SavedAt            time.Time `json:"saved_at,omitempty"`
	ObservedHealth     Health    `json:"observed_health,omitempty"`
	CookieDigest       string    `json:"cookie_digest,omitempty"`
	SessionDigest      string    `json:"session_digest,omitempty"`
	AccountFingerprint string    `json:"account_fingerprint,omitempty"`
	AccountUUIDHash    string    `json:"account_uuid_hash,omitempty"`
	AccountUUIDHashes  []string  `json:"account_uuid_hashes,omitempty"`
	IdentityHashes     []string  `json:"identity_hashes,omitempty"`
	AccountStateDigest string    `json:"account_state_digest,omitempty"`
}

type DuplicateAccountError struct {
	ExistingName string
}

func (e *DuplicateAccountError) Error() string {
	return fmt.Sprintf("this Claude account is already saved as %q", e.ExistingName)
}

type Store struct {
	baseDir string
	now     func() time.Time
}

func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return newStore(filepath.Join(home, storeDirName))
}

func newStore(base string) (*Store, error) {
	s := &Store{baseDir: base, now: time.Now}
	if err := os.MkdirAll(base, dirPerm); err != nil {
		return nil, err
	}
	if err := s.recoverImportedProfiles(); err != nil {
		return nil, err
	}
	if err := s.ensureDirectories(); err != nil {
		return nil, err
	}
	if err := s.recoverProfiles(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Exists(name string) bool {
	if !validProfileName(name) {
		return false
	}
	info, err := os.Stat(s.profileDir(name))
	return err == nil && info.IsDir()
}

func (s *Store) Save(name, appDataPath string) error {
	return s.Checkpoint(name, appDataPath)
}

func (s *Store) Checkpoint(name, appDataPath string) error {
	return s.CheckpointAt(name, appDataPath, filepath.Join(appDataPath, cookiesFile))
}

func (s *Store) CheckpointAt(name, appDataPath, live string) error {
	if !validProfileName(name) {
		return fmt.Errorf("invalid profile name %q", name)
	}
	if err := s.ensureDirectories(); err != nil {
		return err
	}
	if inspection := InspectCookies(live, s.now()); inspection.Health != HealthUsable {
		return fmt.Errorf("refuse checkpoint of %s session: %s", inspection.Health, inspection.Reason)
	}
	if err := CheckpointCookies(live); err != nil {
		return fmt.Errorf("checkpoint Cookies WAL: %w", err)
	}
	if inspection := InspectCookies(live, s.now()); inspection.Health != HealthUsable {
		return fmt.Errorf("refuse checkpoint of %s session: %s", inspection.Health, inspection.Reason)
	}

	stage, err := os.MkdirTemp(s.profilesPath(), "."+name+".stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, dirPerm); err != nil {
		return err
	}
	stagedCookies := filepath.Join(stage, cookiesFile)
	if err := copyFile(live, stagedCookies); err != nil {
		return fmt.Errorf("stage cookies: %w", err)
	}
	for _, directory := range []struct {
		rel   string
		label string
	}{
		{filepath.Join(localStorageDir, leveldbDir), "Local Storage"},
		{indexedDBDir, "IndexedDB"},
		{sessionStorageDir, "Session Storage"},
	} {
		liveDirectory := filepath.Join(appDataPath, directory.rel)
		if info, err := os.Stat(liveDirectory); err == nil && info.IsDir() {
			if err := copyDir(liveDirectory, filepath.Join(stage, directory.rel)); err != nil {
				return fmt.Errorf("stage %s: %w", directory.label, err)
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	accountState, err := captureAccountState(appDataPath)
	if err != nil {
		return err
	}
	accountStateDigest, err := writeAccountState(filepath.Join(stage, accountStateFile), accountState)
	if err != nil {
		return fmt.Errorf("stage account authentication state: %w", err)
	}
	digest, err := cookieDigest(stagedCookies)
	if err != nil {
		return err
	}
	evidence, _ := readCookieEvidence(stagedCookies)
	identity := localIdentityAt(stage)
	accountUUIDHash := primaryAccountUUIDHash(accountState)
	if duplicate, err := s.duplicateAccountName(name, accountUUIDHash, evidence.sessionDigest, digest, identity); err != nil {
		return err
	} else if duplicate != "" {
		return &DuplicateAccountError{ExistingName: duplicate}
	}
	email := ""
	if len(identity.emails) == 1 {
		email = identity.emails[0]
	}
	meta := Meta{Name: name, CreatedAt: s.now(), Email: email, EmailLookupDone: true, FormatVersion: formatVersion, SavedAt: s.now(), ObservedHealth: HealthUsable, CookieDigest: digest, SessionDigest: evidence.sessionDigest, AccountFingerprint: evidence.accountFingerprint, AccountUUIDHash: accountUUIDHash, AccountUUIDHashes: identity.uuidHashes, IdentityHashes: identity.emailHashes, AccountStateDigest: accountStateDigest}
	if existing, err := s.loadMeta(name); err == nil {
		meta.CreatedAt = existing.CreatedAt
		meta.LastUsed = existing.LastUsed
		if existing.Email != "" {
			if meta.Email != "" && !strings.EqualFold(meta.Email, existing.Email) {
				meta.IdentityHashes = existing.IdentityHashes
			}
			meta.Email = existing.Email
		}
		meta.Plan = existing.Plan
		if meta.SessionDigest == "" {
			meta.SessionDigest = existing.SessionDigest
		}
		if meta.AccountUUIDHash == "" {
			meta.AccountUUIDHash = existing.AccountUUIDHash
		}
		if len(meta.IdentityHashes) == 0 {
			meta.IdentityHashes = existing.IdentityHashes
		}
		if len(meta.AccountUUIDHashes) == 0 {
			meta.AccountUUIDHashes = existing.AccountUUIDHashes
		}
	}
	if err := writeJSONAtomic(filepath.Join(stage, metaFile), meta); err != nil {
		return err
	}
	if inspection := InspectCookies(stagedCookies, s.now()); inspection.Health != HealthUsable {
		return fmt.Errorf("staged profile is %s: %s", inspection.Health, inspection.Reason)
	}
	if runtime.GOOS != "windows" {
		if err := syncTree(stage); err != nil {
			return err
		}
	}
	if err := securePath(stage); err != nil {
		return err
	}
	return s.commitProfile(name, stage)
}

func (s *Store) Inspect(name string) Inspection {
	if !s.Exists(name) {
		return Inspection{Health: HealthMissing, Reason: "profile is missing"}
	}
	if err := validateSecureTree(s.profileDir(name)); err != nil {
		return Inspection{Health: HealthUnknown, Reason: err.Error()}
	}
	cookies := filepath.Join(s.profileDir(name), cookiesFile)
	inspection := InspectCookies(cookies, s.now())
	if inspection.Health != HealthUsable {
		return inspection
	}
	meta, err := s.loadMeta(name)
	if err == nil && meta.FormatVersion >= digestFormatVersion && meta.CookieDigest != "" {
		digest, err := cookieDigest(cookies)
		if err != nil || digest != meta.CookieDigest {
			return Inspection{Health: HealthUnknown, Reason: "profile integrity digest does not match"}
		}
	}
	if err == nil && meta.FormatVersion >= formatVersion {
		data, readErr := os.ReadFile(filepath.Join(s.profileDir(name), accountStateFile))
		if readErr != nil || meta.AccountStateDigest == "" || digestBytes(data) != meta.AccountStateDigest {
			return Inspection{Health: HealthUnknown, Reason: "account authentication state integrity does not match"}
		}
		if _, readErr := readAccountState(filepath.Join(s.profileDir(name), accountStateFile)); readErr != nil {
			return Inspection{Health: HealthUnknown, Reason: "account authentication state is invalid"}
		}
	}
	return inspection
}

func (s *Store) Restore(name, appDataPath string) error {
	return s.RestoreAt(name, appDataPath, filepath.Join(appDataPath, cookiesFile))
}

func (s *Store) RestoreAt(name, appDataPath, live string) error {
	if !validProfileName(name) {
		return fmt.Errorf("invalid profile name %q", name)
	}
	inspection := s.Inspect(name)
	if inspection.Health != HealthUsable {
		return fmt.Errorf("profile %q is %s: %s", name, inspection.Health, inspection.Reason)
	}
	if err := os.MkdirAll(appDataPath, dirPerm); err != nil {
		return err
	}

	stage, err := os.MkdirTemp(appDataPath, ".claude-restore-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	stageCookies := filepath.Join(stage, cookiesFile)
	if err := copyFile(filepath.Join(s.profileDir(name), cookiesFile), stageCookies); err != nil {
		return fmt.Errorf("stage live cookies: %w", err)
	}
	if got := InspectCookies(stageCookies, s.now()); got.Health != HealthUsable {
		return fmt.Errorf("staged live cookies are %s: %s", got.Health, got.Reason)
	}
	stateDirectories := []struct {
		rel      string
		label    string
		rollback string
	}{
		{filepath.Join(localStorageDir, leveldbDir), "Local Storage", "leveldb"},
		{indexedDBDir, "IndexedDB", "indexeddb"},
		{sessionStorageDir, "Session Storage", "sessionstorage"},
	}
	for _, directory := range stateDirectories {
		snapshotDirectory := filepath.Join(s.profileDir(name), directory.rel)
		if info, err := os.Stat(snapshotDirectory); err == nil && info.IsDir() {
			if err := copyDir(snapshotDirectory, filepath.Join(stage, directory.rel)); err != nil {
				return fmt.Errorf("stage %s: %w", directory.label, err)
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	state := accountState{Version: accountStateVersion}
	if savedState, err := readAccountState(filepath.Join(s.profileDir(name), accountStateFile)); err == nil {
		state = savedState
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read saved account authentication state: %w", err)
	}
	if err := prepareAccountFiles(appDataPath, stage, state); err != nil {
		return err
	}

	rollbackRoot, err := os.MkdirTemp(appDataPath, ".claude-restore-rollback-")
	if err != nil {
		return err
	}
	recovery := pathRollback{directory: rollbackRoot}
	defer recovery.cleanup()
	backupCookies := filepath.Join(rollbackRoot, "cookies")
	backupData := filepath.Join(rollbackRoot, "data")
	if err := os.MkdirAll(backupCookies, dirPerm); err != nil {
		return err
	}
	if err := os.MkdirAll(backupData, dirPerm); err != nil {
		return err
	}

	for _, sidecar := range []string{cookiesJournalFile, cookiesWALFile, cookiesSHMFile} {
		if err := recovery.move(filepath.Join(filepath.Dir(live), sidecar), filepath.Join(backupCookies, sidecar)); err != nil {
			return recovery.restore(fmt.Errorf("retain Cookies sidecar: %w", err))
		}
	}
	if err := recovery.move(live, filepath.Join(backupCookies, cookiesFile)); err != nil {
		return recovery.restore(fmt.Errorf("retain live cookies: %w", err))
	}
	for _, directory := range stateDirectories {
		if err := recovery.move(filepath.Join(appDataPath, directory.rel), filepath.Join(backupData, directory.rollback)); err != nil {
			return recovery.restore(fmt.Errorf("retain live %s: %w", directory.label, err))
		}
	}
	for _, name := range []string{claudeConfigFile, coworkOpsFile} {
		if err := recovery.move(filepath.Join(appDataPath, name), filepath.Join(backupData, name)); err != nil {
			return recovery.restore(fmt.Errorf("retain live account configuration: %w", err))
		}
	}

	if err := renamePath(stageCookies, live); err != nil {
		return recovery.restore(fmt.Errorf("commit live cookies: %w", err))
	}
	if err := os.Chmod(live, filePerm); err != nil {
		return recovery.restore(err)
	}
	for _, directory := range stateDirectories {
		stageDirectory := filepath.Join(stage, directory.rel)
		liveDirectory := filepath.Join(appDataPath, directory.rel)
		if _, err := os.Stat(stageDirectory); err == nil {
			if err := os.MkdirAll(filepath.Dir(liveDirectory), dirPerm); err != nil {
				return recovery.restore(err)
			}
			if err := renamePath(stageDirectory, liveDirectory); err != nil {
				return recovery.restore(fmt.Errorf("commit %s: %w", directory.label, err))
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return recovery.restore(err)
		}
	}
	for _, name := range []string{claudeConfigFile, coworkOpsFile} {
		stageFile := filepath.Join(stage, name)
		liveFile := filepath.Join(appDataPath, name)
		if _, err := os.Stat(stageFile); err == nil {
			if err := renamePath(stageFile, liveFile); err != nil {
				return recovery.restore(fmt.Errorf("commit account configuration: %w", err))
			}
			if err := os.Chmod(liveFile, filePerm); err != nil {
				return recovery.restore(err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return recovery.restore(err)
		}
	}
	previousMeta, metaErr := s.loadMeta(name)
	previousCurrent, currentErr := s.Current()
	if err := s.setLastUsed(name); err != nil {
		return recovery.restore(err)
	}
	if err := s.SetCurrent(name); err != nil {
		if metaErr == nil {
			_ = s.saveMeta(name, previousMeta)
		}
		if currentErr == nil {
			_ = s.SetCurrent(previousCurrent)
		} else {
			_ = os.RemoveAll(filepath.Join(s.baseDir, currentFileName))
		}
		return recovery.restore(err)
	}
	recovery.completed = true
	return nil
}

func (s *Store) Wipe(appDataPath string) error {
	return s.WipeAt(appDataPath, filepath.Join(appDataPath, cookiesFile))
}

func (s *Store) WipeAt(appDataPath, live string) error {
	if err := replaceAccountFiles(appDataPath, accountState{Version: accountStateVersion}); err != nil {
		return fmt.Errorf("wipe account authentication state: %w", err)
	}
	for _, name := range []string{cookiesFile, cookiesJournalFile, cookiesWALFile, cookiesSHMFile} {
		if err := os.Remove(filepath.Join(filepath.Dir(live), name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("wipe %s: %w", name, err)
		}
	}
	return clearVolatile(appDataPath)
}

func HasActiveSessionAt(cookiesPath string) bool {
	return InspectCookies(cookiesPath, time.Now()).Health == HealthUsable
}

func (s *Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.profilesPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	profiles := make([]Meta, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validProfileName(entry.Name()) {
			continue
		}
		meta, err := s.loadMeta(entry.Name())
		if err != nil {
			meta = Meta{Name: entry.Name()}
		}
		meta.ObservedHealth = s.Inspect(entry.Name()).Health
		profiles = append(profiles, meta)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles, nil
}

func (s *Store) IncompleteProfiles() ([]string, error) {
	profiles, err := s.List()
	if err != nil {
		return nil, err
	}
	var incomplete []string
	for _, meta := range profiles {
		if meta.FormatVersion < formatVersion {
			incomplete = append(incomplete, meta.Name)
		}
	}
	return incomplete, nil
}

func (s *Store) MatchLive(appDataPath string) (string, Health) {
	return s.MatchLiveAt(filepath.Join(appDataPath, cookiesFile))
}

func (s *Store) MatchLiveAt(live string) (string, Health) {
	for attempt := 0; attempt < 3; attempt++ {
		name, health := s.matchLiveAt(live)
		if health != HealthUnknown || attempt == 2 {
			return name, health
		}
		time.Sleep(200 * time.Millisecond)
	}
	return "", HealthUnknown
}

func appDataPathForCookies(cookiesPath string) string {
	parent := filepath.Dir(cookiesPath)
	if strings.EqualFold(filepath.Base(parent), "Network") {
		return filepath.Dir(parent)
	}
	return parent
}

// IdentityEmailChangedAt reports a stable internal identity with a different
// email identity. It is intentionally separate from MatchLiveAt so callers can
// ask for confirmation before replacing a saved snapshot.
func (s *Store) IdentityEmailChangedAt(name, live string) bool {
	meta, err := s.loadMeta(name)
	if err != nil {
		return false
	}
	liveAppData := appDataPathForCookies(live)
	liveAccountUUIDHash := primaryAccountUUIDHashAt(liveAppData)
	savedAccountUUIDHash := s.profileAccountUUIDHash(name, meta)
	if liveAccountUUIDHash != "" && savedAccountUUIDHash != "" && liveAccountUUIDHash != savedAccountUUIDHash {
		return false
	}
	liveIdentity := localIdentityAt(liveAppData)
	savedIdentity := localIdentity{uuidHashes: meta.AccountUUIDHashes, emailHashes: meta.IdentityHashes}
	if len(savedIdentity.uuidHashes) == 0 && len(savedIdentity.emailHashes) == 0 {
		savedIdentity = localIdentityAt(s.profileDir(name))
	}
	samePrimaryAccount := liveAccountUUIDHash != "" && liveAccountUUIDHash == savedAccountUUIDHash
	if !samePrimaryAccount && identityOverlapCount(liveIdentity.uuidHashes, savedIdentity.uuidHashes) < 2 {
		return false
	}
	if len(liveIdentity.emailHashes) == 0 || len(savedIdentity.emailHashes) == 0 {
		return false
	}
	return identityOverlapCount(liveIdentity.emailHashes, savedIdentity.emailHashes) == 0
}

func (s *Store) FindByAccountIdentityAt(appDataPath string) (string, error) {
	accountUUIDHash := primaryAccountUUIDHashAt(appDataPath)
	if accountUUIDHash == "" {
		return "", nil
	}
	profiles, err := s.List()
	if err != nil {
		return "", err
	}
	matched := ""
	for _, meta := range profiles {
		if s.profileAccountUUIDHash(meta.Name, meta) == accountUUIDHash && meta.ObservedHealth == HealthUsable {
			if matched != "" && matched != meta.Name {
				return "", nil
			}
			matched = meta.Name
		}
	}
	return matched, nil
}

func (s *Store) matchLiveAt(live string) (string, Health) {
	inspection := InspectCookies(live, s.now())
	if inspection.Health != HealthUsable {
		return "", inspection.Health
	}
	digest, err := cookieDigest(live)
	if err != nil {
		return "", HealthUnknown
	}
	liveEvidence, _ := readCookieEvidence(live)
	liveAppData := appDataPathForCookies(live)
	liveIdentity := localIdentityAt(liveAppData)
	liveAccountUUIDHash := primaryAccountUUIDHashAt(liveAppData)
	profiles, err := s.List()
	if err != nil {
		return "", HealthUnknown
	}
	identityMatch := ""
	identityScore := 0
	var identityMatches []string
	for _, meta := range profiles {
		if s.Inspect(meta.Name).Health != HealthUsable {
			continue
		}
		profileAccountUUIDHash := s.profileAccountUUIDHash(meta.Name, meta)
		if liveAccountUUIDHash != "" && profileAccountUUIDHash != "" {
			if liveAccountUUIDHash == profileAccountUUIDHash {
				return meta.Name, HealthUsable
			}
			continue
		}
		profileSessionDigest := meta.SessionDigest
		if profileSessionDigest == "" {
			evidence, _ := readCookieEvidence(filepath.Join(s.profileDir(meta.Name), cookiesFile))
			if profileSessionDigest == "" {
				profileSessionDigest = evidence.sessionDigest
			}
		}
		if liveEvidence.sessionDigest != "" && profileSessionDigest == liveEvidence.sessionDigest {
			return meta.Name, HealthUsable
		}
		profileDigest := meta.CookieDigest
		if profileDigest == "" {
			profileDigest, _ = cookieDigest(filepath.Join(s.profileDir(meta.Name), cookiesFile))
		}
		if profileDigest == digest {
			return meta.Name, HealthUsable
		}
		profileIdentity := localIdentity{uuidHashes: meta.AccountUUIDHashes, emailHashes: meta.IdentityHashes}
		if len(profileIdentity.uuidHashes) == 0 && len(profileIdentity.emailHashes) == 0 {
			profileIdentity = localIdentityAt(s.profileDir(meta.Name))
		}
		score := identityMatchScore(liveIdentity, profileIdentity)
		if score > 0 {
			switch {
			case score > identityScore:
				identityMatch = meta.Name
				identityScore = score
				identityMatches = []string{meta.Name}
			case score == identityScore:
				identityMatches = append(identityMatches, meta.Name)
			}
		}
	}
	if identityMatch != "" && len(identityMatches) == 1 {
		return identityMatch, HealthUsable
	}
	if len(identityMatches) > 1 {
		current, _ := s.Current()
		for _, name := range identityMatches {
			if name == current {
				return current, HealthUsable
			}
		}
	}
	return "", HealthUsable
}

func (s *Store) UpdateAccountEmailFromProfile(name string) (string, error) {
	meta, err := s.loadMeta(name)
	if err != nil {
		return "", err
	}
	identity := localIdentityAt(s.profileDir(name))
	if len(identity.emails) != 1 {
		return "", errors.New("saved profile does not contain one unambiguous account email")
	}
	meta.Email = identity.emails[0]
	meta.EmailLookupDone = true
	meta.IdentityHashes = identity.emailHashes
	if err := s.saveMeta(name, meta); err != nil {
		return "", err
	}
	return meta.Email, nil
}

func (s *Store) UpdateAccountEmailFromLive(name, appDataPath string) (string, error) {
	meta, err := s.loadMeta(name)
	if err != nil {
		return "", err
	}
	liveAccountUUIDHash := primaryAccountUUIDHashAt(appDataPath)
	if liveAccountUUIDHash == "" || liveAccountUUIDHash != s.profileAccountUUIDHash(name, meta) {
		return "", errors.New("live Claude account does not match the saved profile")
	}
	identity := localIdentityAt(appDataPath)
	if len(identity.emails) != 1 {
		return "", errors.New("live Claude data does not contain one unambiguous account email")
	}
	meta.Email = identity.emails[0]
	meta.EmailLookupDone = true
	meta.IdentityHashes = identity.emailHashes
	if err := s.saveMeta(name, meta); err != nil {
		return "", err
	}
	return meta.Email, nil
}

func (s *Store) BackfillAccountMetadata(name string) (Meta, error) {
	meta, err := s.loadMeta(name)
	if err != nil {
		return Meta{}, err
	}
	changed := false
	if meta.Email == "" && !meta.EmailLookupDone {
		if email := AccountEmailAt(s.profileDir(name)); email != "" {
			meta.Email = email
		}
		meta.EmailLookupDone = true
		changed = true
	}
	if hash := s.profileAccountUUIDHash(name, meta); hash != "" {
		if meta.AccountUUIDHash != hash {
			meta.AccountUUIDHash = hash
			changed = true
		}
	}
	if changed {
		if err := s.saveMeta(name, meta); err != nil {
			return Meta{}, err
		}
	}
	return meta, nil
}

func (s *Store) duplicateAccountName(name, accountUUIDHash, sessionDigest, cookieDigest string, identity localIdentity) (string, error) {
	entries, err := os.ReadDir(s.profilesPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == name {
			continue
		}
		meta, err := s.loadMeta(entry.Name())
		if err != nil {
			continue
		}
		existingAccountUUIDHash := s.profileAccountUUIDHash(entry.Name(), meta)
		if accountUUIDHash != "" && existingAccountUUIDHash != "" {
			if accountUUIDHash == existingAccountUUIDHash {
				return entry.Name(), nil
			}
			continue
		}
		if sessionDigest != "" && sessionDigest == meta.SessionDigest {
			return entry.Name(), nil
		}
		if cookieDigest != "" && cookieDigest == meta.CookieDigest {
			return entry.Name(), nil
		}
		existingIdentity := localIdentity{uuidHashes: meta.AccountUUIDHashes}
		if len(existingIdentity.uuidHashes) == 0 {
			existingIdentity = localIdentityAt(s.profileDir(entry.Name()))
		}
		if identityOverlapCount(identity.uuidHashes, existingIdentity.uuidHashes) >= 2 {
			return entry.Name(), nil
		}
	}
	return "", nil
}

func (s *Store) profileAccountUUIDHash(name string, meta Meta) string {
	state, err := readAccountState(filepath.Join(s.profileDir(name), accountStateFile))
	if err == nil {
		if hash := primaryAccountUUIDHash(state); hash != "" {
			return hash
		}
	}
	return meta.AccountUUIDHash
}

func (s *Store) Delete(name string) error {
	if !validProfileName(name) {
		return fmt.Errorf("invalid profile name %q", name)
	}
	if !s.Exists(name) {
		return fmt.Errorf("profile %q not found", name)
	}
	current, err := s.Current()
	if errors.Is(err, os.ErrNotExist) {
		current = ""
	} else if err != nil {
		return fmt.Errorf("read active profile marker: %w", err)
	}
	profilePath := s.profileDir(name)
	deletedPath := filepath.Join(s.profilesPath(), "."+name+deleteSuffix)
	if err := os.RemoveAll(deletedPath); err != nil {
		return fmt.Errorf("clean previous account deletion: %w", err)
	}
	if err := renamePath(profilePath, deletedPath); err != nil {
		return fmt.Errorf("prepare account deletion: %w", err)
	}
	rollback := func() {
		_ = renamePath(deletedPath, profilePath)
	}
	if current == name {
		if err := os.Remove(filepath.Join(s.baseDir, currentFileName)); err != nil {
			rollback()
			return fmt.Errorf("clear active profile marker: %w", err)
		}
	}
	if err := syncDir(s.profilesPath()); err != nil {
		if current == name {
			_ = writeFileAtomic(filepath.Join(s.baseDir, currentFileName), []byte(name))
		}
		rollback()
		return err
	}
	if current == name {
		if err := syncDir(s.baseDir); err != nil {
			_ = writeFileAtomic(filepath.Join(s.baseDir, currentFileName), []byte(name))
			rollback()
			return err
		}
	}
	if err := os.RemoveAll(deletedPath); err != nil {
		return fmt.Errorf("account was removed but its temporary cleanup failed: %w", err)
	}
	return syncDir(s.profilesPath())
}

func (s *Store) Current() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.baseDir, currentFileName))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Store) SetCurrent(name string) error {
	if _, err := os.Stat(s.baseDir); errors.Is(err, os.ErrNotExist) {
		if err := s.ensureDirectories(); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.baseDir, currentFileName), []byte(name))
}

func (s *Store) setLastUsed(name string) error {
	meta, err := s.loadMeta(name)
	if err != nil {
		return err
	}
	meta.LastUsed = s.now()
	return s.saveMeta(name, meta)
}

func (s *Store) commitProfile(name, stage string) error {
	final := s.profileDir(name)
	backup := filepath.Join(s.profilesPath(), "."+name+".backup")
	_ = os.RemoveAll(backup)
	hadBackup := false
	if _, err := os.Stat(final); err == nil {
		if err := renamePath(final, backup); err != nil {
			return err
		}
		hadBackup = true
	}
	if err := renamePath(stage, final); err != nil {
		_ = renamePath(backup, final)
		return err
	}
	if err := syncDir(s.profilesPath()); err != nil {
		if hadBackup {
			_ = os.RemoveAll(final)
			_ = renamePath(backup, final)
		}
		return err
	}
	return os.RemoveAll(backup)
}

func (s *Store) recoverProfiles() error {
	entries, err := os.ReadDir(s.profilesPath())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && strings.HasSuffix(name, deleteSuffix) {
			profileName := strings.TrimSuffix(strings.TrimPrefix(name, "."), deleteSuffix)
			current, err := s.Current()
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("recover interrupted account deletion: %w", err)
			}
			if current == profileName {
				final := s.profileDir(profileName)
				if _, err := os.Stat(final); errors.Is(err, os.ErrNotExist) {
					if err := renamePath(filepath.Join(s.profilesPath(), name), final); err != nil {
						return fmt.Errorf("restore account interrupted during deletion: %w", err)
					}
					continue
				} else if err != nil {
					return err
				}
			}
			if err := os.RemoveAll(filepath.Join(s.profilesPath(), name)); err != nil {
				return err
			}
			continue
		}
		if strings.HasSuffix(name, ".backup") && strings.HasPrefix(name, ".") {
			profileName := strings.TrimSuffix(strings.TrimPrefix(name, "."), ".backup")
			final := s.profileDir(profileName)
			if _, err := os.Stat(final); errors.Is(err, os.ErrNotExist) {
				if err := renamePath(filepath.Join(s.profilesPath(), name), final); err != nil {
					return err
				}
			} else {
				_ = os.RemoveAll(filepath.Join(s.profilesPath(), name))
			}
		}
		if strings.Contains(name, ".stage-") && strings.HasPrefix(name, ".") {
			_ = os.RemoveAll(filepath.Join(s.profilesPath(), name))
		}
	}
	return nil
}

func (s *Store) ProfileCookiesPath(name string) string {
	if !validProfileName(name) {
		return ""
	}
	return filepath.Join(s.profileDir(name), cookiesFile)
}

func (s *Store) profileDir(name string) string { return filepath.Join(s.profilesPath(), name) }
func (s *Store) profilesPath() string          { return filepath.Join(s.baseDir, profilesDirName) }

func (s *Store) ensureDirectories() error {
	profiles := s.profilesPath()
	if err := os.MkdirAll(profiles, dirPerm); err != nil {
		return err
	}
	if err := os.Chmod(s.baseDir, dirPerm); err != nil {
		return err
	}
	if err := os.Chmod(profiles, dirPerm); err != nil {
		return err
	}
	if err := securePath(s.baseDir); err != nil {
		return err
	}
	return securePath(profiles)
}

func (s *Store) loadMeta(name string) (Meta, error) {
	if !validProfileName(name) {
		return Meta{}, fmt.Errorf("invalid profile name %q", name)
	}
	data, err := os.ReadFile(filepath.Join(s.profileDir(name), metaFile))
	if err != nil {
		return Meta{}, err
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return Meta{}, err
	}
	meta.Name = name
	return meta, nil
}

func (s *Store) saveMeta(name string, meta Meta) error {
	if !validProfileName(name) {
		return fmt.Errorf("invalid profile name %q", name)
	}
	meta.Name = name
	return writeJSONAtomic(filepath.Join(s.profileDir(name), metaFile), meta)
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(filePerm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := renamePath(tmpPath, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filePerm)
	if err != nil {
		return err
	}
	if err := out.Chmod(filePerm); err != nil {
		out.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, dirPerm); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from := filepath.Join(src, entry.Name())
		to := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(from, to); err != nil {
				return err
			}
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		if err := copyFile(from, to); err != nil {
			return err
		}
	}
	return nil
}

func clearVolatile(appDataPath string) error {
	for _, path := range []string{
		filepath.Join(appDataPath, localStorageDir, leveldbDir),
		filepath.Join(appDataPath, indexedDBDir),
		filepath.Join(appDataPath, sessionStorageDir),
	} {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("clear %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

// validateSecureTree rejects group/other-readable files. Windows has no
// POSIX mode bits — os.FileMode there is synthesized from file attributes
// and duplicates owner bits into group/other, so the check is meaningless;
// Windows enforces access via ACLs instead, which this does not inspect.
func validateSecureTree(root string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&0077 != 0 {
			return fmt.Errorf("unsafe permissions on %s", filepath.Base(path))
		}
		return nil
	})
}

func syncTree(root string) error {
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		// Opened read-write because Windows' FlushFileBuffers (what
		// File.Sync calls) requires a write-capable handle; a read-only
		// os.Open succeeds but Sync() then fails with "Access is denied".
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Sync()
	}); err != nil {
		return err
	}
	return syncDir(root)
}

// syncDir fsyncs a directory's metadata so a prior rename/create is durable.
// Windows has no equivalent: a directory handle opened read-only cannot be
// flushed (FlushFileBuffers returns "Access is denied"), and NTFS journals
// directory changes itself, so the fsync is unnecessary there.
func syncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
