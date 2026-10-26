package profile

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
)

const (
	backupMagic       = "CLAUDE-SWAP-BACKUP\x01"
	localBackupMagic  = "CLAUDE-SWAP-LOCAL\x01"
	backupSaltSize    = 16
	backupNonceSize   = 12
	backupKeySize     = 32
	backupScryptN     = 32768
	backupScryptR     = 8
	backupScryptP     = 1
	maxBackupFileSize = 512 << 20
	maxBackupPayload  = maxBackupFileSize - (1 << 20)
	maxBackupEntry    = 128 << 20
	maxBackupTotal    = 1 << 30
	maxBackupEntries  = 2048
	backupManifestVer = 1

	importProfilesBackupName = ".profiles-import-backup"
	importCurrentBackupName  = ".current-import-backup"
	importStagePrefix        = ".import-stage-"
)

type BackupProtection string

const (
	BackupProtectionPassword BackupProtection = "password"
	BackupProtectionWindows  BackupProtection = "windows-user"
)

type backupManifest struct {
	FormatVersion int       `json:"format_version"`
	CreatedAt     time.Time `json:"created_at"`
	Description   string    `json:"description"`
}

// Export writes every saved profile, its metadata/local session state, and the
// active-profile marker to a password-encrypted archive. The live Claude
// directory is intentionally not copied while it may be open; callers should
// save an untracked live account before exporting.
func (s *Store) Export(path, password string) error {
	if strings.TrimSpace(password) == "" {
		return errors.New("backup password cannot be empty")
	}
	archive, err := s.makeBackupArchive()
	if err != nil {
		return err
	}
	encrypted, err := encryptBackup(archive, password)
	if err != nil {
		return err
	}
	return writeBackupFile(path, encrypted)
}

func (s *Store) ExportLocal(path string) error {
	archive, err := s.makeBackupArchive()
	if err != nil {
		return err
	}
	encrypted, err := encryptLocalBackup(archive)
	if err != nil {
		return err
	}
	return writeBackupFile(path, encrypted)
}

// Import replaces the saved profile set transactionally. It never changes the
// live Claude directory, so the user can inspect the imported list and switch
// explicitly afterward.
func (s *Store) Import(path, password string) error {
	if strings.TrimSpace(password) == "" {
		return errors.New("backup password cannot be empty")
	}
	data, err := readBackupFile(path)
	if err != nil {
		return fmt.Errorf("read backup: %w", err)
	}
	archive, err := decryptBackup(data, password)
	if err != nil {
		return err
	}
	return s.installBackupArchive(archive)
}

func (s *Store) ImportLocal(path string) error {
	data, err := readBackupFile(path)
	if err != nil {
		return fmt.Errorf("read backup: %w", err)
	}
	archive, err := decryptLocalBackup(data)
	if err != nil {
		return err
	}
	return s.installBackupArchive(archive)
}

func (s *Store) ImportAuto(path, password string) error {
	protection, err := DetectBackupProtection(path)
	if err != nil {
		return err
	}
	if protection == BackupProtectionWindows {
		return s.ImportLocal(path)
	}
	return s.Import(path, password)
}

func DetectBackupProtection(path string) (BackupProtection, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read backup: %w", err)
	}
	defer file.Close()
	header := make([]byte, len(localBackupMagic))
	if _, err := io.ReadFull(file, header); err != nil {
		return "", errors.New("invalid Claude Swap backup format")
	}
	switch string(header) {
	case localBackupMagic:
		return BackupProtectionWindows, nil
	case backupMagic[:len(localBackupMagic)]:
		return BackupProtectionPassword, nil
	default:
		return "", errors.New("invalid Claude Swap backup format")
	}
}

func readBackupFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read backup: %w", err)
	}
	if info.Size() > maxBackupFileSize {
		return nil, fmt.Errorf("backup exceeds the %d MiB size limit", maxBackupFileSize>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read backup: %w", err)
	}
	return data, nil
}

func (s *Store) makeBackupArchive() ([]byte, error) {
	incomplete, err := s.IncompleteProfiles()
	if err != nil {
		return nil, err
	}
	if len(incomplete) > 0 {
		return nil, fmt.Errorf("profiles need a complete session refresh before backup: %s", strings.Join(incomplete, ", "))
	}
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)
	manifest, err := json.Marshal(backupManifest{
		FormatVersion: backupManifestVer,
		CreatedAt:     time.Now(),
		Description:   "Claude Desktop Swap saved profiles",
	})
	if err != nil {
		return nil, err
	}
	if err := addBackupBytes(zw, "manifest.json", manifest); err != nil {
		return nil, err
	}
	current, err := os.ReadFile(filepath.Join(s.baseDir, currentFileName))
	if errors.Is(err, os.ErrNotExist) {
		current = nil
	} else if err != nil {
		return nil, fmt.Errorf("read active profile marker: %w", err)
	}
	currentName := strings.TrimSpace(string(current))
	if currentName == "" || !validProfileName(currentName) || !s.Exists(currentName) {
		current = nil
	} else {
		current = []byte(currentName)
	}
	if err := addBackupBytes(zw, currentFileName, current); err != nil {
		return nil, err
	}
	totalSize := uint64(len(manifest) + len(current))
	entryCount := 2
	if err := filepath.Walk(s.profilesPath(), func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported non-regular profile artifact %s", info.Name())
		}
		entryCount++
		if entryCount > maxBackupEntries {
			return errors.New("saved profiles contain too many files for one backup")
		}
		size := uint64(info.Size())
		if size > maxBackupEntry {
			return fmt.Errorf("profile artifact %s is too large for one backup", info.Name())
		}
		if totalSize > maxBackupTotal-size {
			return errors.New("saved profiles exceed the total backup size limit")
		}
		totalSize += size
		rel, err := filepath.Rel(s.baseDir, path)
		if err != nil {
			return err
		}
		return addBackupFile(zw, filepath.ToSlash(rel), path)
	}); err != nil {
		return nil, fmt.Errorf("archive profiles: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if buffer.Len() > maxBackupPayload {
		return nil, fmt.Errorf("compressed backup exceeds the %d MiB size limit", maxBackupPayload>>20)
	}
	return buffer.Bytes(), nil
}

func addBackupBytes(zw *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{Name: filepath.ToSlash(name), Method: zip.Deflate}
	header.SetMode(filePerm)
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func addBackupFile(zw *zip.Writer, name, path string) error {
	header := &zip.FileHeader{Name: filepath.ToSlash(name), Method: zip.Deflate}
	header.SetMode(filePerm)
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	_, err = io.Copy(writer, input)
	return err
}

func encryptBackup(archive []byte, password string) ([]byte, error) {
	salt := make([]byte, backupSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := scrypt.Key([]byte(password), salt, backupScryptN, backupScryptR, backupScryptP, backupKeySize)
	if err != nil {
		return nil, fmt.Errorf("derive backup key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, backupNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, archive, []byte(backupMagic))
	result := make([]byte, 0, len(backupMagic)+len(salt)+len(nonce)+len(ciphertext))
	result = append(result, []byte(backupMagic)...)
	result = append(result, salt...)
	result = append(result, nonce...)
	result = append(result, ciphertext...)
	return result, nil
}

func encryptLocalBackup(archive []byte) ([]byte, error) {
	protected, err := protectForCurrentWindowsUser(archive)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, len(localBackupMagic)+len(protected))
	result = append(result, []byte(localBackupMagic)...)
	result = append(result, protected...)
	return result, nil
}

func decryptLocalBackup(data []byte) ([]byte, error) {
	if len(data) <= len(localBackupMagic) || string(data[:len(localBackupMagic)]) != localBackupMagic {
		return nil, errors.New("invalid local Claude Swap backup format")
	}
	return unprotectForCurrentWindowsUser(data[len(localBackupMagic):])
}

func decryptBackup(data []byte, password string) ([]byte, error) {
	headerSize := len(backupMagic) + backupSaltSize + backupNonceSize
	if len(data) <= headerSize || string(data[:len(backupMagic)]) != backupMagic {
		return nil, errors.New("invalid Claude Swap backup format")
	}
	saltStart := len(backupMagic)
	nonceStart := saltStart + backupSaltSize
	salt := data[saltStart:nonceStart]
	nonce := data[nonceStart:headerSize]
	key, err := scrypt.Key([]byte(password), salt, backupScryptN, backupScryptR, backupScryptP, backupKeySize)
	if err != nil {
		return nil, fmt.Errorf("derive backup key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	archive, err := gcm.Open(nil, nonce, data[headerSize:], []byte(backupMagic))
	if err != nil {
		return nil, errors.New("backup password is incorrect or the file is damaged")
	}
	return archive, nil
}

func writeBackupFile(path string, data []byte) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("backup path cannot be empty")
	}
	if len(data) > maxBackupFileSize {
		return fmt.Errorf("backup exceeds the %d MiB size limit", maxBackupFileSize>>20)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".claude-swap-backup-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(filePerm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceBackupFile(tmpPath, path)
}

func (s *Store) installBackupArchive(archive []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return fmt.Errorf("read backup archive: %w", err)
	}
	stage, err := os.MkdirTemp(s.baseDir, ".import-stage-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	stageProfiles := filepath.Join(stage, profilesDirName)
	if err := os.MkdirAll(stageProfiles, dirPerm); err != nil {
		return err
	}
	seenManifest := false
	seenCurrent := false
	var totalSize uint64
	if len(reader.File) > maxBackupEntries {
		return errors.New("backup contains too many entries")
	}
	for _, entry := range reader.File {
		name, err := safeBackupName(entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().Mode()&os.ModeSymlink != 0 {
			return errors.New("backup contains an unsupported symbolic link")
		}
		if entry.UncompressedSize64 > maxBackupEntry {
			return fmt.Errorf("backup entry %s is too large", name)
		}
		if totalSize > maxBackupTotal-entry.UncompressedSize64 {
			return errors.New("backup contents exceed the total size limit")
		}
		totalSize += entry.UncompressedSize64
		switch name {
		case "manifest.json":
			seenManifest = true
		case currentFileName:
			seenCurrent = true
		}
		target := filepath.Join(stage, filepath.FromSlash(name))
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, dirPerm); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), dirPerm); err != nil {
			return err
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filePerm)
		if err != nil {
			_ = input.Close()
			return err
		}
		written, copyErr := io.Copy(output, io.LimitReader(input, maxBackupEntry+1))
		closeErr := output.Close()
		_ = input.Close()
		if copyErr != nil {
			return copyErr
		}
		if written > maxBackupEntry {
			return fmt.Errorf("backup entry %s exceeds the size limit", name)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if !seenManifest || !seenCurrent {
		return errors.New("backup is missing its manifest or active-profile marker")
	}
	current, err := os.ReadFile(filepath.Join(stage, currentFileName))
	if err != nil {
		return err
	}
	currentName := strings.TrimSpace(string(current))
	if currentName != "" {
		if !validProfileName(currentName) {
			return errors.New("backup active-profile marker is invalid")
		}
		if _, err := os.Stat(filepath.Join(stageProfiles, currentName)); err != nil {
			return errors.New("backup active-profile marker refers to a missing profile")
		}
	}
	entries, err := os.ReadDir(stageProfiles)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validProfileName(entry.Name()) {
			return fmt.Errorf("backup contains invalid profile name %q", entry.Name())
		}
	}
	if err := validateUniqueBackupAccounts(stageProfiles); err != nil {
		return err
	}
	return s.commitImported(stageProfiles, filepath.Join(stage, currentFileName))
}

func validateUniqueBackupAccounts(profilesPath string) error {
	entries, err := os.ReadDir(profilesPath)
	if err != nil {
		return err
	}
	seen := make(map[string]string)
	seenComposite := make(map[string]string)
	seenSession := make(map[string]string)
	seenCookies := make(map[string]string)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		profilePath := filepath.Join(profilesPath, entry.Name())
		data, err := os.ReadFile(filepath.Join(profilePath, metaFile))
		if err != nil {
			continue
		}
		var meta Meta
		if json.Unmarshal(data, &meta) != nil {
			continue
		}
		if meta.Name != "" && meta.Name != entry.Name() {
			return fmt.Errorf("backup profile %q contains mismatched metadata name %q", entry.Name(), meta.Name)
		}
		if existing, ok := seenSession[meta.SessionDigest]; meta.SessionDigest != "" && ok {
			return fmt.Errorf("backup contains the same Claude account in profiles %q and %q", existing, entry.Name())
		}
		if meta.SessionDigest != "" {
			seenSession[meta.SessionDigest] = entry.Name()
		}
		if existing, ok := seenCookies[meta.CookieDigest]; meta.CookieDigest != "" && ok {
			return fmt.Errorf("backup contains the same Claude account in profiles %q and %q", existing, entry.Name())
		}
		if meta.CookieDigest != "" {
			seenCookies[meta.CookieDigest] = entry.Name()
		}
		hash := ""
		state, err := readAccountState(filepath.Join(profilePath, accountStateFile))
		if err == nil {
			hash = primaryAccountUUIDHash(state)
		}
		if hash == "" {
			hash = meta.AccountUUIDHash
		}
		if hash == "" {
			if len(meta.AccountUUIDHashes) < 2 {
				continue
			}
			hashes := append([]string(nil), meta.AccountUUIDHashes...)
			sort.Strings(hashes)
			composite := strings.Join(hashes, "\x00")
			if existing, ok := seenComposite[composite]; ok {
				return fmt.Errorf("backup contains the same Claude account in profiles %q and %q", existing, entry.Name())
			}
			seenComposite[composite] = entry.Name()
			continue
		}
		if existing, ok := seen[hash]; ok {
			return fmt.Errorf("backup contains the same Claude account in profiles %q and %q", existing, entry.Name())
		}
		seen[hash] = entry.Name()
	}
	return nil
}

func safeBackupName(name string) (string, error) {
	name = filepath.ToSlash(filepath.Clean(name))
	if name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return "", fmt.Errorf("backup contains unsafe path %q", name)
	}
	if name != "manifest.json" && name != currentFileName && name != profilesDirName && !strings.HasPrefix(name, profilesDirName+"/") {
		return "", fmt.Errorf("backup contains unsupported path %q", name)
	}
	return name, nil
}

func validProfileName(name string) bool {
	return filepath.IsLocal(name) && filepath.Base(name) == name && !strings.ContainsAny(name, "/\\:\x00") && !strings.HasPrefix(name, ".") && !strings.HasSuffix(name, ".") && !strings.HasSuffix(name, " ")
}

func (s *Store) recoverImportedProfiles() error {
	finalProfiles := s.profilesPath()
	oldProfiles := filepath.Join(s.baseDir, importProfilesBackupName)
	current := filepath.Join(s.baseDir, currentFileName)
	oldCurrent := filepath.Join(s.baseDir, importCurrentBackupName)

	_, oldProfilesErr := os.Stat(oldProfiles)
	oldProfilesExists := oldProfilesErr == nil
	if oldProfilesErr != nil && !errors.Is(oldProfilesErr, os.ErrNotExist) {
		return oldProfilesErr
	}
	_, finalProfilesErr := os.Stat(finalProfiles)
	finalProfilesExists := finalProfilesErr == nil
	if finalProfilesErr != nil && !errors.Is(finalProfilesErr, os.ErrNotExist) {
		return finalProfilesErr
	}
	_, currentErr := os.Stat(current)
	currentExists := currentErr == nil
	if currentErr != nil && !errors.Is(currentErr, os.ErrNotExist) {
		return currentErr
	}
	_, oldCurrentErr := os.Stat(oldCurrent)
	oldCurrentExists := oldCurrentErr == nil
	if oldCurrentErr != nil && !errors.Is(oldCurrentErr, os.ErrNotExist) {
		return oldCurrentErr
	}

	if oldProfilesExists {
		finalHasProfiles := false
		if finalProfilesExists {
			entries, err := os.ReadDir(finalProfiles)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
					finalHasProfiles = true
					break
				}
			}
		}
		oldHasProfiles := false
		entries, err := os.ReadDir(oldProfiles)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				oldHasProfiles = true
				break
			}
		}
		if finalProfilesExists && currentExists && (finalHasProfiles || !oldHasProfiles) {
			if err := os.RemoveAll(oldProfiles); err != nil {
				return err
			}
			if oldCurrentExists {
				if err := os.Remove(oldCurrent); err != nil {
					return err
				}
			}
		} else {
			if finalProfilesExists {
				if err := os.RemoveAll(finalProfiles); err != nil {
					return err
				}
			}
			if err := renamePath(oldProfiles, finalProfiles); err != nil {
				return fmt.Errorf("recover profiles interrupted during import: %w", err)
			}
			if oldCurrentExists {
				if currentExists {
					if err := os.Remove(current); err != nil {
						return err
					}
				}
				if err := renamePath(oldCurrent, current); err != nil {
					return fmt.Errorf("recover active profile interrupted during import: %w", err)
				}
			}
		}
	} else if oldCurrentExists {
		if currentExists {
			if err := os.Remove(oldCurrent); err != nil {
				return err
			}
		} else if err := renamePath(oldCurrent, current); err != nil {
			return fmt.Errorf("recover active profile marker: %w", err)
		}
	}

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), importStagePrefix) {
			if err := os.RemoveAll(filepath.Join(s.baseDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) commitImported(stageProfiles, stageCurrent string) error {
	finalProfiles := s.profilesPath()
	oldProfiles := filepath.Join(s.baseDir, importProfilesBackupName)
	oldCurrent := filepath.Join(s.baseDir, importCurrentBackupName)
	if err := os.RemoveAll(oldProfiles); err != nil {
		return fmt.Errorf("clean previous profile import backup: %w", err)
	}
	if err := os.Remove(oldCurrent); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clean previous active profile import backup: %w", err)
	}
	if err := renamePath(finalProfiles, oldProfiles); err != nil {
		return fmt.Errorf("prepare existing profiles: %w", err)
	}
	hadCurrent := false
	if _, err := os.Stat(filepath.Join(s.baseDir, currentFileName)); err == nil {
		hadCurrent = true
		if err := renamePath(filepath.Join(s.baseDir, currentFileName), oldCurrent); err != nil {
			_ = renamePath(oldProfiles, finalProfiles)
			return fmt.Errorf("prepare active profile marker: %w", err)
		}
	}
	rollback := func() {
		_ = os.RemoveAll(finalProfiles)
		_ = renamePath(oldProfiles, finalProfiles)
		_ = os.Remove(filepath.Join(s.baseDir, currentFileName))
		if hadCurrent {
			_ = renamePath(oldCurrent, filepath.Join(s.baseDir, currentFileName))
		}
	}
	if err := renamePath(stageProfiles, finalProfiles); err != nil {
		rollback()
		return fmt.Errorf("install imported profiles: %w", err)
	}
	if err := renamePath(stageCurrent, filepath.Join(s.baseDir, currentFileName)); err != nil {
		rollback()
		return fmt.Errorf("install active profile marker: %w", err)
	}
	if err := securePath(finalProfiles); err != nil {
		rollback()
		return err
	}
	if err := os.RemoveAll(oldProfiles); err != nil {
		return fmt.Errorf("import completed but previous profiles could not be cleaned: %w", err)
	}
	if err := os.Remove(oldCurrent); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("import completed but previous active profile marker could not be cleaned: %w", err)
	}
	return nil
}
