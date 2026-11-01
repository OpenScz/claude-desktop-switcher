package sessions

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TinoA/claude-desktop-switcher/internal/profile"
)

const (
	recordsDirName    = "claude-code-sessions"
	worktreesFileName = "git-worktrees.json"
	configFileName    = "claude_desktop_config.json"
	maxFileBytes      = 16 << 20
)

// Claude Desktop ignores claude://code/continue links whose session has any other form.
var continueIDPattern = regexp.MustCompile(`^local_[A-Za-z0-9-]{1,64}$`)

// Group is the heading Claude's sidebar shows the session under. GroupRank is
// 0 for a group Claude names after the repository or folder, and from 1 on, in
// sidebar order, for a group the user made. ParentID is set for a side
// session, which the sidebar shows under the session that started it.
type Session struct {
	ID           string
	Title        string
	Group        string
	GroupRank    int
	ParentID     string
	LastActivity time.Time
}

func ContinueURL(id string) string {
	return "claude://code/continue?session=" + id
}

type record struct {
	SessionID             string  `json:"sessionId"`
	CLISessionID          string  `json:"cliSessionId"`
	Title                 string  `json:"title"`
	Cwd                   string  `json:"cwd"`
	OriginCwd             string  `json:"originCwd"`
	WorktreeName          string  `json:"worktreeName"`
	ScheduledTaskID       string  `json:"scheduledTaskId"`
	IsArchived            bool    `json:"isArchived"`
	TranscriptUnavailable bool    `json:"transcriptUnavailable"`
	CreatedAt             float64 `json:"createdAt"`
	LastActivityAt        float64 `json:"lastActivityAt"`
	GitAnchors            []struct {
		GitRoot   string `json:"gitRoot"`
		CommonDir string `json:"commonDir"`
	} `json:"gitAnchors"`
	SpawnedFrom *struct {
		SessionID   string `json:"sessionId"`
		SideSession bool   `json:"sideSession"`
	} `json:"spawnedFrom"`
}

type cachedRecord struct {
	size    int64
	modTime time.Time
	record  record
	valid   bool
}

type Reader struct {
	mu      sync.Mutex
	records map[string]cachedRecord
}

type candidate struct {
	account string
	scope   string
	order   int
	record  record
}

func (r *Reader) Recent(appData string, accounts []string, active string, limit int) (map[string][]Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	git, err := readGit(filepath.Join(appData, worktreesFileName))
	if err != nil {
		return nil, err
	}
	groups := readSidebarGroups(filepath.Join(appData, configFileName))
	order := make(map[string]int, len(accounts))
	for index, account := range accounts {
		if _, seen := order[account]; account != "" && !seen {
			order[account] = index
		}
	}
	root := filepath.Join(appData, recordsDirName)
	accountDirs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return map[string][]Session{}, nil
	}
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	homes := make(map[string]candidate)
	for _, accountDir := range accountDirs {
		account := profile.AccountUUIDHash(accountDir.Name())
		index, saved := order[account]
		if !accountDir.IsDir() || !saved {
			continue
		}
		orgDirs, err := os.ReadDir(filepath.Join(root, accountDir.Name()))
		if err != nil {
			continue
		}
		for _, orgDir := range orgDirs {
			if !orgDir.IsDir() {
				continue
			}
			dir := filepath.Join(root, accountDir.Name(), orgDir.Name())
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				name := entry.Name()
				if entry.IsDir() || !strings.HasPrefix(name, "local_") || !strings.HasSuffix(name, ".json") {
					continue
				}
				path := filepath.Join(dir, name)
				seen[path] = true
				rec, ok := r.load(path, entry)
				if !ok || !continuable(rec, git.leases) {
					continue
				}
				next := candidate{account: account, scope: accountDir.Name() + "/" + orgDir.Name(), order: index, record: rec}
				if current, exists := homes[rec.CLISessionID]; !exists || next.before(current, active) {
					homes[rec.CLISessionID] = next
				}
			}
		}
	}
	for path := range r.records {
		if !seen[path] {
			delete(r.records, path)
		}
	}
	result := make(map[string][]Session)
	for _, home := range homes {
		result[home.account] = append(result[home.account], home.session(git, groups))
	}
	for account, list := range result {
		sort.Slice(list, func(i, j int) bool {
			if !list[i].LastActivity.Equal(list[j].LastActivity) {
				return list[i].LastActivity.After(list[j].LastActivity)
			}
			return list[i].ID < list[j].ID
		})
		if len(list) > limit {
			list = list[:limit]
		}
		result[account] = list
	}
	return result, nil
}

func (r *Reader) load(path string, entry os.DirEntry) (record, bool) {
	info, err := entry.Info()
	if err != nil || info.Size() > maxFileBytes {
		return record{}, false
	}
	if cached, ok := r.records[path]; ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		return cached.record, cached.valid
	}
	data, err := readShared(path)
	if err != nil {
		return record{}, false
	}
	var rec record
	valid := json.Unmarshal(data, &rec) == nil
	if r.records == nil {
		r.records = make(map[string]cachedRecord)
	}
	r.records[path] = cachedRecord{size: info.Size(), modTime: info.ModTime(), record: rec, valid: valid}
	return rec, valid
}

func continuable(rec record, leases map[string]string) bool {
	if rec.IsArchived || rec.TranscriptUnavailable || rec.ScheduledTaskID != "" || rec.CLISessionID == "" || !continueIDPattern.MatchString(rec.SessionID) {
		return false
	}
	// Each worktree folder is reserved for one record. Claude opens any other
	// record of that folder in a fresh worktree on the record's old branch.
	if owner, leased := leases[rec.WorktreeName]; rec.WorktreeName != "" && leased && owner != rec.SessionID {
		return false
	}
	info, err := os.Stat(rec.Cwd)
	return err == nil && info.IsDir()
}

// Records copied between accounts keep their activity time, so a tie means
// the conversation opens the same way on either account.
func (c candidate) before(other candidate, active string) bool {
	if last, otherLast := c.record.lastActivity(), other.record.lastActivity(); last != otherLast {
		return last > otherLast
	}
	if (c.account == active) != (other.account == active) {
		return c.account == active
	}
	if c.order != other.order {
		return c.order < other.order
	}
	return c.record.SessionID < other.record.SessionID
}

func (c candidate) session(git gitState, groups map[string]map[string]sidebarGroup) Session {
	title := strings.TrimSpace(c.record.Title)
	if title == "" {
		title = filepath.Base(c.record.Cwd)
	}
	session := Session{
		ID:           c.record.SessionID,
		Title:        title,
		Group:        git.repositoryName(c.record),
		LastActivity: time.UnixMilli(int64(c.record.lastActivity())),
	}
	if session.Group == "" {
		session.Group = projectName(c.record)
	}
	if group, ok := groups[strings.ToLower(c.scope)]["code:"+c.record.SessionID]; ok {
		session.Group, session.GroupRank = group.name, group.rank
	}
	if c.record.SpawnedFrom != nil && c.record.SpawnedFrom.SideSession {
		session.ParentID = c.record.SpawnedFrom.SessionID
	}
	return session
}

func (r record) lastActivity() float64 {
	if r.LastActivityAt > 0 {
		return r.LastActivityAt
	}
	return r.CreatedAt
}

func projectName(rec record) string {
	folder := rec.OriginCwd
	if folder == "" {
		folder = rec.Cwd
	}
	worktrees := filepath.Dir(folder)
	if strings.EqualFold(filepath.Base(worktrees), "worktrees") && strings.EqualFold(filepath.Base(filepath.Dir(worktrees)), ".claude") {
		folder = filepath.Dir(filepath.Dir(worktrees))
	}
	return filepath.Base(folder)
}

// Claude Code keeps one file per running process in <claudeDir>/sessions, and
// marks it busy while a turn runs; a process that ended may leave it behind.
type processRecord struct {
	PID           int    `json:"pid"`
	ProcStart     string `json:"procStart"`
	Status        string `json:"status"`
	Entrypoint    string `json:"entrypoint"`
	HostSessionID string `json:"hostSessionId"`
	Name          string `json:"name"`
}

func Working(claudeDir, appData string) []string {
	return working(claudeDir, appData, processRunning)
}

func working(claudeDir, appData string, running func(pid int, procStart string) bool) []string {
	dir := filepath.Join(claudeDir, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	// Separate windows share these session records, and the main window runs
	// only the sessions of the account signed in to it.
	account := profile.AccountUUIDHashAt(appData)
	var titles []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := readShared(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var process processRecord
		if json.Unmarshal(data, &process) != nil || process.Status != "busy" || process.Entrypoint != "claude-desktop" {
			continue
		}
		if !hostedBy(appData, account, process.HostSessionID) || !running(process.PID, process.ProcStart) {
			continue
		}
		title := strings.TrimSpace(process.Name)
		if title == "" {
			title = "Untitled session"
		}
		titles = append(titles, title)
	}
	sort.Strings(titles)
	return titles
}

func hostedBy(appData, account, id string) bool {
	if !continueIDPattern.MatchString(id) {
		return false
	}
	root := filepath.Join(appData, recordsDirName)
	accountDirs, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, accountDir := range accountDirs {
		if account != "" && profile.AccountUUIDHash(accountDir.Name()) != account {
			continue
		}
		orgDirs, err := os.ReadDir(filepath.Join(root, accountDir.Name()))
		if err != nil {
			continue
		}
		for _, orgDir := range orgDirs {
			if _, err := os.Stat(filepath.Join(root, accountDir.Name(), orgDir.Name(), id+".json")); err == nil {
				return true
			}
		}
	}
	return false
}

// Claude refuses to save its records through a link, a directory junction
// included, so each separate window keeps a records folder of its own and the
// switcher copies an account's files between it and the main folder. The copy
// changed last wins, and a deletion marker removes the record on the other
// side. Neither side's Claude may hold the account while it is copied into.
func MirrorAccount(fromData, toData, account string) (int, error) {
	from := filepath.Join(fromData, recordsDirName)
	to := filepath.Join(toData, recordsDirName)
	if account == "" {
		return 0, nil
	}
	accountDirs, err := os.ReadDir(from)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, accountDir := range accountDirs {
		if !accountDir.IsDir() || profile.AccountUUIDHash(accountDir.Name()) != account {
			continue
		}
		orgDirs, err := os.ReadDir(filepath.Join(from, accountDir.Name()))
		if err != nil {
			return changed, err
		}
		for _, orgDir := range orgDirs {
			if !orgDir.IsDir() {
				continue
			}
			count, err := mirrorFolder(filepath.Join(from, accountDir.Name(), orgDir.Name()), filepath.Join(to, accountDir.Name(), orgDir.Name()))
			changed += count
			if err != nil {
				return changed, err
			}
		}
	}
	return changed, nil
}

// Claude names a deleted session's marker after the record without its
// "local_" prefix, and after its CLI conversation.
func mirrorFolder(from, to string) (int, error) {
	entries, err := os.ReadDir(from)
	if err != nil {
		return 0, err
	}
	deleted := make(map[string]bool)
	for _, entry := range entries {
		if stem, ok := strings.CutPrefix(entry.Name(), "deleted_"); ok {
			deleted[stem] = true
		}
	}
	changed := 0
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() {
			continue
		}
		if stem, ok := strings.CutPrefix(name, "deleted_"); ok {
			if err := os.Remove(filepath.Join(to, "local_"+stem+".json")); err == nil {
				changed++
			} else if !errors.Is(err, os.ErrNotExist) {
				return changed, err
			}
		} else if stem, ok := strings.CutSuffix(strings.TrimPrefix(name, "local_"), ".json"); ok && strings.HasPrefix(name, "local_") {
			if deleted[stem] || exists(filepath.Join(to, "deleted_"+stem)) {
				continue
			}
		} else if name != "scheduled-tasks.json" && name != "archived-sessions.idx" {
			continue
		}
		copied, err := copyIfNewer(filepath.Join(from, name), filepath.Join(to, name))
		if copied {
			changed++
		}
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// The copy keeps the source's time, so the side Claude changed last stays the
// newer one.
func copyIfNewer(from, to string) (bool, error) {
	source, err := os.Stat(from)
	if err != nil {
		return false, err
	}
	if target, err := os.Stat(to); err == nil && !target.ModTime().Before(source.ModTime()) {
		return false, nil
	}
	data, err := readShared(from)
	if err != nil {
		return false, err
	}
	if err := writeAtomic(to, data); err != nil {
		return false, err
	}
	return true, os.Chtimes(to, source.ModTime(), source.ModTime())
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// Claude's loader reads only local_*.json, so the temporary name is never
// taken for a record.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".switcher-*")
	if err != nil {
		return err
	}
	_, err = temp.Write(data)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(temp.Name())
	}
	return err
}

// Claude reserves each worktree folder for one session in git-worktrees.json,
// one file per data folder, and gives a session whose reservation is missing a
// fresh worktree when the folder has moved to another branch. A separate window
// receives the main window's reservations for its account's sessions before it
// starts.
func CopyWorktreeLeases(mainData, windowData, account string) error {
	ids := accountSessionIDs(filepath.Join(mainData, recordsDirName), account)
	if len(ids) == 0 {
		return nil
	}
	data, err := readShared(filepath.Join(mainData, worktreesFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var main struct {
		SchemaVersion json.RawMessage            `json:"schemaVersion"`
		Worktrees     map[string]json.RawMessage `json:"worktrees"`
	}
	if err := json.Unmarshal(data, &main); err != nil {
		return err
	}
	path := filepath.Join(windowData, worktreesFileName)
	window := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &window); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if version, ok := window["schemaVersion"]; ok && string(version) != string(main.SchemaVersion) {
		return nil
	}
	worktrees := map[string]json.RawMessage{}
	if raw, ok := window["worktrees"]; ok {
		if err := json.Unmarshal(raw, &worktrees); err != nil {
			return err
		}
	}
	copied := false
	for name, raw := range main.Worktrees {
		var entry struct {
			LeasedBy string `json:"leasedBy"`
			Status   string `json:"status"`
		}
		if _, held := worktrees[name]; held || json.Unmarshal(raw, &entry) != nil || !ids[entry.LeasedBy] || entry.Status == "creating" {
			continue
		}
		worktrees[name] = raw
		copied = true
	}
	if !copied {
		return nil
	}
	if window["worktrees"], err = json.Marshal(worktrees); err != nil {
		return err
	}
	if len(main.SchemaVersion) > 0 {
		window["schemaVersion"] = main.SchemaVersion
	}
	out, err := json.MarshalIndent(window, "", "\t")
	if err != nil {
		return err
	}
	return writeAtomic(path, out)
}

func accountSessionIDs(root, account string) map[string]bool {
	ids := make(map[string]bool)
	accountDirs, _ := os.ReadDir(root)
	for _, accountDir := range accountDirs {
		if account == "" || profile.AccountUUIDHash(accountDir.Name()) != account {
			continue
		}
		orgDirs, _ := os.ReadDir(filepath.Join(root, accountDir.Name()))
		for _, orgDir := range orgDirs {
			entries, _ := os.ReadDir(filepath.Join(root, accountDir.Name(), orgDir.Name()))
			for _, entry := range entries {
				if id, ok := strings.CutSuffix(entry.Name(), ".json"); ok && strings.HasPrefix(id, "local_") {
					ids[id] = true
				}
			}
		}
	}
	return ids
}

type gitState struct {
	leases map[string]string
	pins   map[string]string
	urls   map[string]string
}

func readGit(path string) (gitState, error) {
	data, err := readShared(path)
	if errors.Is(err, os.ErrNotExist) {
		return gitState{}, nil
	}
	if err != nil {
		return gitState{}, err
	}
	var file struct {
		Worktrees map[string]struct {
			LeasedBy string `json:"leasedBy"`
		} `json:"worktrees"`
		OriginPins map[string]struct {
			Repo   string `json:"repo"`
			None   bool   `json:"none"`
			Folder string `json:"folder"`
		} `json:"originPins"`
		OriginURLs map[string]struct {
			URL string `json:"url"`
		} `json:"originUrls"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return gitState{}, err
	}
	git := gitState{leases: make(map[string]string), pins: make(map[string]string), urls: make(map[string]string)}
	for name, worktree := range file.Worktrees {
		git.leases[name] = worktree.LeasedBy
	}
	for commonDir, pin := range file.OriginPins {
		switch {
		case pin.Repo != "":
			git.pins[pathKey(commonDir)] = repositoryFromOrigin(pin.Repo)
		case pin.None && pin.Folder != "":
			git.pins[pathKey(commonDir)] = filepath.Base(pin.Folder)
		}
	}
	for folder, origin := range file.OriginURLs {
		if name := repositoryFromOrigin(origin.URL); name != "" {
			git.urls[pathKey(folder)] = name
		}
	}
	return git, nil
}

// Claude's sidebar names a session's group after the repository of its first
// git anchor: the origin Claude pinned for it, else its origin URL, else the
// repository folder.
func (g gitState) repositoryName(rec record) string {
	if len(rec.GitAnchors) == 0 || rec.GitAnchors[0].GitRoot == "" {
		return ""
	}
	anchor := rec.GitAnchors[0]
	if name := g.pins[pathKey(anchor.CommonDir)]; name != "" {
		return name
	}
	if name := g.urls[pathKey(anchor.GitRoot)]; name != "" {
		return name
	}
	return filepath.Base(anchor.GitRoot)
}

func repositoryFromOrigin(origin string) string {
	origin = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(origin), "/"), ".git")
	return origin[strings.LastIndexAny(origin, "/:")+1:]
}

func pathKey(path string) string {
	return strings.ToLower(filepath.Clean(path))
}

type sidebarGroup struct {
	name string
	rank int
}

// Groups the user made in Claude's sidebar live in its desktop configuration,
// per account and organization. Only these fields are read from that file.
func readSidebarGroups(path string) map[string]map[string]sidebarGroup {
	data, err := readShared(path)
	if err != nil {
		return nil
	}
	var config struct {
		Preferences struct {
			EpitaxyPrefs struct {
				GroupScopes map[string]struct {
					Groups []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"groups"`
					Assignments map[string]string `json:"assignments"`
				} `json:"dframe-group-scopes"`
				CodeSections map[string]struct {
					Sections []struct {
						ID    string `json:"id"`
						Kind  string `json:"kind"`
						Order int    `json:"order"`
					} `json:"sections"`
				} `json:"dframe-code-sections"`
			} `json:"epitaxyPrefs"`
		} `json:"preferences"`
	}
	if json.Unmarshal(data, &config) != nil {
		return nil
	}
	prefs := config.Preferences.EpitaxyPrefs
	result := make(map[string]map[string]sidebarGroup, len(prefs.GroupScopes))
	for scope, groupScope := range prefs.GroupScopes {
		ranks := make(map[string]int)
		for _, section := range prefs.CodeSections[scope].Sections {
			if section.Kind == "manual" {
				ranks[section.ID] = section.Order + 1
			}
		}
		named := make(map[string]sidebarGroup, len(groupScope.Groups))
		for index, group := range groupScope.Groups {
			rank, ok := ranks[group.ID]
			if !ok {
				rank = 1000 + index
			}
			if name := strings.TrimSpace(group.Name); name != "" {
				named[group.ID] = sidebarGroup{name: name, rank: rank}
			}
		}
		assigned := make(map[string]sidebarGroup, len(groupScope.Assignments))
		for key, groupID := range groupScope.Assignments {
			if group, ok := named[groupID]; ok {
				assigned[key] = group
			}
		}
		result[strings.ToLower(scope)] = assigned
	}
	return result
}

func readShared(path string) ([]byte, error) {
	file, err := openShared(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, errors.New("file is larger than expected")
	}
	return data, nil
}
