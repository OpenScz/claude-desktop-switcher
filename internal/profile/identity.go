package profile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	maxIdentityFileBytes  int64 = 16 << 20
	maxIdentityTotalBytes int64 = 64 << 20
	maxLoginLogBytes      int64 = 8 << 20
	identityRecordWindow        = 1024
)

var (
	emailPattern = regexp.MustCompile(`(?i)[a-z0-9._%+\-]{1,64}@[a-z0-9.\-]{1,253}\.[a-z]{2,63}`)
	uuidPattern  = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`)
)

type localIdentity struct {
	uuidHashes  []string
	emailHashes []string
	emails      []string
}

type AccountLogState uint8

const (
	AccountLogUnknown AccountLogState = iota
	AccountLogSignedIn
	AccountLogSignedOut
)

func localIdentityAt(appDataPath string) localIdentity {
	uuids := make(map[string]struct{})
	emails := make(map[string]struct{})
	plainEmails := make(map[string]struct{})
	var total int64
	for _, root := range []string{
		filepath.Join(appDataPath, indexedDBDir),
		filepath.Join(appDataPath, localStorageDir, leveldbDir),
	} {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || total >= maxIdentityTotalBytes {
				return nil
			}
			info, err := entry.Info()
			if err != nil || info.Size() > maxIdentityFileBytes || total+info.Size() > maxIdentityTotalBytes {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			total += int64(len(data))
			lower := bytes.ToLower(data)
			if !bytes.Contains(lower, []byte("email_address")) && !bytes.Contains(lower, []byte("emailaddress")) && !bytes.Contains(lower, []byte("account_profile")) {
				return nil
			}
			for _, value := range uuidPattern.FindAll(lower, -1) {
				uuids[identityHash(value)] = struct{}{}
			}
			for _, value := range identityRecordEmails(lower) {
				normalized := strings.ToLower(string(value))
				emails[identityHash(value)] = struct{}{}
				plainEmails[normalized] = struct{}{}
			}
			return nil
		})
	}
	return localIdentity{uuidHashes: sortedIdentityValues(uuids), emailHashes: sortedIdentityValues(emails), emails: sortedIdentityValues(plainEmails)}
}

func identityRecordEmails(data []byte) [][]byte {
	markers := [][]byte{[]byte("email_address"), []byte("emailaddress"), []byte("account_profile")}
	var result [][]byte
	for _, marker := range markers {
		for offset := 0; offset < len(data); {
			index := bytes.Index(data[offset:], marker)
			if index < 0 {
				break
			}
			index += offset
			start := max(index-identityRecordWindow, 0)
			end := min(index+len(marker)+identityRecordWindow, len(data))
			record := data[start:end]
			if uuidPattern.Find(record) != nil {
				result = append(result, emailPattern.FindAll(record, -1)...)
			}
			offset = index + len(marker)
		}
	}
	return result
}

func AccountEmailAt(appDataPath string) string {
	identity := localIdentityAt(appDataPath)
	if len(identity.emails) != 1 {
		return ""
	}
	return identity.emails[0]
}

func HasLoginEvidenceAt(appDataPath, cookiesPath string) bool {
	if HasActiveSessionAt(cookiesPath) {
		return true
	}
	identity := localIdentityAt(appDataPath)
	return len(identity.emailHashes) > 0 && len(identity.uuidHashes) >= 2
}

func LoginLogOffsetAt(appDataPath string) int64 {
	info, err := os.Stat(accountLogPath(appDataPath))
	if err != nil {
		return 0
	}
	return info.Size()
}

func accountLogPath(appDataPath string) string {
	roaming := os.Getenv("APPDATA")
	local := os.Getenv("LOCALAPPDATA")
	if runtime.GOOS == "windows" && roaming != "" && local != "" &&
		strings.EqualFold(filepath.Clean(appDataPath), filepath.Join(roaming, "Claude")) {
		path := filepath.Join(local, "Claude", "Logs", "main.log")
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return filepath.Join(appDataPath, "logs", "main.log")
}

func LatestAccountLogStateSince(appDataPath string, offset int64, createdAt time.Time) AccountLogState {
	state := AccountLogUnknown
	for _, line := range accountLogLinesSince(appDataPath, offset, createdAt) {
		lower := bytes.ToLower(line)
		switch {
		case loginEventLine(line), bytes.Contains(lower, []byte("claude.ai account active and logged in")):
			state = AccountLogSignedIn
		case bytes.Contains(lower, []byte("user logged out")),
			bytes.Contains(lower, []byte("user is logged out")),
			bytes.Contains(line, []byte("loggedOut: false → true")),
			bytes.Contains(line, []byte("loggedOut: false -> true")):
			state = AccountLogSignedOut
		}
	}
	return state
}

func accountLogLinesSince(appDataPath string, offset int64, createdAt time.Time) [][]byte {
	if createdAt.IsZero() {
		return nil
	}
	path := accountLogPath(appDataPath)
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	if offset < 0 || offset > info.Size() {
		offset = 0
	}
	if info.Size()-offset > maxLoginLogBytes {
		offset = info.Size() - maxLoginLogBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(file, maxLoginLogBytes))
	if err != nil {
		return nil
	}
	var result [][]byte
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) < len("2006-01-02 15:04:05") {
			continue
		}
		eventAt, err := time.ParseInLocation("2006-01-02 15:04:05", string(line[:19]), createdAt.Location())
		if err == nil && !eventAt.Before(createdAt.Add(-2*time.Second)) {
			result = append(result, line)
		}
	}
	return result
}

func loginEventLine(line []byte) bool {
	if !bytes.Contains(line, []byte("[account]")) {
		return false
	}
	if !bytes.Contains(line, []byte("Login-state transition")) && !bytes.Contains(line, []byte("Identity changed")) {
		return false
	}
	return bytes.Contains(line, []byte("loggedOut: true → false")) || bytes.Contains(line, []byte("loggedOut: true -> false"))
}

func identityHash(value []byte) string {
	digest := sha256.Sum256([]byte(strings.ToLower(string(value))))
	return hex.EncodeToString(digest[:])
}

func sortedIdentityValues(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func identityMatchScore(live, saved localIdentity) int {
	uuidMatches := identityOverlapCount(live.uuidHashes, saved.uuidHashes)
	emailMatches := identityOverlapCount(live.emailHashes, saved.emailHashes)
	if emailMatches > 0 {
		return 10000 + emailMatches*100 + uuidMatches
	}
	if uuidMatches < 2 {
		return 0
	}
	return uuidMatches
}

func identityOverlapCount(left, right []string) int {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	matches := 0
	for _, value := range right {
		if _, ok := values[value]; ok {
			matches++
		}
	}
	return matches
}
