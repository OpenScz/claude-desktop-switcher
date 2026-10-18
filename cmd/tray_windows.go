//go:build windows

package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/TinoA/claude-desktop-switcher/internal/platform"
	"github.com/TinoA/claude-desktop-switcher/internal/profile"
	"github.com/getlantern/systray"
	"github.com/spf13/cobra"
	"golang.org/x/sys/windows"
)

const (
	loginWindowTimeout           = 30 * time.Second
	profileEmailDiscoveryTimeout = 60 * time.Second
)

//go:embed assets/windows-claude-swap-tray.ico
var trayIcon []byte

var (
	errDeleteSessionUnknown      = errors.New("claude session cannot be verified")
	errDeleteSessionUnrecognized = errors.New("claude session does not match a saved account")
)

var cmdTray = &cobra.Command{
	Use:   "tray",
	Short: "Run Claude Desktop Switcher in the system tray",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTray()
	},
}

type trayState struct {
	mu                 sync.Mutex
	store              *profile.Store
	platform           platform.Platform
	trayLock           *operationLock
	root               *systray.MenuItem
	claude             *systray.MenuItem
	add                *systray.MenuItem
	delete             *systray.MenuItem
	export             *systray.MenuItem
	exportPassword     *systray.MenuItem
	exportLocal        *systray.MenuItem
	importer           *systray.MenuItem
	update             *systray.MenuItem
	version            *systray.MenuItem
	items              map[string]*systray.MenuItem
	deleteItems        map[string]*systray.MenuItem
	itemStops          map[string]chan struct{}
	deleteItemStops    map[string]chan struct{}
	profileLabels      map[string]string
	workflow           *addWorkflow
	claudeInstalled    bool
	claudeRunning      bool
	activeProfile      string
	trustedActive      string
	switching          bool
	initializing       bool
	initialSaveOffered bool
	confirmingClose    bool
	emailDiscovering   map[string]bool
	activityLog        *activityLogger
	activityLogErr     error
}

func runTray() (resultErr error) {
	activityLog, activityLogErr := newActivityLogger()
	logStartup := func(value string) {
		if activityLog != nil {
			_ = activityLog.Write(value)
		}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			resultErr = fmt.Errorf("tray startup panic: %v", recovered)
			logStartup(resultErr.Error())
			trayWarning("Could not start "+ProductName, resultErr.Error())
		}
	}()

	logStartup("Tray launch requested; version=" + displayVersion(Version))
	if err := setCurrentProcessAppUserModelID(); err != nil {
		logStartup("Could not set Windows app identity: " + err.Error())
	}
	logStartup("Acquiring tray lock")
	lock, err := acquireTrayLock()
	if err != nil {
		logStartup("Could not acquire tray lock: " + err.Error())
		return err
	}
	defer lock.Release()
	logStartup("Tray lock acquired")
	store, err := profile.NewStore()
	if err != nil {
		logStartup("Could not open profile store: " + err.Error())
		return err
	}
	logStartup("Profile store ready")
	state := &trayState{store: store, platform: platform.Current(), trayLock: lock, items: make(map[string]*systray.MenuItem), deleteItems: make(map[string]*systray.MenuItem), itemStops: make(map[string]chan struct{}), deleteItemStops: make(map[string]chan struct{}), profileLabels: make(map[string]string), emailDiscovering: make(map[string]bool), initializing: true, activityLog: activityLog, activityLogErr: activityLogErr}
	state.setStatus("Tray started; version=" + displayVersion(Version))
	systray.Run(func() { state.ready() }, func() {
		state.setStatus("Tray stopped")
		lock.Release()
	})
	return nil
}

func acquireTrayLock() (*operationLock, error) {
	return acquireOperationLock("tray")
}

func (s *trayState) ready() {
	systray.SetIcon(trayIcon)
	systray.SetTitle(ProductName)
	systray.SetTooltip(ProductName + " - Claude Desktop account switcher")

	s.root = systray.AddMenuItem("Accounts", "Switch account")
	s.add = systray.AddMenuItem("Add account...", "Open Claude to sign in with another account")
	s.delete = systray.AddMenuItem("Delete account...", "Delete a local Switcher copy")
	systray.AddSeparator()
	s.export = systray.AddMenuItem("Backup", "Save or restore accounts and sessions")
	s.exportPassword = s.export.AddSubMenuItem("Password-protected...", "Create a portable, password-protected backup")
	s.exportLocal = s.export.AddSubMenuItem("Without password...", "Protect the backup with this Windows account")
	s.importer = systray.AddMenuItem("Import backup", "Automatically detect and restore a backup")
	s.update = systray.AddMenuItem("New version available", "Open the latest Claude Desktop Switcher release")
	s.update.Hide()
	systray.AddSeparator()
	s.claude = systray.AddMenuItem("Claude Desktop: Checking...", "Open or close Claude Desktop")
	s.claude.Disable()
	logs := systray.AddMenuItem("Open logs folder", "Open local diagnostic activity logs")
	s.version = systray.AddMenuItem("Current version: "+displayVersion(Version), "Open the Claude Desktop Switcher GitHub repository")
	quit := systray.AddMenuItem("Exit", "Close the tray icon")

	s.loadAccounts()
	s.restorePendingIfPresent()

	go s.handleAdd()
	go s.handleClaude()
	go s.handleBackupExport(s.exportPassword, false)
	go s.handleBackupExport(s.exportLocal, true)
	go s.handleBackupImport()
	go s.handleUpdate()
	go s.handleVersion()
	go s.handleLogs(logs)
	go func() {
		<-quit.ClickedCh
		systray.Quit()
	}()
	go s.detectInitialLive()
	go s.autoRefresh()
	go s.monitorClaudeClose()
	go s.monitorUpdates()
	if s.activityLogErr != nil {
		logs.Disable()
		go trayWarning("Logging unavailable", ProductName+" could not create its local logs folder.\n\n"+s.activityLogErr.Error())
	}
}

func (s *trayState) handleLogs(item *systray.MenuItem) {
	for range item.ClickedCh {
		if s.activityLog == nil {
			trayWarning("Logging unavailable", "The local logs folder is not available.")
			continue
		}
		if err := exec.Command("explorer.exe", s.activityLog.Directory()).Start(); err != nil {
			s.setStatus("Could not open logs folder: " + err.Error())
			trayWarning("Could not open logs folder", err.Error())
			continue
		}
		s.setStatus("Logs folder opened")
	}
}

func (s *trayState) handleAdd() {
	for range s.add.ClickedCh {
		if s.initializingSnapshot() {
			s.setStatus("Finish initial setup before adding another account")
			continue
		}
		if s.workflowSnapshot() != nil {
			s.setStatus("An operation is already in progress")
			continue
		}
		name, err := s.nextAutomaticProfileName()
		if err != nil {
			s.setStatus("Could not prepare the new account: " + err.Error())
			trayWarning("Could not add account", err.Error())
			continue
		}
		s.add.Disable()
		s.claude.Disable()
		preparation := startAddPreparationOverlay(s.launchPath())
		workflow, err := newAddWorkflow(s.store, s.platform)
		if err == nil {
			lock, lockErr := acquireOperationLock("operation")
			if lockErr != nil {
				err = lockErr
			} else {
				workflow.lock = lock
				err = s.prepareCurrentForNewAccount()
			}
		}
		if err == nil {
			err = workflow.Begin(name)
		}
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), loginWindowTimeout)
			err = waitForClaudeLoginWindow(ctx, s.platform)
			cancel()
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					err = errors.New("claude did not show the sign-in window in time")
				}
				if recoverErr := workflow.Cancel(); recoverErr != nil && !errors.Is(recoverErr, errAddHandled) {
					err = fmt.Errorf("%w; recover previous account: %v", err, recoverErr)
				}
			}
		}
		preparation.Close()
		if err != nil {
			if workflow != nil {
				workflow.finishLock()
			}
			s.setStatus("Error: " + err.Error())
			trayWarning("Could not add account", err.Error())
			s.loadAccounts()
			continue
		}
		s.setWorkflow(workflow)
		s.setStatus("Waiting for sign-in...")
		s.disableAccounts(true)
		go s.autoComplete(workflow)
	}
}

func (s *trayState) handleClaude() {
	for range s.claude.ClickedCh {
		if !s.beginSwitch() {
			s.setStatus("Wait for the current operation to finish")
			continue
		}
		go s.toggleClaudeDesktop()
	}
}

func (s *trayState) toggleClaudeDesktop() {
	lock, err := acquireOperationLock("operation")
	if err != nil {
		s.finishClaudeAction("Could not update Claude Desktop", err)
		return
	}
	running, err := s.platform.IsRunning()
	if err != nil {
		lock.Release()
		s.finishClaudeAction("Could not detect Claude Desktop", err)
		return
	}
	if running {
		s.setStatus("Closing Claude Desktop...")
		overlay := startNativeOverlay("Closing Claude Desktop...", false, s.launchPath())
		err = s.platform.KillApp()
		overlay.Close()
		lock.Release()
		if err != nil {
			s.finishClaudeAction("Could not close Claude Desktop", err)
			return
		}
		s.setClaudeRunning(false)
		s.endSwitch()
		s.saveClosedSession(s.platform)
		s.setStatus("Claude Desktop: Closed")
		return
	}

	s.setStatus("Opening Claude Desktop...")
	overlay := startNativeOverlay("Opening Claude Desktop...", false, s.launchPath())
	openedProfile := ""
	current, _ := s.store.Current()
	if current != "" && s.store.Exists(current) && s.store.Inspect(current).Health == profile.HealthUsable {
		err = switchProfileWith(current, s.store, s.platform, io.Discard)
		if err == nil {
			openedProfile = current
		}
	} else {
		err = s.platform.LaunchApp()
	}
	overlay.Close()
	lock.Release()
	if openedProfile != "" {
		s.setTrustedActiveProfile(openedProfile)
	}
	s.endSwitch()
	if err != nil {
		s.setStatus("Could not open Claude Desktop: " + err.Error())
		trayWarning("Could not open Claude Desktop", err.Error())
		return
	}
	s.setStatus("Claude Desktop: Open")
}

func (s *trayState) finishClaudeAction(title string, err error) {
	s.endSwitch()
	s.setStatus(title + ": " + err.Error())
	trayWarning(title, err.Error())
}

func (s *trayState) handleBackupExport(item *systray.MenuItem, local bool) {
	for range item.ClickedCh {
		path, err := trayFileDialog(false, defaultBackupFilename(time.Now(), !local))
		if err != nil || path == "" {
			continue
		}
		password := ""
		if !local {
			password, err = traySecretPrompt("Backup password", "Enter a password to encrypt all saved accounts")
			if err != nil {
				s.setStatus("Backup cancelled")
				continue
			}
		}
		item.Disable()
		s.exportPassword.Disable()
		s.exportLocal.Disable()
		if local {
			s.setStatus("Protecting backup on this device...")
		} else {
			s.setStatus("Exporting portable backup...")
		}
		go func(path, password string, local bool) {
			lock, lockErr := acquireOperationLock("operation")
			preparation := startBackupPreparationOverlay(s.launchPath())
			if lockErr == nil {
				lockErr = prepareBackupProfiles(s.store, s.platform, s.resolveBackupProfile, io.Discard)
				if lockErr == nil {
					if local {
						lockErr = s.store.ExportLocal(path)
					} else {
						lockErr = s.store.Export(path, password)
					}
				}
				lock.Release()
			}
			preparation.Close()
			item.Enable()
			s.exportPassword.Enable()
			s.exportLocal.Enable()
			if lockErr != nil {
				s.setStatus("Backup export error: " + lockErr.Error())
				if incomplete, checkErr := s.store.IncompleteProfiles(); checkErr == nil && len(incomplete) > 0 {
					trayWarning("Accounts need attention", "Before creating a backup, open and verify these accounts, then switch to another account to update them:\n\n"+strings.Join(incomplete, ", "))
				} else {
					trayWarning("Backup failed", lockErr.Error())
				}
			} else {
				s.loadAccounts()
				s.setStatus("Backup exported successfully")
			}
		}(path, password, local)
	}
}

func (s *trayState) handleBackupImport() {
	for range s.importer.ClickedCh {
		path, err := trayFileDialog(true, "")
		if err != nil || path == "" {
			continue
		}
		protection, err := profile.DetectBackupProtection(path)
		if err != nil {
			s.setStatus("Backup read error: " + err.Error())
			trayWarning("Could not read backup", err.Error())
			continue
		}
		password := ""
		if protection == profile.BackupProtectionPassword {
			password, err = traySecretPrompt("Backup password", "Enter the password to decrypt the accounts")
			if err != nil {
				s.setStatus("Import cancelled")
				continue
			}
		}
		s.importer.Disable()
		s.setStatus("Importing backup...")
		go func(path, password string) {
			lock, lockErr := acquireOperationLock("operation")
			if lockErr == nil {
				profiles, listErr := s.store.List()
				if listErr != nil {
					lockErr = listErr
				} else if len(profiles) > 0 {
					confirmed, confirmErr := nativeTrayConfirm(
						"Replace saved accounts?",
						"Importing this backup will replace all accounts currently saved in Claude Desktop Switcher.\n\nClaude Desktop's open session will not be changed.\n\nContinue?",
					)
					if confirmErr != nil {
						lockErr = confirmErr
					} else if !confirmed {
						lock.Release()
						s.importer.Enable()
						s.setStatus("Import cancelled")
						return
					}
				}
				if lockErr == nil {
					lockErr = s.store.ImportAuto(path, password)
				}
				lock.Release()
			}
			s.importer.Enable()
			if lockErr != nil {
				s.setStatus("Backup import error: " + lockErr.Error())
				trayWarning("Backup import failed", lockErr.Error())
			} else {
				s.loadAccounts()
				s.setStatus("Backup imported; choose an account to activate it")
				if incomplete, checkErr := s.store.IncompleteProfiles(); checkErr == nil && len(incomplete) > 0 {
					trayWarning("Older backup imported", "These accounts must be opened and verified once before creating a new complete backup:\n\n"+strings.Join(incomplete, ", "))
				}
			}
		}(path, password)
	}
}

func (s *trayState) resolveBackupProfile(string) (string, error) {
	return s.nextAutomaticProfileName()
}

func (s *trayState) handleUpdate() {
	for range s.update.ClickedCh {
		if err := openLatestRelease(); err != nil {
			s.setStatus("Update opening error: " + err.Error())
			trayWarning("Could not open update", err.Error())
		}
	}
}

func (s *trayState) autoRefresh() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.refreshAccounts()
	}
}

// Reading the profiles while an operation renames their folders makes
// Windows refuse the rename, so background refreshes wait for it to finish.
func (s *trayState) refreshAccounts() {
	if s.backgroundReadsPaused() {
		return
	}
	s.loadAccounts()
}

func (s *trayState) backgroundReadsPaused() bool {
	return s.workflowSnapshot() != nil || s.switchingSnapshot() || operationLockHeld("operation")
}

func (s *trayState) monitorClaudeClose() {
	p := s.platform
	wasRunning, err := p.IsRunning()
	if err != nil {
		return
	}
	s.setClaudeRunning(wasRunning)
	if wasRunning {
		go s.detectInitialAccountAfterLaunch()
	}
	observer, observesWindow := p.(platform.LoginWindowObserver)
	var observedWorkflow *addWorkflow
	windowWasVisible := false
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	processPoll := 0
	for range ticker.C {
		workflow := s.workflowSnapshot()
		if workflow != observedWorkflow {
			observedWorkflow = workflow
			windowWasVisible = workflow != nil
		}
		if workflow != nil && observesWindow {
			visible, windowErr := observer.LoginWindowVisible()
			if windowErr == nil {
				if visible {
					windowWasVisible = true
				} else if windowWasVisible {
					windowWasVisible = false
					go s.confirmLoginWindowClose(workflow, p, func() bool {
						visible, err := observer.LoginWindowVisible()
						return err != nil || visible
					})
				}
			}
		}
		processPoll++
		if workflow == nil && processPoll%4 != 0 {
			continue
		}
		running, err := p.IsRunning()
		if err != nil {
			continue
		}
		if running != wasRunning {
			s.setClaudeRunning(running)
			if running {
				go s.refreshAccounts()
				go s.detectInitialAccountAfterLaunch()
			}
		}
		if wasRunning && !running {
			if workflow != nil {
				go s.confirmLoginWindowClose(workflow, p, func() bool {
					running, err := p.IsRunning()
					return err != nil || running
				})
			} else {
				s.saveClosedSession(p)
			}
			go s.refreshAccounts()
		}
		wasRunning = running
	}
}

func (s *trayState) confirmLoginWindowClose(workflow *addWorkflow, p platform.Platform, keepWaiting func() bool) {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if s.workflowSnapshot() != workflow || keepWaiting() || !workflow.waiting() || workflow.loginDetected() {
		return
	}
	if !s.beginCloseConfirmation(workflow) {
		return
	}
	defer s.endCloseConfirmation()
	if s.workflowSnapshot() != workflow || keepWaiting() || !workflow.waiting() || workflow.loginDetected() {
		return
	}
	cancelSetup, err := nativeTrayConfirm(
		"Cancel account setup?",
		"Do you want to cancel adding the new account and return to your previous account?",
	)
	if err != nil || !cancelSetup {
		if s.workflowSnapshot() != workflow {
			return
		}
		err = restartLoginWindow(workflow, p)
		if err != nil {
			s.setStatus("Could not reopen Claude: " + err.Error())
			trayWarning("Could not reopen Claude Desktop", err.Error())
			return
		}
		s.setStatus("Waiting for sign-in...")
		return
	}
	s.setStatus("Restoring previous account...")
	err = workflow.Cancel()
	if errors.Is(err, errAddHandled) || s.workflowSnapshot() != workflow {
		return
	}
	s.clearWorkflow()
	s.add.Enable()
	s.disableAccounts(false)
	if err != nil {
		s.setStatus("Account recovery error: " + err.Error())
		trayWarning("Could not restore the previous account", err.Error())
		return
	}
	s.setStatus("Previous account restored")
}

func restartLoginWindow(workflow *addWorkflow, p platform.Platform) error {
	overlay := startLoginReopenOverlay(platformLaunchPath(p))
	defer overlay.Close()
	if err := restartLoginProcess(workflow, p); err != nil {
		return err
	}
	waiter, ok := p.(platform.LoginWindowWaiter)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := waiter.WaitForLoginWindow(ctx); err != nil {
		return fmt.Errorf("claude login window did not reopen: %w", err)
	}
	return nil
}

func (s *trayState) saveClosedSession(p platform.Platform) {
	if s.workflowSnapshot() != nil || s.switchingSnapshot() {
		return
	}
	appData, err := p.AppDataPath()
	if err != nil {
		return
	}
	current, err := s.store.Current()
	if err != nil || current == "" || !s.store.Exists(current) {
		return
	}
	matched, health := s.store.MatchLiveAt(platform.CookiesPath(appData))
	if health != profile.HealthUsable || matched != current {
		return
	}
	emailChanged := s.store.IdentityEmailChangedAt(current, platform.CookiesPath(appData))
	if emailChanged {
		choice, err := trayChoice(
			"Email updated",
			"This appears to be the same account, but the email changed.\n\nUpdate the profile information?",
		)
		if err != nil || choice != trayYes {
			s.setStatus("Session closed; profile was not updated")
			return
		}
	}
	lock, err := acquireOperationLock("operation")
	if err != nil {
		return
	}
	defer lock.Release()
	if s.workflowSnapshot() != nil || s.switchingSnapshot() {
		return
	}
	running, err := p.IsRunning()
	if err != nil || running {
		return
	}
	if err := saveProfileWith(current, s.store, p, io.Discard); err != nil {
		s.setStatus("Could not save closed session: " + err.Error())
		return
	}
	if emailChanged {
		if _, err := s.store.UpdateAccountEmailFromProfile(current); err != nil {
			s.setStatus("Account saved, but its email label could not be updated")
			return
		}
		s.loadAccounts()
	}
}

func (s *trayState) monitorUpdates() {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	<-timer.C
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		release, err := latestGitHubRelease(ctx)
		cancel()
		if err == nil && updateAvailable(Version, release.TagName) {
			s.update.SetTitle("New version available: " + release.TagName)
			s.update.Show()
		}
		ticker := time.NewTimer(6 * time.Hour)
		<-ticker.C
	}
}

func openLatestRelease() error {
	return openGitHubPage(githubReleasePage)
}

func openGitHubPage(url string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

func (s *trayState) handleVersion() {
	for range s.version.ClickedCh {
		if err := openGitHubPage(githubRepoPage); err != nil {
			s.setStatus("Could not open GitHub repository: " + err.Error())
			trayWarning("Could not open GitHub repository", err.Error())
		}
	}
}

func (s *trayState) restorePendingIfPresent() {
	if _, err := loadPendingAdd(); err != nil {
		return
	}
	workflow, err := newPendingAddWorkflow(s.store, s.platform)
	if err != nil {
		s.setStatus("Pending operation requires manual recovery")
		return
	}
	if err := workflow.ResumePendingLogin(); err != nil {
		s.setStatus("Pending account recovery failed: " + err.Error())
		trayWarning("Could not resume account setup", err.Error())
		return
	}
	s.setWorkflow(workflow)
	s.add.Disable()
	s.disableAccounts(true)
	s.setStatus("Waiting for pending account sign-in...")
	go s.autoComplete(workflow)
}

func (s *trayState) autoComplete(workflow *addWorkflow) {
	var err error
	handled := false
	if workflow.resumed {
		existing, lookupErr := workflow.existingAccount()
		if lookupErr != nil {
			err = lookupErr
		} else if existing != "" {
			progress := startAddCompletionOverlay(platformLaunchPath(workflow.platform))
			handled, err = workflow.RecoverPendingDuplicate()
			progress.Close()
		}
	}
	for err == nil && !handled {
		err = workflow.WaitForLogin(context.Background())
		if err != nil {
			break
		}
		err = completeWithSuccessOverlay(workflow)
		if errors.Is(err, errAddLoginNotReady) {
			err = nil
			if s.workflowSnapshot() != workflow {
				return
			}
			s.setStatus("Waiting for Claude to finish saving the new session...")
			continue
		}
		if err == nil {
			break
		}
	}
	if errors.Is(err, errAddHandled) {
		return
	}
	if s.workflowSnapshot() != workflow {
		return
	}
	s.clearWorkflow()
	s.add.Enable()
	s.disableAccounts(false)
	if err != nil {
		var duplicate *profile.DuplicateAccountError
		if errors.As(err, &duplicate) {
			label := s.refreshProfileEmail(duplicate.ExistingName)
			s.setTrustedActiveProfile(duplicate.ExistingName)
			s.setStatus("Account already saved: " + label)
			trayWarning("Account already saved", "This Claude account is already saved as \""+label+"\".\n\nThe saved account was updated and reopened; no duplicate was created.")
			s.loadAccounts()
			return
		}
		s.setStatus("Account add error: " + err.Error())
		trayWarning("Could not add account", err.Error())
		return
	}
	s.setTrustedActiveProfile(workflow.Name())
	label := s.refreshProfileEmail(workflow.Name())
	s.setStatus("Account added: " + label)
	s.loadAccounts()
}

func completeWithSuccessOverlay(workflow *addWorkflow) error {
	iconPath := platformLaunchPath(workflow.platform)
	progress := startAddCompletionOverlay(iconPath)
	err := workflow.Complete()
	progress.Close()
	if err != nil {
		return err
	}
	success := startAddSuccessOverlay(iconPath)
	defer success.Close()
	time.Sleep(1500 * time.Millisecond)
	return nil
}

func (s *trayState) detectInitialLive() {
	defer s.showFirstRunGuideIfPending()
	defer s.finishInitialization()
	if s.workflowSnapshot() != nil {
		return
	}
	detector, installed := s.platform.(platform.InstallationDetector)
	if !installed || !detector.IsInstalled() {
		s.setStatus("Claude Desktop is not installed or was not detected")
		return
	}
	p := s.platform
	appData, err := p.AppDataPath()
	if err != nil {
		return
	}
	live := platform.CookiesPath(appData)
	running, err := p.IsRunning()
	if err != nil {
		s.setStatus("Could not verify Claude Desktop: " + err.Error())
		return
	}
	profiles, err := s.store.List()
	if err != nil {
		s.setStatus("Could not read saved accounts: " + err.Error())
		return
	}
	inspection := profile.InspectCookies(live, time.Now())
	if s.claimInitialSaveOffer(len(profiles), inspection.Health, running) {
		s.saveInitialDetectedAccount(running)
		return
	}
	hasSession := inspection.Health == profile.HealthUsable
	if inspection.Health == profile.HealthUnknown && running {
		_, digestErr := profile.SessionDigest(live)
		hasSession = digestErr == nil
		if digestErr != nil {
			s.setStatus("Claude Desktop detected, but its session could not be verified")
			return
		}
	}
	matched, _ := s.store.MatchLiveAt(live)
	current, _ := s.store.Current()
	if matched != "" {
		if current != matched {
			if err := s.store.SetCurrent(matched); err != nil {
				s.setStatus("Could not register the active account: " + err.Error())
				return
			}
			s.loadAccounts()
		}
		return
	}
	if !hasSession {
		s.setStatus("No signed-in Claude account detected; choose Add account...")
		return
	}
	if current != "" && s.store.Exists(current) && len(profiles) > 0 {
		label := s.profileDisplayName(current)
		choice, choiceErr := trayChoice("Unrecognized session", "Claude has an active session that does not match the saved copy of "+label+". Update that copy with the current session? Yes=update, No=keep it unchanged.")
		if choiceErr != nil || choice != trayYes {
			if choiceErr == nil && choice == trayNo {
				s.setStatus("Unrecognized active session; saved account was not changed")
			}
			return
		}
		lock, lockErr := acquireOperationLock("operation")
		if lockErr != nil {
			s.setStatus("Could not update account: " + lockErr.Error())
			return
		}
		defer lock.Release()
		var output bytes.Buffer
		if err := saveProfileWith(current, s.store, p, io.Writer(&output)); err != nil {
			s.setStatus("Account update error: " + err.Error())
			trayWarning("Could not update account", err.Error())
			return
		}
		s.loadAccounts()
		s.setStatus("Account updated: " + label)
		return
	}
	choice, err := trayChoice("Account detected", "Claude already has a signed-in account that is not saved in the Switcher. Save it now? Yes=save, No=keep it unchanged.")
	if err != nil || choice != trayYes {
		if err == nil && choice == trayNo {
			s.setStatus("Account detected but not saved")
		}
		return
	}
	name, err := s.nextAutomaticProfileName()
	if err != nil {
		s.setStatus("Could not prepare the detected account: " + err.Error())
		trayWarning("Could not save account", err.Error())
		return
	}
	lock, lockErr := acquireOperationLock("operation")
	if lockErr != nil {
		s.setStatus("Could not save detected account: " + lockErr.Error())
		return
	}
	defer lock.Release()
	if err := s.saveDetectedProfile(name); err != nil {
		s.setStatus("Detected account save error: " + err.Error())
		trayWarning("Could not save account", err.Error())
		return
	}
	s.setTrustedActiveProfile(name)
	s.loadAccounts()
	s.setStatus("Account detected and saved: " + s.profileDisplayName(name))
}

func shouldOfferInitialSave(profileCount int, health profile.Health, running bool) bool {
	return profileCount == 0 && (health == profile.HealthUsable || health == profile.HealthUnknown && running)
}

func (s *trayState) claimInitialSaveOffer(profileCount int, health profile.Health, running bool) bool {
	if !shouldOfferInitialSave(profileCount, health, running) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initialSaveOffered || s.workflow != nil || s.switching {
		return false
	}
	s.initialSaveOffered = true
	return true
}

func (s *trayState) detectInitialAccountAfterLaunch() {
	if s.workflowSnapshot() != nil || s.switchingSnapshot() {
		return
	}
	profiles, err := s.store.List()
	if err != nil || len(profiles) != 0 {
		return
	}
	appData, err := s.platform.AppDataPath()
	if err != nil {
		return
	}
	inspection := profile.InspectCookies(platform.CookiesPath(appData), time.Now())
	if !s.claimInitialSaveOffer(len(profiles), inspection.Health, true) {
		return
	}
	s.saveInitialDetectedAccount(true)
}

func (s *trayState) saveInitialDetectedAccount(running bool) {
	saveAccount, err := nativeTrayConfirm(
		"Save this Claude account?",
		"Claude Desktop is already signed in.\n\nSave this account in Claude Desktop Switcher? Claude Desktop will close and reopen once.",
	)
	if err != nil || !saveAccount {
		s.setStatus("Account not saved; choose Add account... when ready")
		return
	}
	name, err := s.nextAutomaticProfileName()
	if err != nil {
		s.setStatus("Could not prepare the initial account: " + err.Error())
		return
	}
	lock, lockErr := acquireOperationLock("operation")
	if lockErr != nil {
		s.setStatus("Could not save initial account: " + lockErr.Error())
		trayWarning("Could not save account", lockErr.Error())
		return
	}
	defer lock.Release()
	preparation := startInitialAccountSaveOverlay(s.launchPath())
	err = s.saveDetectedProfile(name)
	preparation.Close()
	if err != nil {
		s.setStatus("Initial account save error: " + err.Error())
		trayWarning("Could not save account", err.Error())
		return
	}
	if !running {
		if err := s.platform.LaunchApp(); err != nil {
			s.setStatus("Account saved, but Claude Desktop could not open: " + err.Error())
			trayWarning("Account saved", "The account was saved, but Claude Desktop could not open. You can launch it manually.")
			return
		}
	}
	s.setTrustedActiveProfile(name)
	s.loadAccounts()
	s.setStatus("Account detected and saved: " + s.profileDisplayName(name))
}

func (s *trayState) prepareCurrentForNewAccount() error {
	appData, err := s.platform.AppDataPath()
	if err != nil {
		return err
	}
	live := platform.CookiesPath(appData)
	if !profile.HasActiveSessionAt(live) {
		return nil
	}
	matched, _ := s.store.MatchLiveAt(live)
	if matched != "" {
		if current, _ := s.store.Current(); current != matched {
			return s.store.SetCurrent(matched)
		}
		return nil
	}
	if current, _ := s.store.Current(); current != "" && s.store.Exists(current) {
		return nil
	}
	name, err := s.nextAutomaticProfileName()
	if err != nil {
		return err
	}
	if err := s.saveDetectedProfile(name); err != nil {
		return err
	}
	s.loadAccounts()
	return nil
}

func (s *trayState) nextAutomaticProfileName() (string, error) {
	return automaticProfileName(s.store.Exists)
}

func automaticProfileName(exists func(string) bool) (string, error) {
	for {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return "", err
		}
		id[6] = (id[6] & 0x0f) | 0x40
		id[8] = (id[8] & 0x3f) | 0x80
		name := fmt.Sprintf("account-%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
		if !exists(name) {
			return name, nil
		}
	}
}

func (s *trayState) saveDetectedProfile(name string) error {
	if s.store.Exists(name) {
		return errors.New("that profile name already exists")
	}
	var output bytes.Buffer
	if err := saveProfileWith(name, s.store, s.platform, io.Writer(&output)); err != nil {
		return err
	}
	s.refreshProfileEmail(name)
	return nil
}

func (s *trayState) refreshProfileEmail(name string) string {
	email, err := s.store.UpdateAccountEmailFromProfile(name)
	if err == nil {
		return email
	}
	s.startProfileEmailDiscovery(name)
	return s.profileDisplayName(name)
}

func (s *trayState) startProfileEmailDiscovery(name string) {
	s.mu.Lock()
	if s.emailDiscovering == nil {
		s.emailDiscovering = make(map[string]bool)
	}
	if s.emailDiscovering[name] {
		s.mu.Unlock()
		return
	}
	s.emailDiscovering[name] = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.emailDiscovering, name)
			s.mu.Unlock()
		}()
		s.discoverProfileEmail(name)
	}()
}

func (s *trayState) discoverProfileEmail(name string) {
	appData, err := s.platform.AppDataPath()
	if err != nil {
		return
	}
	deadline := time.Now().Add(profileEmailDiscoveryTimeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if s.backgroundReadsPaused() {
			if time.Now().After(deadline) {
				return
			}
			<-ticker.C
			continue
		}
		profiles, err := s.store.List()
		if err != nil {
			return
		}
		for _, meta := range profiles {
			if meta.Name == name && meta.Email != "" {
				return
			}
		}
		matched, err := s.store.FindByAccountIdentityAt(appData)
		if err != nil {
			return
		}
		if matched != "" && matched != name {
			return
		}
		if matched == name {
			if _, err := s.store.UpdateAccountEmailFromLive(name, appData); err == nil {
				s.loadAccounts()
				return
			}
		}
		if time.Now().After(deadline) {
			return
		}
		<-ticker.C
	}
}

func (s *trayState) profileDisplayName(name string) string {
	profiles, err := s.store.List()
	if err == nil {
		for _, meta := range profiles {
			if meta.Name == name {
				return accountLabel(meta)
			}
		}
	}
	return automaticAccountLabel(name)
}

func (s *trayState) cachedProfileDisplayName(name string) string {
	s.mu.Lock()
	label := s.profileLabels[name]
	s.mu.Unlock()
	if label != "" {
		return label
	}
	return automaticAccountLabel(name)
}

func resolveActiveProfile(detected string, health profile.Health, identityVerified bool, current, trusted string, running, busy, detectedExists, trustedExists bool) string {
	if !running || busy {
		return ""
	}
	if health == profile.HealthUsable {
		return detected
	}
	if health == profile.HealthUnknown && identityVerified && detectedExists {
		return detected
	}
	if health == profile.HealthUnknown && trusted != "" && trusted == current && trustedExists {
		return trusted
	}
	return ""
}

func resolveActiveIdentity(detected string, health profile.Health, persisted bool, identity string, identityErr error) (string, profile.Health) {
	if detected != "" || health != profile.HealthUnknown || !persisted || identityErr != nil {
		return detected, health
	}
	return identity, profile.HealthUnknown
}

func (s *trayState) loadAccounts() {
	profiles, err := s.store.List()
	if err != nil {
		s.setStatus("Account read error: " + err.Error())
		return
	}
	for index := range profiles {
		if (profiles[index].Email != "" || profiles[index].EmailLookupDone) && profiles[index].AccountUUIDHash != "" {
			continue
		}
		if updated, updateErr := s.store.BackfillAccountMetadata(profiles[index].Name); updateErr == nil {
			profiles[index] = updated
		}
	}
	installed := false
	if detector, ok := s.platform.(platform.InstallationDetector); ok {
		installed = detector.IsInstalled()
	}
	running := false
	if installed {
		running, _ = s.platform.IsRunning()
	}
	s.mu.Lock()
	busy := s.switching || s.workflow != nil || s.initializing
	trusted := s.trustedActive
	s.mu.Unlock()
	current, _ := s.store.Current()
	detected := ""
	liveHealth := profile.HealthUnknown
	identityVerified := false
	if installed && running && !busy {
		if appData, appDataErr := s.platform.AppDataPath(); appDataErr == nil {
			detected, liveHealth = s.store.MatchLiveAt(platform.CookiesPath(appData))
			persisted := profile.HasPersistedAccountStateAt(appData)
			if detected == "" && persisted {
				identityDetected, identityErr := s.store.FindByAccountIdentityAt(appData)
				detected, liveHealth = resolveActiveIdentity(detected, liveHealth, persisted, identityDetected, identityErr)
				identityVerified = identityErr == nil && identityDetected != ""
			}
		}
	}
	detectedExists := detected != "" && s.store.Exists(detected)
	trustedExists := trusted != "" && s.store.Exists(trusted)
	active := resolveActiveProfile(detected, liveHealth, identityVerified, current, trusted, running, busy, detectedExists, trustedExists)
	if active != "" && (liveHealth == profile.HealthUsable || identityVerified) && current != active {
		_ = s.store.SetCurrent(active)
	}
	if active != "" {
		for _, meta := range profiles {
			if meta.Name == active && meta.Email == "" {
				s.startProfileEmailDiscovery(active)
				break
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	busy = s.switching || s.workflow != nil || s.initializing
	s.claudeInstalled = installed
	s.claudeRunning = running
	s.activeProfile = active
	if !running {
		s.trustedActive = ""
	} else if !busy {
		switch {
		case active != "":
			s.trustedActive = active
		case liveHealth != profile.HealthUnknown || !trustedExists:
			s.trustedActive = ""
		}
	}
	s.updateClaudeMenuLocked(busy)
	if installed && !busy {
		s.add.Enable()
	} else {
		s.add.Disable()
	}
	if len(profiles) == 0 {
		s.root.Hide()
		retireMenuItem(s.delete)
	} else {
		s.root.Show()
		s.delete.Show()
		if busy {
			s.delete.Disable()
		} else {
			s.delete.Enable()
		}
	}
	seen := make(map[string]bool, len(profiles))
	for _, meta := range profiles {
		seen[meta.Name] = true
		label := accountLabel(meta)
		if s.profileLabels == nil {
			s.profileLabels = make(map[string]string)
		}
		s.profileLabels[meta.Name] = label
		item, ok := s.items[meta.Name]
		if !ok {
			name := meta.Name
			item = s.root.AddSubMenuItem(label, "Switch to "+label)
			s.items[name] = item
			stop := make(chan struct{})
			s.itemStops[name] = stop
			go s.watchAccount(item, name, stop)
		}
		deleteItem, deleteOK := s.deleteItems[meta.Name]
		if !deleteOK {
			deleteItem = s.delete.AddSubMenuItem(label, "Delete "+label)
			s.deleteItems[meta.Name] = deleteItem
			stop := make(chan struct{})
			s.deleteItemStops[meta.Name] = stop
			go s.watchDeleteAccount(deleteItem, meta.Name, stop)
		}
		isActive := running && active == meta.Name
		if isActive {
			item.SetTitle("✓ " + label + " (in use)")
		} else {
			item.SetTitle(label)
		}
		if installed && !busy && !isActive {
			item.Enable()
		} else {
			item.Disable()
		}
		item.Show()
		if isActive {
			deleteItem.SetTitle("✓ " + label + " (in use)")
		} else {
			deleteItem.SetTitle(label)
		}
		deleteItem.Show()
		if busy || isActive {
			deleteItem.Disable()
		} else {
			deleteItem.Enable()
		}
	}
	for name, item := range s.items {
		if !seen[name] {
			retireMenuItem(item)
			stopMenuWatcher(s.itemStops, name)
			delete(s.items, name)
			delete(s.profileLabels, name)
		}
	}
	for name, item := range s.deleteItems {
		if !seen[name] {
			retireMenuItem(item)
			stopMenuWatcher(s.deleteItemStops, name)
			delete(s.deleteItems, name)
			delete(s.profileLabels, name)
		}
	}
	if !installed {
		s.setStatus("Claude Desktop is not installed or was not detected")
	}
}

type hideableMenuItem interface {
	Disable()
	Hide()
}

func retireMenuItem(item hideableMenuItem) {
	item.Disable()
	item.Hide()
}

func stopMenuWatcher(stops map[string]chan struct{}, name string) {
	if stop, ok := stops[name]; ok {
		close(stop)
		delete(stops, name)
	}
}

func (s *trayState) updateClaudeMenuLocked(busy bool) {
	if s.claude == nil {
		return
	}
	title, enabled := claudeMenuState(s.claudeInstalled, s.claudeRunning, busy)
	s.claude.SetTitle(title)
	if enabled {
		s.claude.Enable()
	} else {
		s.claude.Disable()
	}
}

func claudeMenuState(installed, running, busy bool) (string, bool) {
	if !installed {
		return "Claude Desktop: Not installed", false
	}
	if running {
		return "Close Claude Desktop", !busy
	}
	return "Open Claude Desktop", !busy
}

func (s *trayState) setClaudeRunning(running bool) {
	s.mu.Lock()
	s.claudeRunning = running
	if !running {
		s.activeProfile = ""
		s.trustedActive = ""
	}
	busy := s.switching || s.workflow != nil || s.initializing
	s.updateClaudeMenuLocked(busy)
	s.mu.Unlock()
	if !busy {
		if running {
			s.setStatus("Claude Desktop: Open")
		} else {
			s.setStatus("Claude Desktop: Closed")
		}
	}
}

func (s *trayState) setTrustedActiveProfile(name string) {
	s.mu.Lock()
	s.trustedActive = name
	s.mu.Unlock()
}

func platformLaunchPath(p platform.Platform) string {
	if provider, ok := p.(interface{ LaunchPath() string }); ok {
		return provider.LaunchPath()
	}
	return ""
}

func (s *trayState) launchPath() string {
	return platformLaunchPath(s.platform)
}

func (s *trayState) watchAccount(item *systray.MenuItem, name string, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case _, ok := <-item.ClickedCh:
			if !ok {
				return
			}
		}
		if s.activeAccountSnapshot(name) {
			trayWarning(ProductName, "This account is currently in use. Choose another account or add a new one.")
			continue
		}
		label := s.cachedProfileDisplayName(name)
		if !s.beginSwitch() {
			s.setStatus("Wait for automatic account setup to finish")
			continue
		}
		s.setStatus("Switching to " + label + "...")
		go func() {
			err := switchProfileFromTray(name, s.store, s.platform, s.launchPath())
			if err == nil {
				s.setTrustedActiveProfile(name)
			}
			s.endSwitch()
			if err != nil {
				if errors.Is(err, errLiveSessionUnrecognized) || errors.Is(err, errLiveSessionUnverified) {
					s.setStatus("Switch skipped: the open Claude account could not be identified; no profiles were changed")
					return
				}
				s.setStatus("Account switch error: " + err.Error())
				trayWarning("Could not switch account", err.Error())
				return
			}
			s.setStatus("Active account: " + label)
		}()
	}
}

func switchProfileFromTray(name string, store *profile.Store, p platform.Platform, iconPath string) error {
	lock, err := acquireOperationLock("operation")
	if err != nil {
		return err
	}
	defer lock.Release()
	overlay := startSwitchOverlay(iconPath)
	defer overlay.Close()
	err = switchProfileWith(name, store, p, io.Discard)
	if err == nil {
		if pending, pendingErr := loadPendingAdd(); pendingErr == nil && pending.Previous == name {
			_ = clearPendingAdd()
		}
	}
	return err
}

func (s *trayState) watchDeleteAccount(item *systray.MenuItem, name string, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case _, ok := <-item.ClickedCh:
			if !ok {
				return
			}
		}
		label := s.cachedProfileDisplayName(name)
		if s.initializingSnapshot() {
			s.setStatus("Finish initial setup before deleting an account")
			continue
		}
		if s.switchingSnapshot() {
			s.setStatus("Wait for the account switch to finish")
			continue
		}
		if s.workflowSnapshot() != nil {
			s.setStatus("Wait for automatic account setup to finish")
			continue
		}
		if s.activeAccountSnapshot(name) {
			trayWarning(ProductName, "Close Claude Desktop first to delete this account.")
			continue
		}
		confirmed, err := trayDeleteConfirm(label)
		if err != nil || !confirmed {
			continue
		}
		item.Disable()
		s.delete.Disable()
		s.setStatus("Checking account: " + label)
		go func() {
			lock, lockErr := acquireOperationLock("operation")
			if lockErr == nil {
				active, _, running, activeErr := s.deleteAccountIsActive(name)
				if activeErr != nil {
					lock.Release()
					message := s.deleteVerificationMessage(name, activeErr)
					trayWarning("Unable to verify active account", message)
					s.loadAccounts()
					s.setStatus(label + " was not deleted: active account could not be verified")
					return
				}
				if active {
					lock.Release()
					trayWarning(ProductName, "Close Claude Desktop first to delete this account.")
					s.loadAccounts()
					s.setStatus(label + " was not deleted: it is active")
					return
				}
				s.setStatus("Deleting account: " + label)
				if !running {
					appData, appDataErr := s.platform.AppDataPath()
					if appDataErr != nil {
						lockErr = appDataErr
					} else {
						lockErr = deleteClosedAccount(name, s.store, appData, platform.CookiesPath(appData))
					}
				} else {
					lockErr = s.store.Delete(name)
				}
				lock.Release()
			}
			if lockErr != nil {
				s.loadAccounts()
				s.setStatus("Account deletion error: " + lockErr.Error())
				trayWarning("Could not delete account", lockErr.Error())
				return
			}
			s.removeDeletedAccount(name)
			s.loadAccounts()
			s.setStatus("Account deleted: " + label)
		}()
	}
}

type closedAccountDeletionStore interface {
	Current() (string, error)
	List() ([]profile.Meta, error)
	Delete(string) error
	RestoreAt(string, string, string) error
	WipeAt(string, string) error
}

func deleteClosedAccount(name string, store closedAccountDeletionStore, appData, live string) error {
	current, err := store.Current()
	if errors.Is(err, os.ErrNotExist) {
		current = ""
	} else if err != nil {
		return fmt.Errorf("read closed Claude account: %w", err)
	}
	if current != name {
		return store.Delete(name)
	}

	profiles, err := store.List()
	if err != nil {
		return fmt.Errorf("read remaining accounts: %w", err)
	}
	fallback, remaining := closedDeletionFallback(profiles, name)
	if remaining && fallback == "" {
		return errors.New("no remaining account has a usable saved session")
	}
	if fallback == "" {
		if err := store.WipeAt(appData, live); err != nil {
			return fmt.Errorf("clear the last account from Claude Desktop: %w", err)
		}
		if err := store.Delete(name); err != nil {
			if restoreErr := store.RestoreAt(name, appData, live); restoreErr != nil {
				return fmt.Errorf("delete account: %w; restore its Claude session: %v", err, restoreErr)
			}
			return err
		}
		return nil
	}

	if err := store.RestoreAt(fallback, appData, live); err != nil {
		return fmt.Errorf("prepare remaining account %q: %w", fallback, err)
	}
	if err := store.Delete(name); err != nil {
		if restoreErr := store.RestoreAt(name, appData, live); restoreErr != nil {
			return fmt.Errorf("delete account: %w; restore previous Claude session: %v", err, restoreErr)
		}
		return err
	}
	return nil
}

func closedDeletionFallback(profiles []profile.Meta, deleting string) (string, bool) {
	var selected profile.Meta
	remaining := false
	for _, candidate := range profiles {
		if candidate.Name == deleting {
			continue
		}
		remaining = true
		if candidate.ObservedHealth != profile.HealthUsable {
			continue
		}
		if selected.Name == "" || profileActivityTime(candidate).After(profileActivityTime(selected)) ||
			profileActivityTime(candidate).Equal(profileActivityTime(selected)) && candidate.Name < selected.Name {
			selected = candidate
		}
	}
	return selected.Name, remaining
}

func profileActivityTime(meta profile.Meta) time.Time {
	if !meta.LastUsed.IsZero() {
		return meta.LastUsed
	}
	if !meta.SavedAt.IsZero() {
		return meta.SavedAt
	}
	return meta.CreatedAt
}

func (s *trayState) removeDeletedAccount(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item, ok := s.items[name]; ok {
		retireMenuItem(item)
		stopMenuWatcher(s.itemStops, name)
		delete(s.items, name)
	}
	if item, ok := s.deleteItems[name]; ok {
		retireMenuItem(item)
		stopMenuWatcher(s.deleteItemStops, name)
		delete(s.deleteItems, name)
	}
	delete(s.profileLabels, name)
}

func (s *trayState) deleteAccountIsActive(name string) (bool, bool, bool, error) {
	p := s.platform
	running, err := p.IsRunning()
	if err != nil {
		return false, false, false, fmt.Errorf("claude could not be verified: %w", err)
	}
	if !running {
		return false, false, false, nil
	}
	appData, err := p.AppDataPath()
	if err != nil {
		return false, false, true, fmt.Errorf("claude account data could not be located: %w", err)
	}
	liveName, liveHealth := s.store.MatchLiveAt(platform.CookiesPath(appData))
	if liveName == "" && liveHealth != profile.HealthMissing {
		identityName, identityErr := s.store.FindByAccountIdentityAt(appData)
		if identityErr != nil {
			return false, false, true, fmt.Errorf("claude account identity could not be verified: %w", identityErr)
		}
		if identityName != "" {
			return identityName == name, true, true, nil
		}
	}
	isActive, verified, err := resolveDeleteActivity(name, liveName, liveHealth)
	return isActive, verified, true, err
}

func resolveDeleteActivity(name, liveName string, liveHealth profile.Health) (bool, bool, error) {
	switch liveHealth {
	case profile.HealthMissing:
		return false, false, nil
	case profile.HealthUsable:
		if liveName != "" {
			return liveName == name, true, nil
		}
		return false, false, errDeleteSessionUnrecognized
	case profile.HealthExpired, profile.HealthUnknown:
		return false, false, errDeleteSessionUnknown
	default:
		return false, false, errDeleteSessionUnknown
	}
}

func (s *trayState) deleteVerificationMessage(_ string, err error) string {
	if errors.Is(err, errDeleteSessionUnknown) {
		return "Could not verify Claude's active account.\n\nClose Claude Desktop and try again."
	}
	if errors.Is(err, errDeleteSessionUnrecognized) {
		return "Claude is using an unsaved session.\n\nSave it or close Claude Desktop before deleting accounts."
	}
	return "Could not verify the active account.\n\nClose Claude Desktop and try again."
}

func (s *trayState) setWorkflow(workflow *addWorkflow) {
	s.mu.Lock()
	s.workflow = workflow
	s.mu.Unlock()
}

func (s *trayState) beginSwitch() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.switching || s.workflow != nil || s.initializing {
		return false
	}
	s.switching = true
	for _, item := range s.items {
		item.Disable()
	}
	s.add.Disable()
	s.delete.Disable()
	s.claude.Disable()
	return true
}

func (s *trayState) endSwitch() {
	s.mu.Lock()
	s.switching = false
	s.mu.Unlock()
	s.loadAccounts()
}

func (s *trayState) switchingSnapshot() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.switching
}

func (s *trayState) activeAccountSnapshot(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claudeRunning && s.activeProfile == name
}

func (s *trayState) initializingSnapshot() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initializing
}

func (s *trayState) finishInitialization() {
	s.mu.Lock()
	s.initializing = false
	s.mu.Unlock()
	s.loadAccounts()
}

func (s *trayState) clearWorkflow() {
	s.mu.Lock()
	s.workflow = nil
	s.mu.Unlock()
}

func (s *trayState) beginCloseConfirmation(workflow *addWorkflow) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workflow != workflow || s.confirmingClose {
		return false
	}
	s.confirmingClose = true
	return true
}

func (s *trayState) endCloseConfirmation() {
	s.mu.Lock()
	s.confirmingClose = false
	s.mu.Unlock()
}

func (s *trayState) workflowSnapshot() *addWorkflow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workflow
}

func (s *trayState) disableAccounts(disabled bool) {
	if !disabled {
		s.loadAccounts()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		item.Disable()
	}
}

func (s *trayState) setStatus(value string) {
	if s.activityLog != nil {
		_ = s.activityLog.Write(value)
	}
}

type trayChoiceValue string

const (
	trayYes    trayChoiceValue = "Yes"
	trayNo     trayChoiceValue = "No"
	trayCancel trayChoiceValue = "Cancel"
)

func trayChoice(title, message string) (trayChoiceValue, error) {
	return nativeTrayChoice(title, message)
}

func trayFileDialog(open bool, defaultName string) (string, error) {
	return nativeTrayFileDialog(open, defaultName)
}

func traySecretPrompt(title, message string) (string, error) {
	return nativeTraySecretPrompt(title, message)
}
