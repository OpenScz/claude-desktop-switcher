//go:build windows

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/TinoA/claude-desktop-switcher/internal/platform"
	"github.com/TinoA/claude-desktop-switcher/internal/profile"
	"github.com/TinoA/claude-desktop-switcher/internal/sessions"
	"github.com/getlantern/systray"
)

const (
	recentSessionsPerAccount = 10
	sessionMenuSlots         = 2 * recentSessionsPerAccount
	sessionTitleRunes        = 60
	sessionWindowTimeout     = 30 * time.Second
	sessionOpeningCardTime   = 1500 * time.Millisecond
	maxListedWork            = 5
)

type sessionMenu struct {
	item    *systray.MenuItem
	title   string
	slots   []*systray.MenuItem
	visible []bool
	enabled []bool
	shown   []sessionEntry
	stop    chan struct{}
}

type sessionEntry struct {
	title   string
	session *sessions.Session
}

func (s *trayState) readRecentSessions(profiles []profile.Meta, active string) (map[string][]sessions.Session, bool) {
	if s.sessionReader == nil || s.backgroundReadsPaused() {
		return nil, false
	}
	appData, err := s.platform.AppDataPath()
	if err != nil {
		return nil, false
	}
	accounts := make([]string, len(profiles))
	activeAccount := ""
	for index, meta := range profiles {
		accounts[index] = meta.AccountUUIDHash
		if meta.Name == active {
			activeAccount = meta.AccountUUIDHash
		}
	}
	recent, err := s.sessionReader.Recent(appData, accounts, activeAccount, recentSessionsPerAccount)
	message := ""
	if err != nil {
		message = err.Error()
	}
	s.mu.Lock()
	changed := message != s.sessionReadErr
	s.sessionReadErr = message
	s.mu.Unlock()
	if changed && message != "" {
		s.setStatus("Could not read Claude Code sessions: " + message)
	}
	return recent, err == nil
}

func (s *trayState) updateSessionMenusLocked(profiles []profile.Meta, active string, running, enabled bool, recent map[string][]sessions.Session, read bool) {
	if len(profiles) == 0 {
		s.continueMenu.Hide()
	} else {
		s.continueMenu.Show()
	}
	now := time.Now()
	seen := make(map[string]bool, len(profiles))
	for _, meta := range profiles {
		seen[meta.Name] = true
		label := s.profileLabels[meta.Name]
		menu := s.sessionMenus[meta.Name]
		if menu == nil {
			menu = s.newSessionMenuLocked(meta.Name, label)
		}
		title := menuLabel(label)
		if running && active == meta.Name {
			title = "✓ " + title + " (in use)"
		}
		if title != menu.title {
			menu.item.SetTitle(title)
			menu.title = title
		}
		entries := menu.shown
		switch {
		case read:
			entries = sessionMenuEntries(recent[meta.AccountUUIDHash], now)
		case entries == nil:
			entries = []sessionEntry{{title: "Reading sessions..."}}
		}
		menu.update(entries, enabled)
	}
	for name := range s.sessionMenus {
		if !seen[name] {
			s.retireSessionMenuLocked(name)
		}
	}
}

func (s *trayState) newSessionMenuLocked(name, label string) *sessionMenu {
	menu := &sessionMenu{
		item:    s.continueMenu.AddSubMenuItem(menuLabel(label), "Recent Claude Code sessions of "+label),
		title:   menuLabel(label),
		slots:   make([]*systray.MenuItem, sessionMenuSlots),
		visible: make([]bool, sessionMenuSlots),
		enabled: make([]bool, sessionMenuSlots),
		stop:    make(chan struct{}),
	}
	for index := range menu.slots {
		menu.slots[index] = menu.item.AddSubMenuItem("", "")
		menu.visible[index] = true
		menu.enabled[index] = true
		go s.watchSessionSlot(menu, name, index)
	}
	s.sessionMenus[name] = menu
	return menu
}

// systray re-inserts a hidden item whenever its title or state changes, so a
// slot past the end of the list is only ever hidden, never updated. Unchanged
// slots are left alone so an open menu does not repaint every refresh.
func (m *sessionMenu) update(entries []sessionEntry, enabled bool) {
	for index, slot := range m.slots {
		if index >= len(entries) {
			if m.visible[index] {
				slot.Hide()
				m.visible[index] = false
			}
			continue
		}
		entry := entries[index]
		clickable := entry.session != nil && enabled
		if m.visible[index] && index < len(m.shown) && m.shown[index].title == entry.title && m.enabled[index] == clickable {
			continue
		}
		slot.SetTitle(entry.title)
		if clickable {
			slot.Enable()
		} else {
			slot.Disable()
		}
		m.visible[index] = true
		m.enabled[index] = clickable
	}
	m.shown = entries
}

func (s *trayState) disableSessionSlotsLocked() {
	for _, menu := range s.sessionMenus {
		for index, slot := range menu.slots {
			if menu.visible[index] && menu.enabled[index] {
				slot.Disable()
				menu.enabled[index] = false
			}
		}
	}
}

func (s *trayState) retireSessionMenuLocked(name string) {
	menu, ok := s.sessionMenus[name]
	if !ok {
		return
	}
	retireMenuItem(menu.item)
	close(menu.stop)
	delete(s.sessionMenus, name)
}

func (s *trayState) watchSessionSlot(menu *sessionMenu, name string, index int) {
	for {
		select {
		case <-menu.stop:
			return
		case _, ok := <-menu.slots[index].ClickedCh:
			if !ok {
				return
			}
		}
		s.mu.Lock()
		var session *sessions.Session
		if index < len(menu.shown) {
			session = menu.shown[index].session
		}
		s.mu.Unlock()
		if session != nil {
			s.continueSession(name, *session)
		}
	}
}

func (s *trayState) continueSession(name string, session sessions.Session) {
	if s.initializingSnapshot() || s.switchingSnapshot() || s.workflowSnapshot() != nil {
		s.setStatus("Wait for the current operation to finish")
		return
	}
	label := s.cachedProfileDisplayName(name)
	link := sessions.ContinueURL(session.ID)
	running, active := s.claudeStateSnapshot()
	if running && active == name {
		if err := openURL(link); err != nil {
			s.setStatus("Could not open the session: " + err.Error())
			trayWarning("Could not open the session", err.Error())
			return
		}
		s.setStatus("Session opened on " + label)
		return
	}
	if running {
		title, message := continueSessionPrompt(session, label, s.workingSessions(), time.Now())
		if confirmed, err := nativeTrayConfirm(title, message); err != nil || !confirmed {
			return
		}
	}
	if !s.beginSwitch() {
		s.setStatus("Wait for the current operation to finish")
		return
	}
	s.setStatus("Continuing a session on " + label + "...")
	go s.switchAndContinue(name, label, link)
}

func (s *trayState) switchAndContinue(name, label, link string) {
	err := switchProfileFromTray(name, s.store, s.platform, s.launchPath())
	if err != nil {
		s.endSwitch()
		if errors.Is(err, errLiveSessionUnrecognized) || errors.Is(err, errLiveSessionUnverified) {
			s.setStatus("Continue skipped: the open Claude account could not be identified; no profiles were changed")
			trayWarning("Could not continue the session", "The account open in Claude Desktop could not be identified, so nothing was changed.")
			return
		}
		s.setStatus("Account switch error: " + err.Error())
		trayWarning("Could not switch account", err.Error())
		return
	}
	s.setTrustedActiveProfile(name)
	card := startNativeOverlay("Opening the session...", false, s.launchPath())
	err = openSessionWhenClaudeIsReady(s.platform, link)
	time.Sleep(sessionOpeningCardTime)
	card.Close()
	s.endSwitch()
	if err != nil {
		s.setStatus("Could not open the session: " + err.Error())
		trayWarning("Could not open the session", "Claude Desktop is open on "+label+", but the session could not be opened.\n\n"+err.Error())
		return
	}
	s.setStatus("Session opened on " + label)
}

// A link sent while Claude is still starting can reach a process that is not
// yet the running instance, so it waits for Claude's window first.
func openSessionWhenClaudeIsReady(p platform.Platform, link string) error {
	if waiter, ok := p.(platform.LoginWindowWaiter); ok {
		ctx, cancel := context.WithTimeout(context.Background(), sessionWindowTimeout)
		_ = waiter.WaitForLoginWindow(ctx)
		cancel()
	}
	return openURL(link)
}

func (s *trayState) claudeStateSnapshot() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claudeRunning, s.activeProfile
}

// The groups follow Claude's sidebar: the user's own groups first, in their
// sidebar order, then the repository groups by name.
func sessionMenuEntries(list []sessions.Session, now time.Time) []sessionEntry {
	if len(list) == 0 {
		return []sessionEntry{{title: "No Claude Code sessions yet"}}
	}
	var groups []string
	rank := make(map[string]int)
	members := make(map[string][]sessions.Session)
	for _, session := range list {
		if _, ok := members[session.Group]; !ok {
			groups = append(groups, session.Group)
			rank[session.Group] = session.GroupRank
		}
		members[session.Group] = append(members[session.Group], session)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		left, right := rank[groups[i]], rank[groups[j]]
		if (left > 0) != (right > 0) {
			return left > 0
		}
		if left != right {
			return left < right
		}
		return strings.ToLower(groups[i]) < strings.ToLower(groups[j])
	})
	entries := make([]sessionEntry, 0, len(groups)+len(list))
	for _, group := range groups {
		entries = append(entries, sessionEntry{title: menuLabel(plainText(group))})
		entries = appendGroupSessions(entries, members[group], rank[group] == 0, now)
	}
	return entries
}

// The sidebar shows a side session under the session that started it, except
// in groups the user made.
func appendGroupSessions(entries []sessionEntry, group []sessions.Session, nest bool, now time.Time) []sessionEntry {
	listed := make(map[string]bool, len(group))
	for _, session := range group {
		listed[session.ID] = true
	}
	nested := func(session sessions.Session) bool {
		return nest && session.ParentID != "" && session.ParentID != session.ID && listed[session.ParentID]
	}
	children := make(map[string][]sessions.Session)
	for _, session := range group {
		if nested(session) {
			children[session.ParentID] = append(children[session.ParentID], session)
		}
	}
	placed := make(map[string]bool, len(group))
	var add func(session sessions.Session, prefix string)
	add = func(session sessions.Session, prefix string) {
		if placed[session.ID] {
			return
		}
		placed[session.ID] = true
		title := prefix + menuLabel(shortTitle(session.Title)) + "\t" + sessionTimeLabel(session.LastActivity, now)
		entries = append(entries, sessionEntry{title: title, session: &session})
		for _, child := range children[session.ID] {
			add(child, "└ ")
		}
	}
	for _, session := range group {
		if !nested(session) {
			add(session, "")
		}
	}
	for _, session := range group {
		add(session, "")
	}
	return entries
}

func continueSessionPrompt(session sessions.Session, target string, working []string, now time.Time) (string, string) {
	message := plainText(session.Title) + "\n" +
		plainText(session.Group) + " · last activity " + sessionTimeLabel(session.LastActivity, now) + "\n\n" +
		"Claude Desktop will close and reopen on " + target + " (about half a minute), then this session opens."
	if len(working) > 0 {
		message += "\n\n" + strings.TrimSuffix(stopWorkNotice(working), "\n")
	}
	return "Continue on " + target + "?", message
}

func (s *trayState) workingSessions() []string {
	appData, err := s.platform.AppDataPath()
	if err != nil {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return sessions.Working(filepath.Join(home, ".claude"), appData)
}

// Switching and adding an account close Claude Desktop on the way, so they
// always ask first and say whether any Claude Code session is working.
func closingPrompt(title, consequence, question string, working []string) (string, string) {
	if len(working) == 0 {
		return title, consequence + "\n\nNo Claude Code session is working right now."
	}
	return title, stopWorkNotice(working) + "\n" + consequence + " You can continue these sessions afterwards.\n\n" + question
}

func switchPrompt(label string, working []string) (string, string) {
	return closingPrompt("Switch to "+label+"?", "Claude Desktop will close and reopen on "+label+" (about half a minute).", "Switch anyway?", working)
}

func addAccountPrompt(working []string) (string, string) {
	return closingPrompt("Add another account?", "Claude Desktop will close and reopen signed out, so you can sign in with the other account.", "Add it anyway?", working)
}

// Closing Claude Desktop stops every Claude Code session running a turn in it,
// so actions that close it ask first while any is busy.
func confirmStoppingWork(title, consequence, question string, working []string) bool {
	confirmed, err := nativeTrayConfirm(title, stopWorkNotice(working)+"\n"+consequence+"\n\n"+question)
	return err == nil && confirmed
}

func stopWorkNotice(working []string) string {
	var notice strings.Builder
	notice.WriteString("These Claude Code sessions are working right now and will stop:\n")
	for index, title := range working {
		if index == maxListedWork {
			fmt.Fprintf(&notice, "• and %d more\n", len(working)-index)
			break
		}
		notice.WriteString("• " + plainText(title) + "\n")
	}
	return notice.String()
}

func sessionTimeLabel(at, now time.Time) string {
	at = at.In(now.Location())
	switch yesterday := now.AddDate(0, 0, -1); {
	case sameDay(at, now):
		return "today " + at.Format("15:04")
	case sameDay(at, yesterday):
		return "yesterday " + at.Format("15:04")
	case at.Year() == now.Year():
		return at.Format("Jan 2")
	default:
		return at.Format("Jan 2, 2006")
	}
}

func sameDay(left, right time.Time) bool {
	leftYear, leftMonth, leftDay := left.Date()
	rightYear, rightMonth, rightDay := right.Date()
	return leftYear == rightYear && leftMonth == rightMonth && leftDay == rightDay
}

// Session titles come from Claude's files; control and format characters
// would break the menu or the message box, which stops at a NUL.
func plainText(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsSpace(r), unicode.IsControl(r):
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func shortTitle(title string) string {
	runes := []rune(plainText(title))
	if len(runes) <= sessionTitleRunes {
		return string(runes)
	}
	return strings.TrimSpace(string(runes[:sessionTitleRunes-1])) + "…"
}

// Windows menus underline the letter after "&" unless it is doubled.
func menuLabel(value string) string {
	return strings.ReplaceAll(value, "&", "&&")
}
