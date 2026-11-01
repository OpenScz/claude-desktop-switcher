//go:build windows

package cmd

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TinoA/claude-desktop-switcher/internal/sessions"
)

var sessionTestNow = time.Date(2026, 10, 18, 21, 30, 0, 0, time.UTC)

func sessionAt(month time.Month, day, hour, minute int) time.Time {
	return time.Date(2026, month, day, hour, minute, 0, 0, time.UTC)
}

func entryTitlesAndIDs(entries []sessionEntry) ([]string, []string) {
	var titles, ids []string
	for _, entry := range entries {
		titles = append(titles, entry.title)
		id := ""
		if entry.session != nil {
			id = entry.session.ID
		}
		ids = append(ids, id)
	}
	return titles, ids
}

func TestSessionMenuEntriesGroupSessionsUnderTheirProjects(t *testing.T) {
	list := []sessions.Session{
		{ID: "local_1", Title: "Găsește materiale și calculatoare", Group: "Sample Project One", LastActivity: sessionAt(10, 18, 15, 13)},
		{ID: "local_2", Title: "Garden funcții spațiu server", Group: "Zinnia", LastActivity: sessionAt(10, 18, 7, 35)},
		{ID: "local_3", Title: "Rethink the layout", Group: "Sample Project One", LastActivity: sessionAt(10, 17, 17, 35)},
	}

	titles, ids := entryTitlesAndIDs(sessionMenuEntries(list, sessionTestNow))

	wantTitles := []string{
		"Sample Project One",
		"Găsește materiale și calculatoare\ttoday 15:13",
		"Rethink the layout\tyesterday 17:35",
		"Zinnia",
		"Garden funcții spațiu server\ttoday 07:35",
	}
	wantIDs := []string{"", "local_1", "local_3", "", "local_2"}
	if !reflect.DeepEqual(titles, wantTitles) || !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("entries = %q %q\nwant %q %q", titles, ids, wantTitles, wantIDs)
	}
}

func TestSessionMenuEntriesFitTheMenuWhenEverySessionHasItsOwnProject(t *testing.T) {
	var list []sessions.Session
	for index := range recentSessionsPerAccount {
		list = append(list, sessions.Session{ID: fmt.Sprintf("local_%d", index), Title: "Session", Group: fmt.Sprintf("Project %d", index), LastActivity: sessionTestNow})
	}
	if entries := sessionMenuEntries(list, sessionTestNow); len(entries) > sessionMenuSlots {
		t.Fatalf("%d entries do not fit %d menu slots", len(entries), sessionMenuSlots)
	}
}

func TestSessionMenuEntriesWithoutSessions(t *testing.T) {
	titles, ids := entryTitlesAndIDs(sessionMenuEntries(nil, sessionTestNow))
	if !reflect.DeepEqual(titles, []string{"No Claude Code sessions yet"}) || ids[0] != "" {
		t.Fatalf("entries = %q %q", titles, ids)
	}
}

func TestSessionMenuEntriesKeepTitlesSafeForTheMenu(t *testing.T) {
	long := strings.Repeat("abcdefghij", 7)
	list := []sessions.Session{
		{ID: "local_1", Title: "Q&A\x00 notes\nfor\u202e R&D", Group: "R&D lab", LastActivity: sessionAt(10, 18, 9, 5)},
		{ID: "local_2", Title: long, Group: "R&D lab", LastActivity: sessionAt(10, 18, 9, 0)},
	}

	titles, _ := entryTitlesAndIDs(sessionMenuEntries(list, sessionTestNow))

	want := []string{
		"R&&D lab",
		"Q&&A notes for R&&D\ttoday 09:05",
		long[:sessionTitleRunes-1] + "…\ttoday 09:00",
	}
	if !reflect.DeepEqual(titles, want) {
		t.Fatalf("titles = %q\nwant %q", titles, want)
	}
}

func TestSessionTimeLabel(t *testing.T) {
	tests := []struct {
		at, now time.Time
		want    string
	}{
		{sessionAt(10, 18, 0, 5), sessionTestNow, "today 00:05"},
		{sessionAt(10, 17, 23, 59), sessionTestNow, "yesterday 23:59"},
		{sessionAt(10, 8, 3, 24), sessionTestNow, "Oct 8"},
		{time.Date(2025, 12, 31, 22, 0, 0, 0, time.UTC), sessionTestNow, "Dec 31, 2025"},
		{time.Date(2025, 12, 31, 22, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC), "yesterday 22:00"},
		{time.Date(2026, 10, 18, 20, 0, 0, 0, time.UTC), time.Date(2026, 10, 19, 1, 0, 0, 0, time.FixedZone("EEST", 3*3600)), "yesterday 23:00"},
	}
	for _, test := range tests {
		if got := sessionTimeLabel(test.at, test.now); got != test.want {
			t.Errorf("sessionTimeLabel(%s, %s) = %q, want %q", test.at, test.now, got, test.want)
		}
	}
}

func TestContinueSessionPromptSaysWhatTheSwitchWillDo(t *testing.T) {
	session := sessions.Session{ID: "local_1", Title: "Găsește materiale și calculatoare, scrie e-mailurile", Group: "Sample Project One", LastActivity: sessionAt(10, 18, 15, 13)}

	title, message := continueSessionPrompt(session, "private@accounts.test", nil, sessionTestNow)

	if title != "Continue on private@accounts.test?" {
		t.Fatalf("title = %q", title)
	}
	want := "Găsește materiale și calculatoare, scrie e-mailurile\n" +
		"Sample Project One · last activity today 15:13\n\n" +
		"Claude Desktop will close and reopen on private@accounts.test (about half a minute), then this session opens."
	if message != want {
		t.Fatalf("message = %q"+"\nwant %q", message, want)
	}

	_, busy := continueSessionPrompt(session, "private@accounts.test", []string{"Planificare rapidă mesei"}, sessionTestNow)
	if want := want + "\n\nThese Claude Code sessions are working right now and will stop:\n• Planificare rapidă mesei"; busy != want {
		t.Fatalf("message with busy sessions = %q"+"\nwant %q", busy, want)
	}
}

func TestSwitchPromptAsksEvenWhenNoSessionIsWorking(t *testing.T) {
	title, message := switchPrompt("private@accounts.test", nil)
	if title != "Switch to private@accounts.test?" {
		t.Fatalf("title = %q", title)
	}
	want := "Claude Desktop will close and reopen on private@accounts.test (about half a minute).\n\n" +
		"No Claude Code session is working right now."
	if message != want {
		t.Fatalf("message = %q"+"\nwant %q", message, want)
	}

	_, busy := switchPrompt("private@accounts.test", []string{"Planificare rapidă mesei"})
	want = "These Claude Code sessions are working right now and will stop:\n• Planificare rapidă mesei\n\n" +
		"Claude Desktop will close and reopen on private@accounts.test (about half a minute). You can continue these sessions afterwards.\n\nSwitch anyway?"
	if busy != want {
		t.Fatalf("message with busy sessions = %q"+"\nwant %q", busy, want)
	}
}

func TestAddAccountPromptAsksBeforeClaudeCloses(t *testing.T) {
	title, message := addAccountPrompt(nil)
	if title != "Add another account?" {
		t.Fatalf("title = %q", title)
	}
	want := "Claude Desktop will close and reopen signed out, so you can sign in with the other account.\n\n" +
		"No Claude Code session is working right now."
	if message != want {
		t.Fatalf("message = %q"+"\nwant %q", message, want)
	}
	if _, busy := addAccountPrompt([]string{"Site design"}); !strings.HasSuffix(busy, "You can continue these sessions afterwards.\n\nAdd it anyway?") {
		t.Fatalf("message with busy sessions = %q", busy)
	}
}

func TestContinueMenuTitleSaysWhereTheAccountIsOpen(t *testing.T) {
	tests := []struct {
		name         string
		inMainWindow bool
		separate     separateWindowState
		want         string
	}{
		{"closed", false, separateWindowState{}, "private@accounts.test"},
		{"in the main window", true, separateWindowState{}, "✓ private@accounts.test (in use)"},
		{"in its separate window", false, separateWindowState{open: true}, "✓ private@accounts.test (separate window)"},
		{"its window holds another account", false, separateWindowState{open: true, otherAccount: true}, "private@accounts.test"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := continueMenuTitle("private@accounts.test", test.inMainWindow, test.separate); got != test.want {
				t.Fatalf("continueMenuTitle() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStopWorkNoticeListsAtMostFiveSessions(t *testing.T) {
	working := []string{"One", "Two\nlines", "Three", "Four", "Five", "Six", "Seven"}
	want := "These Claude Code sessions are working right now and will stop:\n" +
		"• One\n• Two lines\n• Three\n• Four\n• Five\n• and 2 more\n"
	if got := stopWorkNotice(working); got != want {
		t.Fatalf("notice = %q"+"\nwant %q", got, want)
	}
}

func TestSessionMenuEntriesFollowTheSidebar(t *testing.T) {
	at := sessionAt(10, 18, 12, 0)
	list := []sessions.Session{
		{ID: "local_a", Title: "Switcher", Group: "SwitcherGroups", GroupRank: 3, ParentID: "local_b", LastActivity: at},
		{ID: "local_c", Title: "Lectura", Group: "SampleRepoB", LastActivity: at},
		{ID: "local_b", Title: "Stabilize", Group: "SwitcherGroups", GroupRank: 3, LastActivity: at},
		{ID: "local_d", Title: "Harden", Group: "SampleRepoB", ParentID: "local_c", LastActivity: at},
		{ID: "local_e", Title: "Video", Group: "Demo Video Studio", LastActivity: at},
		{ID: "local_f", Title: "Started elsewhere", Group: "SampleRepoB", ParentID: "local_missing", LastActivity: at},
		{ID: "local_g", Title: "Loop one", Group: "Loops", ParentID: "local_h", LastActivity: at},
		{ID: "local_h", Title: "Loop two", Group: "Loops", ParentID: "local_g", LastActivity: at},
	}

	titles, ids := entryTitlesAndIDs(sessionMenuEntries(list, sessionTestNow))

	suffix := "\ttoday 12:00"
	wantTitles := []string{
		"SwitcherGroups", "Switcher" + suffix, "Stabilize" + suffix,
		"Demo Video Studio", "Video" + suffix,
		"Loops", "Loop one" + suffix, "└ Loop two" + suffix,
		"SampleRepoB", "Lectura" + suffix, "└ Harden" + suffix, "Started elsewhere" + suffix,
	}
	wantIDs := []string{"", "local_a", "local_b", "", "local_e", "", "local_g", "local_h", "", "local_c", "local_d", "local_f"}
	if !reflect.DeepEqual(titles, wantTitles) || !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("entries = %q %q\nwant %q %q", titles, ids, wantTitles, wantIDs)
	}
}
