package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestRecentNamesTheProjectAfterTheFolderTheSessionStartedIn(t *testing.T) {
	f := newFixture(t)
	worktree := f.folder("Sample Project One", ".claude", "worktrees", "hopeful-wescoff")
	child := f.folder("Sample Second App")
	plain := f.folder("FridgeChef")
	f.record(accountA, "local_w", map[string]any{"cliSessionId": "c1", "title": "Worktree", "cwd": worktree, "lastActivityAt": 3000})
	f.record(accountA, "local_o", map[string]any{"cliSessionId": "c2", "title": "Started in Other", "cwd": worktree, "originCwd": child, "lastActivityAt": 2000})
	f.record(accountA, "local_p", map[string]any{"cliSessionId": "c3", "title": "  ", "cwd": plain, "lastActivityAt": 1000})

	got := f.recent("", 10, accountA)[profile.AccountUUIDHash(accountA)]

	want := []Session{
		{ID: "local_w", Title: "Worktree", Project: "Sample Project One", LastActivity: time.UnixMilli(3000)},
		{ID: "local_o", Title: "Started in Other", Project: "Sample Second App", LastActivity: time.UnixMilli(2000)},
		{ID: "local_p", Title: "FridgeChef", Project: "FridgeChef", LastActivity: time.UnixMilli(1000)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessions = %+v\nwant %+v", got, want)
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

func TestWorkingWithoutRunningSessions(t *testing.T) {
	f := newFixture(t)
	if got := working(f.claudeDir, f.appData, func(int, string) bool { return true }); len(got) != 0 {
		t.Fatalf("working sessions = %q", got)
	}
}
