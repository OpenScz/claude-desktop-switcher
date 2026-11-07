//go:build windows

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TinoA/claude-desktop-switcher/internal/platform"
	"github.com/TinoA/claude-desktop-switcher/internal/profile"
	"github.com/TinoA/claude-desktop-switcher/internal/sessions"
	"github.com/getlantern/systray"
)

const (
	separateFolderName  = "Claude-Accounts"
	separateStartWait   = 15 * time.Second
	separateCardMinimum = 1500 * time.Millisecond
)

type separateWindowState struct {
	open         bool
	otherAccount bool
}

// Each account's separate window keeps its own sign-in in a folder of its own,
// outside the Store package's redirected Roaming folder.
func separateWindowFolder(name string) (string, error) {
	local, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(local, separateFolderName, name), nil
}

func (s *trayState) readSeparateWindows(profiles []profile.Meta) map[string]separateWindowState {
	runner, ok := s.platform.(platform.SeparateWindowRunner)
	if !ok || s.backgroundReadsPaused() {
		return nil
	}
	open, err := runner.SeparateWindows()
	if err != nil {
		return nil
	}
	states := make(map[string]separateWindowState, len(profiles))
	for _, meta := range profiles {
		folder, err := separateWindowFolder(meta.Name)
		if err != nil {
			continue
		}
		signedIn := profile.AccountUUIDHashAt(folder)
		states[meta.Name] = separateWindowState{
			open:         open[normalizedFolder(folder)],
			otherAccount: signedIn != "" && meta.AccountUUIDHash != "" && signedIn != meta.AccountUUIDHash,
		}
	}
	return states
}

func normalizedFolder(folder string) string {
	if absolute, err := filepath.Abs(folder); err == nil {
		folder = absolute
	}
	return strings.ToLower(filepath.Clean(folder))
}

func (s *trayState) updateSeparateMenuLocked(profiles []profile.Meta, active string, running, enabled bool, states map[string]separateWindowState) {
	if s.separateMenu == nil {
		return
	}
	if _, ok := s.platform.(platform.SeparateWindowRunner); !ok || len(profiles) == 0 {
		s.separateMenu.Hide()
		return
	}
	s.separateMenu.Show()
	seen := make(map[string]bool, len(profiles))
	for _, meta := range profiles {
		seen[meta.Name] = true
		item, ok := s.separateItems[meta.Name]
		if !ok {
			item = s.separateMenu.AddSubMenuItem(menuLabel(s.profileLabels[meta.Name]), "Open this account in a separate Claude window")
			s.separateItems[meta.Name] = item
			stop := make(chan struct{})
			s.separateStops[meta.Name] = stop
			go s.watchSeparateItem(item, meta.Name, stop)
		}
		state, known := states[meta.Name]
		if !known {
			state = s.separateShown[meta.Name]
		}
		s.separateShown[meta.Name] = state
		title, clickable := separateMenuState(s.profileLabels[meta.Name], running && active == meta.Name, state, enabled)
		item.SetTitle(title)
		if clickable {
			item.Enable()
		} else {
			item.Disable()
		}
		item.Show()
	}
	for name := range s.separateItems {
		if !seen[name] {
			s.retireSeparateItemLocked(name)
		}
	}
}

func separateMenuState(label string, inMainWindow bool, state separateWindowState, enabled bool) (string, bool) {
	title := menuLabel(label)
	switch {
	case inMainWindow:
		return title + " (main window)", false
	case state.open:
		title = "✓ " + title + " (open)"
	}
	if state.otherAccount {
		title += " - another account is signed in there"
	}
	return title, enabled
}

// Claude's tray icons look the same for every window, so while a separate
// window is open, Close Claude Desktop becomes a submenu naming each window's
// account.
func (s *trayState) updateCloseMenuLocked(profiles []profile.Meta, active string, enabled bool) {
	if s.closeMenu == nil {
		return
	}
	s.separateOpen = false
	open := make(map[string]bool, len(profiles))
	for _, meta := range profiles {
		if !s.separateShown[meta.Name].open {
			continue
		}
		s.separateOpen = true
		open[meta.Name] = true
		item, ok := s.closeItems[meta.Name]
		if !ok {
			item = s.closeMenu.AddSubMenuItem("", "Close this account's separate Claude window")
			s.closeItems[meta.Name] = item
			go s.watchCloseItem(item, meta.Name)
		}
		item.SetTitle(windowTitle(s.profileLabels[meta.Name], false))
		if enabled {
			item.Enable()
		} else {
			item.Disable()
		}
		item.Show()
	}
	for name, item := range s.closeItems {
		if !open[name] {
			item.Hide()
		}
	}
	s.closeMain.SetTitle(windowTitle(s.profileLabels[active], true))
	if enabled {
		s.closeMain.Enable()
	} else {
		s.closeMain.Disable()
	}
	if s.claudeRunning {
		s.closeMain.Show()
	} else {
		s.closeMain.Hide()
	}
	if s.separateOpen {
		s.closeMenu.Show()
	} else {
		s.closeMenu.Hide()
	}
	if claudeItemVisible(s.claudeRunning, s.separateOpen) {
		s.claude.Show()
	} else {
		s.claude.Hide()
	}
}

func windowTitle(label string, main bool) string {
	switch {
	case main && label == "":
		return "Main window"
	case main:
		return menuLabel(label) + " (main window)"
	}
	return menuLabel(label) + " (separate window)"
}

func (s *trayState) watchCloseItem(item *systray.MenuItem, name string) {
	for range item.ClickedCh {
		s.closeSeparateWindow(name)
	}
}

func (s *trayState) closeSeparateWindow(name string) {
	folder, err := separateWindowFolder(name)
	if err != nil {
		return
	}
	if home, err := os.UserHomeDir(); err == nil {
		if working := sessions.Working(filepath.Join(home, ".claude"), folder); len(working) > 0 &&
			!confirmStoppingWork("Close the separate window of "+s.cachedProfileDisplayName(name)+"?", "You can continue these sessions after it opens again.", "Close anyway?", working) {
			return
		}
	}
	if s.initializingSnapshot() || s.switchingSnapshot() || s.workflowSnapshot() != nil {
		s.setStatus("Wait for the current operation to finish")
		return
	}
	runner, ok := s.platform.(platform.SeparateWindowRunner)
	if !ok {
		return
	}
	label := s.cachedProfileDisplayName(name)
	s.setStatus("Closing the separate window of " + label)
	card := startNativeOverlay("Closing the separate window...", false, s.launchPath())
	err = runner.CloseSeparateWindow(folder)
	card.Close()
	if err != nil {
		s.setStatus("Could not close the separate window: " + err.Error())
		trayWarning("Could not close the separate window", err.Error())
		return
	}
	s.refreshAccounts()
}

func (s *trayState) disableSeparateItemsLocked() {
	for _, item := range s.separateItems {
		item.Disable()
	}
	for _, item := range s.closeItems {
		item.Disable()
	}
	if s.closeMain != nil {
		s.closeMain.Disable()
	}
}

func (s *trayState) retireSeparateItemLocked(name string) {
	item, ok := s.separateItems[name]
	if !ok {
		return
	}
	retireMenuItem(item)
	stopMenuWatcher(s.separateStops, name)
	delete(s.separateItems, name)
	delete(s.separateShown, name)
}

func (s *trayState) watchSeparateItem(item *systray.MenuItem, name string, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case _, ok := <-item.ClickedCh:
			if !ok {
				return
			}
		}
		s.openSeparateWindow(name)
	}
}

func (s *trayState) openSeparateWindow(name string) {
	if s.initializingSnapshot() || s.switchingSnapshot() || s.workflowSnapshot() != nil {
		s.setStatus("Wait for the current operation to finish")
		return
	}
	runner, ok := s.platform.(platform.SeparateWindowRunner)
	if !ok {
		return
	}
	label := s.cachedProfileDisplayName(name)
	if running, active := s.claudeStateSnapshot(); running && active == name {
		trayWarning(ProductName, label+" is open in the main Claude window.")
		return
	}
	folder, err := separateWindowFolder(name)
	if err != nil {
		trayWarning("Could not open a separate window", err.Error())
		return
	}
	if _, err := os.Stat(folder); errors.Is(err, os.ErrNotExist) {
		title, message := separateWindowPrompt(label)
		if confirmed, err := nativeTrayConfirm(title, message); err != nil || !confirmed {
			return
		}
	}
	if open, err := runner.SeparateWindows(); err == nil && !open[normalizedFolder(folder)] {
		if err := s.prepareSeparateWindow(name, folder); err != nil {
			s.setStatus("Could not open a separate window: " + err.Error())
			trayWarning("Could not open a separate window", "The sessions of "+label+" could not be prepared for the window.\n\n"+err.Error())
			return
		}
	}
	s.setStatus("Opening a separate window for " + label)
	card := startNativeOverlay("Opening a separate window...", false, s.launchPath())
	shown := time.Now()
	err = runner.OpenSeparateWindow(folder, "")
	if err == nil {
		deadline := time.Now().Add(separateStartWait)
		for time.Now().Before(deadline) {
			if open, readErr := runner.SeparateWindows(); readErr == nil && open[normalizedFolder(folder)] {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	if wait := separateCardMinimum - time.Since(shown); wait > 0 {
		time.Sleep(wait)
	}
	card.Close()
	if err != nil {
		s.setStatus("Could not open a separate window: " + err.Error())
		trayWarning("Could not open a separate window", err.Error())
		return
	}
	s.refreshAccounts()
}

// A separate window's sessions are copied to and from the main folder, so an
// account signed in to one of them must not open in the main window too: both
// windows would change the same sessions.
func (s *trayState) separateWindowWith(name string) (string, bool) {
	runner, ok := s.platform.(platform.SeparateWindowRunner)
	if !ok {
		return "", false
	}
	open, err := runner.SeparateWindows()
	if err != nil || len(open) == 0 {
		return "", false
	}
	profiles, err := s.store.List()
	if err != nil {
		return "", false
	}
	account := profileAccount(profiles, name)
	if account == "" {
		return "", false
	}
	for _, meta := range profiles {
		folder, err := separateWindowFolder(meta.Name)
		if err == nil && open[normalizedFolder(folder)] && profile.AccountUUIDHashAt(folder) == account {
			return folder, true
		}
	}
	return "", false
}

// Claude refuses to save its records through a link, so a separate window
// keeps a records folder of its own. Before it starts, whatever it saved last
// time goes to the main folder, then its account's sessions come back from
// there with the worktree reservations they hold.
func (s *trayState) prepareSeparateWindow(name, folder string) error {
	appData, err := s.platform.AppDataPath()
	if err != nil {
		return err
	}
	profiles, err := s.store.List()
	if err != nil {
		return err
	}
	account := profile.AccountUUIDHashAt(folder)
	if account == "" {
		account = profileAccount(profiles, name)
	}
	if account != profile.AccountUUIDHashAt(appData) {
		if _, err := sessions.MirrorAccount(folder, appData, account); err != nil {
			return err
		}
	}
	if _, err := sessions.MirrorAccount(appData, folder, account); err != nil {
		return err
	}
	return sessions.CopyWorktreeLeases(appData, folder, account)
}

// What a separate window saves is copied to the main folder on every refresh,
// so Continue a session lists it and the main window has it after a switch.
// The account signed in to the main window is left to that window.
func (s *trayState) syncSeparateWindows() {
	if _, ok := s.platform.(platform.SeparateWindowRunner); !ok || s.backgroundReadsPaused() {
		return
	}
	appData, err := s.platform.AppDataPath()
	if err != nil {
		return
	}
	profiles, err := s.store.List()
	if err != nil {
		return
	}
	mainAccount := profile.AccountUUIDHashAt(appData)
	message := ""
	for _, meta := range profiles {
		folder, err := separateWindowFolder(meta.Name)
		if err != nil {
			continue
		}
		account := profile.AccountUUIDHashAt(folder)
		if account == "" || account == mainAccount {
			continue
		}
		if _, err := sessions.MirrorAccount(folder, appData, account); err != nil {
			message = "Could not copy the sessions of a separate window: " + err.Error()
		}
	}
	s.mu.Lock()
	changed := message != s.separateSyncErr
	s.separateSyncErr = message
	s.mu.Unlock()
	if changed && message != "" {
		s.setStatus(message)
	}
}

func profileAccount(profiles []profile.Meta, name string) string {
	for _, meta := range profiles {
		if meta.Name == name {
			return meta.AccountUUIDHash
		}
	}
	return ""
}

func (s *trayState) offerSeparateWindow(label, folder string) {
	title, message := separateWindowInUsePrompt(label)
	if confirmed, err := nativeTrayConfirm(title, message); err != nil || !confirmed {
		return
	}
	if err := s.platform.(platform.SeparateWindowRunner).OpenSeparateWindow(folder, ""); err != nil {
		s.setStatus("Could not bring the separate window forward: " + err.Error())
		trayWarning("Could not bring the separate window forward", err.Error())
	}
}

func separateWindowInUsePrompt(label string) (string, string) {
	return label + " is open in a separate window",
		label + " is already signed in to a separate Claude window. Opening it in the main window too would let both windows change the same sessions.\n\n" +
			"Bring that window to the front?"
}

func separateWindowPrompt(label string) (string, string) {
	return "Open " + label + " in a separate window?",
		"A second Claude Desktop window opens for " + label + ", next to the main one.\n\n" +
			"The first time it opens signed out: choose “Sign in with code” and sign in as " + label + ".\n\n" +
			"It uses about 2 GB of memory while it is open."
}
