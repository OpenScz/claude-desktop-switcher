package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TinoA/claude-desktop-switcher/internal/profile"
)

const (
	accountA = "11111111-1111-4111-8111-111111111111"
	accountB = "22222222-2222-4222-8222-222222222222"
	accountC = "33333333-3333-4333-8333-333333333333"
	org      = "44444444-4444-4444-8444-444444444444"
)

type fixture struct {
	t         *testing.T
	appData   string
	claudeDir string
	folders   string
	reader    Reader
}

func newFixture(t *testing.T) *fixture {
	root := t.TempDir()
	return &fixture{t: t, appData: filepath.Join(root, "Claude"), claudeDir: filepath.Join(root, ".claude"), folders: filepath.Join(root, "Projects")}
}

func (f *fixture) process(name, content string) {
	f.t.Helper()
	dir := filepath.Join(f.claudeDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) folder(parts ...string) string {
	f.t.Helper()
	path := filepath.Join(append([]string{f.folders}, parts...)...)
	if err := os.MkdirAll(path, 0o700); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *fixture) record(account, id string, fields map[string]any) string {
	f.t.Helper()
	dir := filepath.Join(f.appData, recordsDirName, account, org)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	value := map[string]any{"sessionId": id}
	for key, field := range fields {
		value[key] = field
	}
	data, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *fixture) leases(owners map[string]string) {
	f.t.Helper()
	worktrees := make(map[string]any, len(owners))
	for name, owner := range owners {
		worktrees[name] = map[string]any{"name": name, "leasedBy": owner}
	}
	data, err := json.Marshal(map[string]any{"schemaVersion": 2, "worktrees": worktrees})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.MkdirAll(f.appData, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.appData, worktreesFileName), data, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) recent(active string, limit int, accounts ...string) map[string][]Session {
	f.t.Helper()
	hashes := make([]string, len(accounts))
	for index, account := range accounts {
		hashes[index] = profile.AccountUUIDHash(account)
	}
	result, err := f.reader.Recent(f.appData, hashes, profile.AccountUUIDHash(active), limit)
	if err != nil {
		f.t.Fatal(err)
	}
	return result
}

func listed(result map[string][]Session, account string) []string {
	ids := []string{}
	for _, session := range result[profile.AccountUUIDHash(account)] {
		ids = append(ids, session.ID)
	}
	return ids
}

func assertListed(t *testing.T, result map[string][]Session, account string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := listed(result, account); !reflect.DeepEqual(got, want) {
		t.Fatalf("sessions of %s = %v, want %v", account[:8], got, want)
	}
}

func TestRecentListsEachConversationUnderTheAccountThatUsedItLast(t *testing.T) {
	f := newFixture(t)
	folder := f.folder("Sample Project One")
	f.record(accountA, "local_a1", map[string]any{"cliSessionId": "c1", "title": "Shared", "cwd": folder, "lastActivityAt": 1000})
	f.record(accountB, "local_b1", map[string]any{"cliSessionId": "c1", "title": "Shared", "cwd": folder, "lastActivityAt": 2000})
	f.record(accountA, "local_a2", map[string]any{"cliSessionId": "c2", "title": "Only on A", "cwd": folder, "lastActivityAt": 1500})

	result := f.recent("", 10, accountA, accountB)

	assertListed(t, result, accountA, "local_a2")
	assertListed(t, result, accountB, "local_b1")
}

func TestRecentPrefersTheOpenAccountForIdenticalCopies(t *testing.T) {
	f := newFixture(t)
	folder := f.folder("Sample Project One")
	f.record(accountA, "local_a1", map[string]any{"cliSessionId": "c1", "cwd": folder, "lastActivityAt": 1000})
	f.record(accountB, "local_b1", map[string]any{"cliSessionId": "c1", "cwd": folder, "lastActivityAt": 1000})

	open := f.recent(accountB, 10, accountA, accountB)
	assertListed(t, open, accountA)
	assertListed(t, open, accountB, "local_b1")

	for _, active := range []string{"", accountC} {
		closed := f.recent(active, 10, accountA, accountB)
		assertListed(t, closed, accountA, "local_a1")
		assertListed(t, closed, accountB)
	}
}

func TestRecentSkipsSessionsThatCannotBeContinued(t *testing.T) {
	f := newFixture(t)
	folder := f.folder("Sample Project One")
	missing := filepath.Join(f.folders, "Deleted project")
	f.record(accountA, "local_ok", map[string]any{"cliSessionId": "c1", "cwd": folder, "lastActivityAt": 1000})
	f.record(accountA, "local_archived", map[string]any{"cliSessionId": "c2", "cwd": folder, "lastActivityAt": 1000, "isArchived": true})
	f.record(accountA, "local_scheduled", map[string]any{"cliSessionId": "c3", "cwd": folder, "lastActivityAt": 1000, "scheduledTaskId": "daily"})
	f.record(accountA, "local_lost", map[string]any{"cliSessionId": "c4", "cwd": folder, "lastActivityAt": 1000, "transcriptUnavailable": true})
	f.record(accountA, "local_old", map[string]any{"cwd": folder, "lastActivityAt": 1000})
	f.record(accountA, "local_gone", map[string]any{"cliSessionId": "c5", "cwd": missing, "lastActivityAt": 1000})
	f.record(accountA, "local_odd", map[string]any{"sessionId": "session_odd", "cliSessionId": "c6", "cwd": folder, "lastActivityAt": 1000})
	f.record(accountA, "local_shared", map[string]any{"cliSessionId": "c7", "cwd": folder, "lastActivityAt": 1000})
	f.record(accountB, "local_hidden", map[string]any{"cliSessionId": "c7", "cwd": folder, "lastActivityAt": 3000, "isArchived": true})
	path := filepath.Join(f.appData, recordsDirName, accountA, org, "local_broken.json")
	if err := os.WriteFile(path, []byte(`{"sessionId":`), 0o600); err != nil {
		t.Fatal(err)
	}

	result := f.recent("", 10, accountA, accountB)

	assertListed(t, result, accountA, "local_ok", "local_shared")
	assertListed(t, result, accountB)
}

func TestRecentKeepsWorktreeSessionsWithTheAccountHoldingTheirFolder(t *testing.T) {
	f := newFixture(t)
	oldFolder := f.folder("Sample Project One", ".claude", "worktrees", "admiring-spence")
	newFolder := f.folder("Sample Project One", ".claude", "worktrees", "peaceful-bassi")
	untracked := f.folder("Sample Project One", ".claude", "worktrees", "untracked")
	f.leases(map[string]string{"admiring-spence": "local_a9", "peaceful-bassi": "local_b1"})
	f.record(accountA, "local_a1", map[string]any{"cliSessionId": "c1", "cwd": oldFolder, "worktreeName": "admiring-spence", "lastActivityAt": 3000})
	f.record(accountB, "local_b1", map[string]any{"cliSessionId": "c1", "cwd": newFolder, "worktreeName": "peaceful-bassi", "lastActivityAt": 1000})
	f.record(accountA, "local_a9", map[string]any{"cliSessionId": "c9", "cwd": oldFolder, "worktreeName": "admiring-spence", "lastActivityAt": 500})
	f.record(accountA, "local_a3", map[string]any{"cliSessionId": "c3", "cwd": untracked, "worktreeName": "untracked", "lastActivityAt": 400})

	result := f.recent("", 10, accountA, accountB)

	assertListed(t, result, accountA, "local_a9", "local_a3")
	assertListed(t, result, accountB, "local_b1")
}

func (f *fixture) writeJSON(name string, value any) {
	f.t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.MkdirAll(f.appData, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.appData, name), data, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func anchor(root string) []map[string]string {
	return []map[string]string{{"gitRoot": root, "commonDir": filepath.Join(root, ".git")}}
}

func TestRecentGroupsSessionsLikeClaudesSidebar(t *testing.T) {
	f := newFixture(t)
	parent := f.folder("Sample Project One")
	child := f.folder("Sample Second App")
	video := f.folder("Demo Video Studio")
	budgeting := f.folder("Budgeting")
	plain := f.folder("Notebook folder")
	worktree := f.folder("Lab One", ".claude", "worktrees", "hopeful-wescoff")
	f.writeJSON(worktreesFileName, map[string]any{
		"worktrees": map[string]any{},
		"originPins": map[string]any{
			filepath.Join(parent, ".git"): map[string]any{"repo": "exampleuser/Sample-Project-One", "folder": parent},
			filepath.Join(video, ".git"):  map[string]any{"none": true, "folder": video},
		},
		"originUrls": map[string]any{
			strings.ToLower(budgeting): map[string]any{"url": "https://github.com/exampleuser/SampleRepoA.git"},
		},
	})
	scope := accountA + "/" + org
	f.writeJSON(configFileName, map[string]any{
		"mcpServers": map[string]any{"example": map[string]any{"env": map[string]any{"NOT_READ": "x"}}},
		"preferences": map[string]any{"epitaxyPrefs": map[string]any{
			"dframe-group-scopes": map[string]any{scope: map[string]any{
				"groups":      []any{map[string]any{"id": "cg-1", "name": "SwitcherGroups"}},
				"assignments": map[string]any{"code:local_switcher": "cg-1"},
			}},
			"dframe-code-sections": map[string]any{scope: map[string]any{
				"sections": []any{
					map[string]any{"id": "pinned", "kind": "pinned", "order": 0},
					map[string]any{"id": "cg-1", "kind": "manual", "order": 2},
				},
			}},
		}},
	})
	childAnchor := []map[string]string{{"gitRoot": child, "commonDir": filepath.Join(parent, ".git")}}
	f.record(accountA, "local_switcher", map[string]any{"cliSessionId": "c1", "title": "Switcher", "cwd": parent, "gitAnchors": anchor(parent), "lastActivityAt": 7000})
	f.record(accountA, "local_child", map[string]any{"cliSessionId": "c2", "title": "Other", "cwd": child, "gitAnchors": childAnchor, "lastActivityAt": 6000})
	f.record(accountA, "local_video", map[string]any{"cliSessionId": "c3", "title": "Video", "cwd": video, "gitAnchors": anchor(video), "lastActivityAt": 5000})
	f.record(accountA, "local_budgeting", map[string]any{"cliSessionId": "c4", "title": "Budgeting", "cwd": budgeting, "gitAnchors": anchor(budgeting), "lastActivityAt": 4000})
	f.record(accountA, "local_plain", map[string]any{"cliSessionId": "c5", "title": "Plain git folder", "cwd": plain, "gitAnchors": anchor(plain), "lastActivityAt": 3000})
	f.record(accountA, "local_side", map[string]any{"cliSessionId": "c6", "title": "Side", "cwd": plain, "gitAnchors": anchor(plain), "lastActivityAt": 2500,
		"spawnedFrom": map[string]any{"sessionId": "local_plain", "sideSession": true}})
	f.record(accountA, "local_started", map[string]any{"cliSessionId": "c7", "title": "  ", "cwd": worktree, "lastActivityAt": 2000,
		"spawnedFrom": map[string]any{"sessionId": "local_plain"}})

	got := f.recent("", 10, accountA)[profile.AccountUUIDHash(accountA)]

	want := []Session{
		{ID: "local_switcher", Title: "Switcher", Group: "SwitcherGroups", GroupRank: 3, LastActivity: time.UnixMilli(7000)},
		{ID: "local_child", Title: "Other", Group: "Sample-Project-One", LastActivity: time.UnixMilli(6000)},
		{ID: "local_video", Title: "Video", Group: "Demo Video Studio", LastActivity: time.UnixMilli(5000)},
		{ID: "local_budgeting", Title: "Budgeting", Group: "SampleRepoA", LastActivity: time.UnixMilli(4000)},
		{ID: "local_plain", Title: "Plain git folder", Group: "Notebook folder", LastActivity: time.UnixMilli(3000)},
		{ID: "local_side", Title: "Side", Group: "Notebook folder", ParentID: "local_plain", LastActivity: time.UnixMilli(2500)},
		{ID: "local_started", Title: "hopeful-wescoff", Group: "Lab One", LastActivity: time.UnixMilli(2000)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessions = %+v\nwant %+v", got, want)
	}
}

func TestRecentGroupsSessionsWithoutClaudesConfiguration(t *testing.T) {
	f := newFixture(t)
	child := f.folder("Sample Second App")
	f.record(accountA, "local_o", map[string]any{"cliSessionId": "c1", "title": "Started in Other", "cwd": f.folder("Sample Project One"), "originCwd": child, "lastActivityAt": 1000})
	f.writeJSON(configFileName, "not the expected shape")

	got := f.recent("", 10, accountA)[profile.AccountUUIDHash(accountA)]

	if len(got) != 1 || got[0].Group != "Sample Second App" || got[0].GroupRank != 0 {
		t.Fatalf("sessions = %+v", got)
	}
}

func TestRepositoryFromOrigin(t *testing.T) {
	for origin, want := range map[string]string{
		"exampleuser/SampleRepoB":                    "SampleRepoB",
		"https://github.com/exampleuser/Zinnia.git":  "Zinnia",
		"https://github.com/exampleuser/Zinnia.git/": "Zinnia",
		"git@github.com:exampleuser/SampleRepoA.git": "SampleRepoA",
		"Sample-Project-One":                         "Sample-Project-One",
		"":                                           "",
	} {
		if got := repositoryFromOrigin(origin); got != want {
			t.Errorf("repositoryFromOrigin(%q) = %q, want %q", origin, got, want)
		}
	}
}

func TestRecentListsNewestFirstUpToTheLimit(t *testing.T) {
	f := newFixture(t)
	folder := f.folder("Sample Project One")
	f.record(accountA, "local_1", map[string]any{"cliSessionId": "c1", "cwd": folder, "lastActivityAt": 1000})
	f.record(accountA, "local_2", map[string]any{"cliSessionId": "c2", "cwd": folder, "lastActivityAt": 3000})
	f.record(accountA, "local_3", map[string]any{"cliSessionId": "c3", "cwd": folder, "createdAt": 2500})

	result := f.recent("", 2, accountA)

	assertListed(t, result, accountA, "local_2", "local_3")
}

func TestRecentIgnoresAccountsThatAreNotSaved(t *testing.T) {
	f := newFixture(t)
	folder := f.folder("Sample Project One")
	f.record(accountC, "local_c1", map[string]any{"cliSessionId": "c1", "cwd": folder, "lastActivityAt": 9000})
	f.record(accountA, "local_a1", map[string]any{"cliSessionId": "c1", "cwd": folder, "lastActivityAt": 1000})

	result := f.recent("", 10, accountA, accountB)

	assertListed(t, result, accountA, "local_a1")
	if _, ok := result[profile.AccountUUIDHash(accountC)]; ok {
		t.Fatal("an account that is not saved in the switcher was listed")
	}
}

func TestRecentRereadsChangedRecordsAndForgetsRemovedOnes(t *testing.T) {
	f := newFixture(t)
	folder := f.folder("Sample Project One")
	path := f.record(accountA, "local_a1", map[string]any{"cliSessionId": "c1", "title": "Before", "cwd": folder, "lastActivityAt": 1000})
	if got := f.recent("", 10, accountA)[profile.AccountUUIDHash(accountA)][0].Title; got != "Before" {
		t.Fatalf("title = %q", got)
	}

	f.record(accountA, "local_a1", map[string]any{"cliSessionId": "c1", "title": "After a rename", "cwd": folder, "lastActivityAt": 2000})
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if got := f.recent("", 10, accountA)[profile.AccountUUIDHash(accountA)][0].Title; got != "After a rename" {
		t.Fatalf("title after the record changed = %q", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	assertListed(t, f.recent("", 10, accountA), accountA)
	if len(f.reader.records) != 0 {
		t.Fatalf("removed records stayed cached: %d", len(f.reader.records))
	}
}

func TestRecentWithoutClaudeData(t *testing.T) {
	f := newFixture(t)
	if result := f.recent("", 10, accountA); len(result) != 0 {
		t.Fatalf("sessions without Claude data = %v", result)
	}
}

func TestRecentReportsAnUnreadableFolderReservationFile(t *testing.T) {
	f := newFixture(t)
	f.record(accountA, "local_a1", map[string]any{"cliSessionId": "c1", "cwd": f.folder("Sample Project One"), "lastActivityAt": 1000})
	if err := os.WriteFile(filepath.Join(f.appData, worktreesFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reader.Recent(f.appData, []string{profile.AccountUUIDHash(accountA)}, "", 10); err == nil {
		t.Fatal("sessions were listed without knowing which account holds each worktree folder")
	}
}

func TestContinueURL(t *testing.T) {
	if got := ContinueURL("local_55555555-5555-4555-8555-555555555555"); got != "claude://code/continue?session=local_55555555-5555-4555-8555-555555555555" {
		t.Fatalf("ContinueURL() = %q", got)
	}
}

func TestWorkingListsTheBusySessionsOfTheMainWindow(t *testing.T) {
	f := newFixture(t)
	folder := f.folder("Sample Project One")
	f.record(accountA, "local_a1", map[string]any{"cliSessionId": "c1", "cwd": folder})
	f.record(accountA, "local_a2", map[string]any{"cliSessionId": "c2", "cwd": folder})
	f.record(accountB, "local_b1", map[string]any{"cliSessionId": "c3", "cwd": folder})
	f.process("101.json", `{"pid":101,"procStart":"11","status":"busy","entrypoint":"claude-desktop","hostSessionId":"local_a1","name":"Planificare rapidă mesei"}`)
	f.process("102.json", `{"pid":102,"procStart":"12","status":"busy","entrypoint":"claude-desktop","hostSessionId":"local_b1","name":" "}`)
	f.process("103.json", `{"pid":103,"procStart":"13","status":"idle","entrypoint":"claude-desktop","hostSessionId":"local_a2","name":"Waiting for the next message"}`)
	f.process("104.json", `{"pid":104,"procStart":"14","status":"busy","entrypoint":"cli","hostSessionId":"local_a2","name":"In a terminal"}`)
	f.process("105.json", `{"pid":105,"procStart":"15","status":"busy","entrypoint":"claude-desktop","hostSessionId":"local_elsewhere","name":"In a separate window"}`)
	f.process("106.json", `{"pid":106,"procStart":"16","status":"busy","entrypoint":"claude-desktop","hostSessionId":"local_a2","name":"Ended without cleanup"}`)
	f.process("107.json", `{"pid":`)
	f.process("101.key", "not a session")

	running := func(pid int, procStart string) bool { return pid != 106 }
	got := working(f.claudeDir, f.appData, running)

	if want := []string{"Planificare rapidă mesei", "Untitled session"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("working sessions = %q, want %q", got, want)
	}
}

func TestWorkingLeavesOutSessionsOfAccountsInSeparateWindows(t *testing.T) {
	f := newFixture(t)
	folder := f.folder("Sample Project One")
	f.record(accountA, "local_a1", map[string]any{"cliSessionId": "c1", "cwd": folder})
	f.record(accountB, "local_b1", map[string]any{"cliSessionId": "c2", "cwd": folder})
	f.writeJSON("config.json", map[string]any{"lastKnownAccountUuid": accountA})
	f.process("101.json", `{"pid":101,"procStart":"11","status":"busy","entrypoint":"claude-desktop","hostSessionId":"local_a1","name":"In the main window"}`)
	f.process("102.json", `{"pid":102,"procStart":"12","status":"busy","entrypoint":"claude-desktop","hostSessionId":"local_b1","name":"In a separate window"}`)

	got := working(f.claudeDir, f.appData, func(int, string) bool { return true })

	if want := []string{"In the main window"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("working sessions = %q, want %q", got, want)
	}
}

func TestCopyWorktreeLeasesGivesTheWindowItsAccountsReservations(t *testing.T) {
	f := newFixture(t)
	folder := f.folder("Sample Project One")
	for _, id := range []string{"local_a1", "local_a2", "local_a3"} {
		f.record(accountA, id, map[string]any{"cliSessionId": id, "cwd": folder})
	}
	f.record(accountB, "local_b1", map[string]any{"cliSessionId": "b1", "cwd": folder})
	f.writeJSON(worktreesFileName, map[string]any{
		"schemaVersion": 2,
		"worktrees": map[string]any{
			"brave-a1":   map[string]any{"name": "brave-a1", "leasedBy": "local_a1", "branch": "claude/brave-a1", "autoTrusted": true},
			"fresh-a2":   map[string]any{"name": "fresh-a2", "leasedBy": "local_a2", "status": "creating"},
			"kept-a3":    map[string]any{"name": "kept-a3", "leasedBy": "local_a3", "branch": "main"},
			"other-b1":   map[string]any{"name": "other-b1", "leasedBy": "local_b1"},
			"spare-pool": map[string]any{"name": "spare-pool", "leasedBy": "", "pooledAt": 1},
		},
		"originPins": map[string]any{},
	})
	window := filepath.Join(t.TempDir(), "Main-account")
	writeWindowFile(t, window, `{"schemaVersion": 2, "untrackedDirGc": {"roots": {}}, "worktrees": {"kept-a3": {"name": "kept-a3", "leasedBy": "local_a3", "branch": "feature"}}}`)

	if err := CopyWorktreeLeases(f.appData, window, profile.AccountUUIDHash(accountA)); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	data, err := os.ReadFile(filepath.Join(window, worktreesFileName))
	if err != nil || json.Unmarshal(data, &got) != nil {
		t.Fatalf("window reservations unreadable: %v %s", err, data)
	}
	want := map[string]any{
		"schemaVersion":  float64(2),
		"untrackedDirGc": map[string]any{"roots": map[string]any{}},
		"worktrees": map[string]any{
			"brave-a1": map[string]any{"name": "brave-a1", "leasedBy": "local_a1", "branch": "claude/brave-a1", "autoTrusted": true},
			"kept-a3":  map[string]any{"name": "kept-a3", "leasedBy": "local_a3", "branch": "feature"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("window reservations = %v\nwant %v", got, want)
	}
}

func TestCopyWorktreeLeasesStartsAWindowFileAndLeavesAnotherFormatAlone(t *testing.T) {
	f := newFixture(t)
	f.record(accountA, "local_a1", map[string]any{"cliSessionId": "c1", "cwd": f.folder("Sample Project One")})
	f.writeJSON(worktreesFileName, map[string]any{
		"schemaVersion": 2,
		"worktrees":     map[string]any{"brave-a1": map[string]any{"name": "brave-a1", "leasedBy": "local_a1"}},
	})

	fresh := filepath.Join(t.TempDir(), "new-window")
	if err := CopyWorktreeLeases(f.appData, fresh, profile.AccountUUIDHash(accountA)); err != nil {
		t.Fatal(err)
	}
	var got struct {
		SchemaVersion int                       `json:"schemaVersion"`
		Worktrees     map[string]map[string]any `json:"worktrees"`
	}
	data, err := os.ReadFile(filepath.Join(fresh, worktreesFileName))
	if err != nil || json.Unmarshal(data, &got) != nil || got.SchemaVersion != 2 || got.Worktrees["brave-a1"]["leasedBy"] != "local_a1" {
		t.Fatalf("new window reservations = %s (%v)", data, err)
	}

	other := filepath.Join(t.TempDir(), "newer-claude")
	const newer = `{"schemaVersion": 3, "worktrees": {}}`
	writeWindowFile(t, other, newer)
	if err := CopyWorktreeLeases(f.appData, other, profile.AccountUUIDHash(accountA)); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(other, worktreesFileName)); err != nil || string(data) != newer {
		t.Fatalf("a window file of another format changed: %s (%v)", data, err)
	}
}

func TestMirrorAccountCopiesWhatChangedLast(t *testing.T) {
	window, main := t.TempDir(), t.TempDir()
	early, late, latest := time.Unix(1792000000, 0), time.Unix(1792100000, 0), time.Unix(1792200000, 0)
	writeRecordFiles(t, window, accountA, map[string]fileAt{
		"local_new.json":             {"window new", late},
		"local_old.json":             {"window old", early},
		"scheduled-tasks.json":       {"window tasks", late},
		"local_saving.json.tmp":      {"half written", latest},
		"waiting-input/local_q.json": {"queued", latest},
	})
	writeRecordFiles(t, window, accountB, map[string]fileAt{"local_other.json": {"other account", latest}})
	writeRecordFiles(t, main, accountA, map[string]fileAt{
		"local_old.json":       {"main newer", latest},
		"scheduled-tasks.json": {"main tasks", early},
	})

	copied, err := MirrorAccount(window, main, profile.AccountUUIDHash(accountA))

	if err != nil || copied != 2 {
		t.Fatalf("MirrorAccount() = %d, %v; want 2 files copied", copied, err)
	}
	want := map[string]string{
		accountA + "/" + org + "/local_new.json":       "window new",
		accountA + "/" + org + "/local_old.json":       "main newer",
		accountA + "/" + org + "/scheduled-tasks.json": "window tasks",
	}
	if got := recordFiles(t, main); !reflect.DeepEqual(got, want) {
		t.Fatalf("main records = %v\nwant %v", got, want)
	}
	if info, err := os.Stat(filepath.Join(main, recordsDirName, accountA, org, "local_new.json")); err != nil || !info.ModTime().Equal(late) {
		t.Fatalf("a copied record lost the time of its source: %v %v", info.ModTime(), err)
	}
	if copied, err := MirrorAccount(window, main, profile.AccountUUIDHash(accountA)); err != nil || copied != 0 {
		t.Fatalf("mirroring again = %d, %v; want nothing to copy", copied, err)
	}
}

func TestMirrorAccountCarriesDeletions(t *testing.T) {
	window, main := t.TempDir(), t.TempDir()
	at := time.Unix(1792000000, 0)
	writeRecordFiles(t, window, accountA, map[string]fileAt{
		"deleted_gone":     {"1792000000000", at},
		"local_kept.json":  {"window", at.Add(time.Hour)},
		"local_fresh.json": {"window", at},
	})
	writeRecordFiles(t, main, accountA, map[string]fileAt{
		"local_gone.json": {"main", at},
		"deleted_kept":    {"1792000000000", at},
	})

	if _, err := MirrorAccount(window, main, profile.AccountUUIDHash(accountA)); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		accountA + "/" + org + "/deleted_gone":     "1792000000000",
		accountA + "/" + org + "/deleted_kept":     "1792000000000",
		accountA + "/" + org + "/local_fresh.json": "window",
	}
	if got := recordFiles(t, main); !reflect.DeepEqual(got, want) {
		t.Fatalf("main records = %v\nwant %v", got, want)
	}
}

type fileAt struct {
	content string
	at      time.Time
}

func writeRecordFiles(t *testing.T, data, account string, files map[string]fileAt) {
	t.Helper()
	for name, file := range files {
		path := filepath.Join(data, recordsDirName, account, org, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(file.content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, file.at, file.at); err != nil {
			t.Fatal(err)
		}
	}
}

func recordFiles(t *testing.T, data string) map[string]string {
	t.Helper()
	root := filepath.Join(data, recordsDirName)
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		files[filepath.ToSlash(relative)] = string(content)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func writeWindowFile(t *testing.T, window, content string) {
	t.Helper()
	if err := os.MkdirAll(window, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(window, worktreesFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkingWithoutRunningSessions(t *testing.T) {
	f := newFixture(t)
	if got := working(f.claudeDir, f.appData, func(int, string) bool { return true }); len(got) != 0 {
		t.Fatalf("working sessions = %q", got)
	}
}
