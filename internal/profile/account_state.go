package profile

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	accountStateFile    = "account-state.json"
	accountStateVersion = 1
	claudeConfigFile    = "config.json"
	coworkOpsFile       = "cowork-enabled-cli-ops.json"
	coworkOwnerKey      = "ownerAccountId"
	accountUUIDKey      = "lastKnownAccountUuid"
)

var accountConfigKeys = []string{
	"oauth:tokenCache",
	"oauth:tokenCacheV2",
	accountUUIDKey,
}

type accountState struct {
	Version     int                        `json:"version"`
	Config      map[string]json.RawMessage `json:"config,omitempty"`
	CoworkOwner json.RawMessage            `json:"cowork_owner,omitempty"`
}

func captureAccountState(appDataPath string) (accountState, error) {
	state := accountState{Version: accountStateVersion, Config: make(map[string]json.RawMessage)}
	config, _, err := readJSONObject(filepath.Join(appDataPath, claudeConfigFile))
	if err != nil {
		return accountState{}, fmt.Errorf("read Claude account configuration: %w", err)
	}
	for _, key := range accountConfigKeys {
		if value, ok := config[key]; ok {
			if err := validateAccountConfigValue(key, value); err != nil {
				return accountState{}, err
			}
			state.Config[key] = cloneRawMessage(value)
		}
	}
	cowork, _, err := readJSONObject(filepath.Join(appDataPath, coworkOpsFile))
	if err != nil {
		return accountState{}, fmt.Errorf("read Cowork account configuration: %w", err)
	}
	if value, ok := cowork[coworkOwnerKey]; ok {
		state.CoworkOwner = cloneRawMessage(value)
	}
	return state, nil
}

func writeAccountState(path string, state accountState) (string, error) {
	state.Version = accountStateVersion
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, data); err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func readAccountState(path string) (accountState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return accountState{}, err
	}
	var state accountState
	if err := json.Unmarshal(data, &state); err != nil {
		return accountState{}, err
	}
	if state.Version != accountStateVersion {
		return accountState{}, fmt.Errorf("unsupported account state version %d", state.Version)
	}
	for key := range state.Config {
		if !managedAccountConfigKey(key) {
			return accountState{}, fmt.Errorf("unsupported account configuration key %q", key)
		}
		if err := validateAccountConfigValue(key, state.Config[key]); err != nil {
			return accountState{}, err
		}
	}
	return state, nil
}

func primaryAccountUUIDHash(state accountState) string {
	if hash := accountIdentifierHash(state.Config[accountUUIDKey]); hash != "" {
		return hash
	}
	return accountIdentifierHash(state.CoworkOwner)
}

func primaryAccountUUIDHashAt(appDataPath string) string {
	config, _, err := readJSONObject(filepath.Join(appDataPath, claudeConfigFile))
	if err == nil {
		if hash := accountIdentifierHash(config[accountUUIDKey]); hash != "" {
			return hash
		}
	}
	cowork, _, err := readJSONObject(filepath.Join(appDataPath, coworkOpsFile))
	if err != nil {
		return ""
	}
	return accountIdentifierHash(cowork[coworkOwnerKey])
}

func HasPersistedAccountStateAt(appDataPath string) bool {
	return PersistedAccountStateFingerprintAt(appDataPath) != ""
}

func PersistedAccountStateFingerprintAt(appDataPath string) string {
	state, err := captureAccountState(appDataPath)
	if err != nil || primaryAccountUUIDHash(state) == "" {
		return ""
	}
	if len(state.Config["oauth:tokenCache"]) == 0 && len(state.Config["oauth:tokenCacheV2"]) == 0 {
		return ""
	}
	data, err := json.Marshal(state)
	if err != nil {
		return ""
	}
	return digestBytes(data)
}

func accountIdentifierHash(value json.RawMessage) string {
	var identifier string
	if len(value) == 0 || json.Unmarshal(value, &identifier) != nil {
		return ""
	}
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	if identifier == "" || len(identifier) > 256 {
		return ""
	}
	return identityHash([]byte(identifier))
}

func prepareAccountFiles(appDataPath, stagePath string, state accountState) error {
	configPath := filepath.Join(appDataPath, claudeConfigFile)
	config, configExists, err := readJSONObject(configPath)
	if err != nil {
		return fmt.Errorf("read live Claude configuration: %w", err)
	}
	for _, key := range accountConfigKeys {
		delete(config, key)
	}
	for key, value := range state.Config {
		config[key] = cloneRawMessage(value)
	}
	if configExists || len(config) > 0 {
		if err := writeJSONAtomic(filepath.Join(stagePath, claudeConfigFile), config); err != nil {
			return fmt.Errorf("stage Claude account configuration: %w", err)
		}
	}

	coworkPath := filepath.Join(appDataPath, coworkOpsFile)
	cowork, coworkExists, err := readJSONObject(coworkPath)
	if err != nil {
		return fmt.Errorf("read live Cowork configuration: %w", err)
	}
	delete(cowork, coworkOwnerKey)
	if len(state.CoworkOwner) > 0 {
		cowork[coworkOwnerKey] = cloneRawMessage(state.CoworkOwner)
	}
	if coworkExists || len(cowork) > 0 {
		if err := writeJSONAtomic(filepath.Join(stagePath, coworkOpsFile), cowork); err != nil {
			return fmt.Errorf("stage Cowork account configuration: %w", err)
		}
	}
	return nil
}

func replaceAccountFiles(appDataPath string, state accountState) error {
	stage, err := os.MkdirTemp(appDataPath, ".claude-account-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := prepareAccountFiles(appDataPath, stage, state); err != nil {
		return err
	}
	rollback, err := os.MkdirTemp(appDataPath, ".claude-account-rollback-")
	if err != nil {
		return err
	}
	recovery := pathRollback{directory: rollback}
	defer recovery.cleanup()
	for _, name := range []string{claudeConfigFile, coworkOpsFile} {
		live := filepath.Join(appDataPath, name)
		backup := filepath.Join(rollback, name)
		if err := recovery.move(live, backup); err != nil {
			return recovery.restore(err)
		}
		staged := filepath.Join(stage, name)
		if _, err := os.Stat(staged); err == nil {
			if err := renamePath(staged, live); err != nil {
				return recovery.restore(err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return recovery.restore(err)
		}
	}
	recovery.completed = true
	return nil
}

var ErrRecoveryRequired = errors.New("manual recovery required")

type movedPath struct {
	live     string
	backup   string
	existed  bool
	restored bool
}

type pathRollback struct {
	directory string
	moved     []movedPath
	completed bool
}

func (r *pathRollback) move(live, backup string) error {
	entry := movedPath{live: live, backup: backup}
	if _, err := os.Stat(live); err == nil {
		if err := renamePath(live, backup); err != nil {
			return err
		}
		entry.existed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	r.moved = append(r.moved, entry)
	return nil
}

func (r *pathRollback) restore(cause error) error {
	var failures []error
	for index := len(r.moved) - 1; index >= 0; index-- {
		entry := &r.moved[index]
		if entry.restored {
			continue
		}
		// Check the retained original before removing its replacement.
		if entry.existed {
			if _, err := os.Stat(entry.backup); err != nil {
				failures = append(failures, fmt.Errorf("read retained state %s: %w", entry.backup, err))
				continue
			}
		}
		if err := os.RemoveAll(entry.live); err != nil {
			failures = append(failures, fmt.Errorf("remove replacement %s: %w", entry.live, err))
			continue
		}
		if entry.existed {
			if err := os.MkdirAll(filepath.Dir(entry.live), dirPerm); err != nil {
				failures = append(failures, err)
				continue
			}
			if err := renamePath(entry.backup, entry.live); err != nil {
				failures = append(failures, fmt.Errorf("restore original %s: %w", entry.live, err))
				continue
			}
		}
		entry.restored = true
	}
	if len(failures) > 0 {
		return errors.Join(cause, ErrRecoveryRequired, fmt.Errorf("original state retained in %s; keep Claude closed until recovery: %w", r.directory, errors.Join(failures...)))
	}
	r.completed = true
	return cause
}

func (r *pathRollback) cleanup() {
	if r.completed || len(r.moved) == 0 {
		_ = os.RemoveAll(r.directory)
	}
}

func readJSONObject(path string) (map[string]json.RawMessage, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]json.RawMessage), false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, true, err
	}
	if value == nil {
		value = make(map[string]json.RawMessage)
	}
	return value, true, nil
}

func managedAccountConfigKey(key string) bool {
	for _, allowed := range accountConfigKeys {
		if key == allowed {
			return true
		}
	}
	return false
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func validateAccountConfigValue(key string, value json.RawMessage) error {
	if key != "oauth:tokenCache" && key != "oauth:tokenCacheV2" {
		return nil
	}
	var encoded string
	if err := json.Unmarshal(value, &encoded); err != nil {
		return fmt.Errorf("%s is not an encrypted string", key)
	}
	encrypted, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(encrypted) < 3 {
		return fmt.Errorf("%s is not Chromium-encrypted", key)
	}
	prefix := string(encrypted[:3])
	if prefix != "v10" && prefix != "v11" && prefix != "v20" {
		return fmt.Errorf("%s uses an unsupported encryption format", key)
	}
	return nil
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:])
}
