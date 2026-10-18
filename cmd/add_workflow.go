package cmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TinoA/claude-desktop-switcher/internal/platform"
	"github.com/TinoA/claude-desktop-switcher/internal/profile"
)

type addStage string

const (
	addIdle         addStage = "idle"
	addWaitingLogin addStage = "waiting_for_login"
	addCompleting   addStage = "completing"
	addCompleted    addStage = "completed"
	addCancelled    addStage = "cancelled"
)

var errAddCancelled = errors.New("account add cancelled")
var errAddHandled = errors.New("account-add workflow was already handled")
var errAddLoginNotReady = errors.New("new Claude session is not ready yet")

const maxPersistenceRetries = 1

type addStore interface {
	Exists(string) bool
	Current() (string, error)
	SetCurrent(string) error
	FindByAccountIdentityAt(string) (string, error)
	CheckpointAt(string, string, string) error
	WipeAt(string, string) error
	RestoreAt(string, string, string) error
}

type addWorkflow struct {
	store              addStore
	platform           platform.Platform
	appData            string
	live               string
	name               string
	previous           string
	stage              addStage
	resumed            bool
	lock               *operationLock
	stopped            bool
	sessionUsable      func(string) bool
	loginEvidence      func(string, string) bool
	accountLogState    func(string, int64, time.Time) profile.AccountLogState
	accountStateReady  func(string) bool
	accountStateID     func(string) string
	persistPending     func(pendingAdd) error
	removePending      func() error
	diagnostic         func(string, string, string)
	loginPoll          time.Duration
	loginSettle        time.Duration
	sessionPoll        time.Duration
	sessionTimeout     time.Duration
	createdAt          time.Time
	loginLogOffset     int64
	persistenceRetries int
	mu                 sync.Mutex
}

func newAddWorkflow(store addStore, p platform.Platform) (*addWorkflow, error) {
	appData, err := p.AppDataPath()
	if err != nil {
		return nil, err
	}
	return &addWorkflow{
		store:             store,
		platform:          p,
		appData:           appData,
		live:              platform.CookiesPath(appData),
		stage:             addIdle,
		sessionUsable:     profile.HasActiveSessionAt,
		loginEvidence:     profile.HasLoginEvidenceAt,
		accountLogState:   profile.LatestAccountLogStateSince,
		accountStateReady: profile.HasPersistedAccountStateAt,
		accountStateID:    profile.PersistedAccountStateFingerprintAt,
		persistPending:    writePendingAdd,
		removePending:     clearPendingAdd,
		diagnostic:        writeAddDiagnostic,
		loginPoll:         time.Second,
		loginSettle:       3 * time.Second,
		sessionPoll:       250 * time.Millisecond,
		sessionTimeout:    2 * time.Second,
	}, nil
}

func (w *addWorkflow) Begin(name string) error {
	if w.stage != addIdle {
		return fmt.Errorf("account-add workflow is already %s", w.stage)
	}
	if !validAddProfileName(name) {
		w.finishLock()
		return errors.New("profile name is invalid")
	}
	if w.store.Exists(name) {
		w.finishLock()
		return fmt.Errorf("profile %q already exists", name)
	}
	if w.lock == nil {
		lock, err := acquireOperationLock("operation")
		if err != nil {
			return err
		}
		w.lock = lock
	}
	w.name = name
	w.previous, _ = w.store.Current()
	if w.previous != "" && !w.store.Exists(w.previous) {
		w.previous = ""
	}
	if w.previous == "" && w.sessionUsable(w.live) {
		w.finishLock()
		return errors.New("the active session has no tracked profile; save it before adding another account")
	}
	w.createdAt = time.Now()
	w.loginLogOffset = profile.LoginLogOffsetAt(w.appData)
	if err := w.persistPending(w.pendingState()); err != nil {
		w.finishLock()
		return err
	}
	if running, err := w.platform.IsRunning(); err != nil {
		return w.failBeforeMutation(err)
	} else if running {
		if err := w.stopClaude(); err != nil {
			return w.failBeforeMutation(err)
		}
		w.stopped = true
	}
	if w.previous != "" && w.sessionUsable(w.live) {
		if err := w.store.CheckpointAt(w.previous, w.appData, w.live); err != nil {
			return w.failBeforeMutation(fmt.Errorf("checkpoint current profile: %w", err))
		}
	}
	if err := w.store.WipeAt(w.appData, w.live); err != nil {
		return w.recover(fmt.Errorf("clear session state: %w", err))
	}
	if err := w.platform.LaunchApp(); err != nil {
		return w.recover(fmt.Errorf("launch Claude for login: %w", err))
	}
	w.mu.Lock()
	w.stage = addWaitingLogin
	w.mu.Unlock()
	w.record("waiting", "Claude opened for sign-in")
	return nil
}

func validAddProfileName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.HasPrefix(name, ".") && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}

func (w *addWorkflow) Complete() error {
	if err := w.claim(addWaitingLogin, addCompleting); err != nil {
		return err
	}
	if !w.loginDetected() {
		w.resetToWaiting()
		return errAddLoginNotReady
	}
	existing, err := w.existingAccount()
	if err != nil {
		return w.recover(fmt.Errorf("check existing accounts: %w", err))
	}
	target := w.name
	duplicateName := ""
	if existing != "" && existing != w.name {
		target = existing
		duplicateName = existing
	}
	if err := w.stopClaude(); err != nil {
		return w.recover(fmt.Errorf("stop Claude after login: %w", err))
	}
	if !w.waitForUsableSession() {
		if w.persistenceRetries >= maxPersistenceRetries {
			return w.recover(errors.New("the new Claude session did not remain usable after Claude closed"))
		}
		w.persistenceRetries++
		if err := w.persistPending(w.pendingState()); err != nil {
			return w.recover(fmt.Errorf("record account persistence retry: %w", err))
		}
		if err := w.platform.LaunchApp(); err != nil {
			return w.recover(fmt.Errorf("claude did not finish persisting the new session and could not reopen: %w", err))
		}
		w.resetToWaiting()
		w.record("waiting", "Claude reopened to finish persisting the new session")
		return errAddLoginNotReady
	}
	if err := w.store.CheckpointAt(target, w.appData, w.live); err != nil {
		var duplicate *profile.DuplicateAccountError
		if errors.As(err, &duplicate) {
			if refreshErr := w.store.CheckpointAt(duplicate.ExistingName, w.appData, w.live); refreshErr != nil {
				return w.recover(fmt.Errorf("%w; update saved account: %v", err, refreshErr))
			}
			target = duplicate.ExistingName
			duplicateName = duplicate.ExistingName
		} else {
			return w.recover(fmt.Errorf("save new profile: %w", err))
		}
	}
	if err := w.store.RestoreAt(target, w.appData, w.live); err != nil {
		return w.recover(fmt.Errorf("activate new profile: %w", err))
	}
	if err := w.platform.LaunchApp(); err != nil {
		w.setStage(addCompleted)
		w.finishLock()
		_ = w.removePending()
		w.record("saved", "Profile saved; Claude restart failed")
		return fmt.Errorf("profile saved but Claude could not restart: %w", err)
	}
	if duplicateName != "" {
		return w.finishExistingAccount(duplicateName)
	}
	w.setStage(addCompleted)
	w.finishLock()
	if err := w.removePending(); err != nil {
		w.record("saved", "Profile saved; pending marker cleanup failed")
		return err
	}
	w.record("completed", "Profile saved and Claude restarted")
	return nil
}

func (w *addWorkflow) finishExistingAccount(name string) error {
	if err := w.store.SetCurrent(name); err != nil {
		return w.recover(fmt.Errorf("select existing account: %w", err))
	}
	w.setStage(addCompleted)
	w.finishLock()
	if err := w.removePending(); err != nil {
		return err
	}
	w.record("duplicate", "Existing profile updated and Claude restarted")
	return &profile.DuplicateAccountError{ExistingName: name}
}

func (w *addWorkflow) Cancel() error {
	if err := w.claim(addWaitingLogin, addCancelled); err != nil {
		return err
	}
	return w.recover(errAddCancelled)
}

func (w *addWorkflow) RecoverPendingDuplicate() (bool, error) {
	if !w.resumed {
		return false, nil
	}
	existing, err := w.existingAccount()
	if err != nil || existing == "" {
		return false, err
	}
	if w.loginDetected() {
		return true, w.Complete()
	}
	if err := w.claim(addWaitingLogin, addCompleting); err != nil {
		return true, err
	}
	if running, err := w.platform.IsRunning(); err != nil {
		return true, w.recover(err)
	} else if running {
		if err := w.stopClaude(); err != nil {
			return true, w.recover(fmt.Errorf("stop incomplete Claude login: %w", err))
		}
	}
	if err := w.store.RestoreAt(existing, w.appData, w.live); err != nil {
		return true, w.recover(fmt.Errorf("restore existing account: %w", err))
	}
	if err := w.platform.LaunchApp(); err != nil {
		return true, w.recover(fmt.Errorf("reopen existing account: %w", err))
	}
	return true, w.finishExistingAccount(existing)
}

func restartLoginProcess(workflow *addWorkflow, p platform.Platform) error {
	running, err := p.IsRunning()
	if err != nil {
		return err
	}
	if running {
		if err := p.KillApp(); err != nil {
			return err
		}
	}
	if err := workflow.store.WipeAt(workflow.appData, workflow.live); err != nil {
		return fmt.Errorf("clear incomplete login: %w", err)
	}
	if err := p.LaunchApp(); err != nil {
		return err
	}
	return nil
}

func (w *addWorkflow) ResumePendingLogin() error {
	if !w.resumed || !w.waiting() {
		return errors.New("account-add workflow is not waiting to resume")
	}
	running, err := w.platform.IsRunning()
	if err != nil {
		return w.recover(fmt.Errorf("verify Claude before resuming account login: %w", err))
	}
	if running {
		return nil
	}
	w.createdAt = time.Now()
	w.loginLogOffset = profile.LoginLogOffsetAt(w.appData)
	if err := w.persistPending(w.pendingState()); err != nil {
		return w.recover(fmt.Errorf("reset pending login detection: %w", err))
	}
	if err := w.platform.LaunchApp(); err != nil {
		return w.recover(fmt.Errorf("reopen Claude to resume account login: %w", err))
	}
	w.record("waiting", "Claude reopened to resume pending sign-in")
	return nil
}

func (w *addWorkflow) existingAccount() (string, error) {
	return w.store.FindByAccountIdentityAt(w.appData)
}

// WaitAndComplete watches the live Cookies database and completes as soon as
// Claude has established a usable session. The context lets Ctrl+C or tray
// shutdown recover the previous account safely.
func (w *addWorkflow) WaitAndComplete(ctx context.Context) error {
	for {
		if err := w.WaitForLogin(ctx); err != nil {
			return err
		}
		err := w.Complete()
		if errors.Is(err, errAddLoginNotReady) {
			continue
		}
		return err
	}
}

// WaitForLogin waits for stable local account evidence before Claude is
// stopped and its locked Cookies database can be validated.
func (w *addWorkflow) WaitForLogin(ctx context.Context) error {
	interval := w.loginPoll
	if interval <= 0 {
		interval = time.Second
	}
	var readySince time.Time
	var readyState string
	for {
		if !w.waiting() {
			return errAddHandled
		}
		state := w.loginState()
		if state != "" {
			if readySince.IsZero() || state != readyState {
				readySince = time.Now()
				readyState = state
			}
			if time.Since(readySince) >= w.loginSettle {
				return nil
			}
		} else {
			readySince = time.Time{}
			readyState = ""
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			_ = w.Cancel()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (w *addWorkflow) loginReady() bool {
	return w.sessionUsable != nil && w.sessionUsable(w.live)
}

func (w *addWorkflow) loginDetected() bool {
	if w.accountStateReady == nil || !w.accountStateReady(w.appData) {
		return false
	}
	if w.loginReady() {
		return true
	}
	if w.accountLogState != nil {
		switch w.accountLogState(w.appData, w.loginLogOffset, w.createdAt) {
		case profile.AccountLogSignedIn:
			return true
		case profile.AccountLogSignedOut:
			return false
		}
	}
	return w.loginEvidence != nil && w.loginEvidence(w.appData, w.live)
}

func (w *addWorkflow) loginState() string {
	if !w.loginDetected() || w.accountStateID == nil {
		return ""
	}
	return w.accountStateID(w.appData)
}

func (w *addWorkflow) persistedSessionReady() bool {
	return w.loginReady() && w.accountStateReady != nil && w.accountStateReady(w.appData)
}

func (w *addWorkflow) resetToWaiting() {
	w.mu.Lock()
	if w.stage == addCompleting {
		w.stage = addWaitingLogin
	}
	w.mu.Unlock()
}

func (w *addWorkflow) waitForUsableSession() bool {
	poll := w.sessionPoll
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	timeout := w.sessionTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if w.persistedSessionReady() {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		time.Sleep(min(poll, remaining))
	}
}

func (w *addWorkflow) pendingState() pendingAdd {
	return pendingAdd{
		Name:               w.name,
		Previous:           w.previous,
		AppData:            w.appData,
		Live:               w.live,
		CreatedAt:          w.createdAt,
		LoginLogOffset:     w.loginLogOffset,
		PersistenceRetries: w.persistenceRetries,
	}
}

func (w *addWorkflow) Name() string { return w.name }

func (w *addWorkflow) waiting() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stage == addWaitingLogin
}

func (w *addWorkflow) claim(expected, next addStage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stage != expected {
		if w.stage == addCompleting || w.stage == addCancelled || w.stage == addCompleted {
			return errAddHandled
		}
		return fmt.Errorf("account-add workflow is %s, not waiting for login", w.stage)
	}
	w.stage = next
	return nil
}

func (w *addWorkflow) setStage(stage addStage) {
	w.mu.Lock()
	w.stage = stage
	w.mu.Unlock()
}

func (w *addWorkflow) stopClaude() error {
	err := w.platform.KillApp()
	if err == nil {
		return nil
	}
	running, stateErr := w.platform.IsRunning()
	if stateErr != nil {
		return fmt.Errorf("%w; verify Claude state: %v", err, stateErr)
	}
	if running {
		return err
	}
	return nil
}

func (w *addWorkflow) record(state, detail string) {
	if w.diagnostic != nil {
		w.diagnostic(w.name, state, detail)
	}
}

func (w *addWorkflow) failBeforeMutation(err error) error {
	if w.stopped {
		return w.recover(err)
	}
	if running, stateErr := w.platform.IsRunning(); stateErr == nil && !running {
		if launchErr := w.platform.LaunchApp(); launchErr != nil {
			err = fmt.Errorf("%w; previous Claude session could not reopen: %v", err, launchErr)
		}
	}
	w.mu.Lock()
	w.stage = addCancelled
	w.mu.Unlock()
	w.finishLock()
	_ = w.removePending()
	return err
}

//nolint:unused // used by the Windows tray build.
func newPendingAddWorkflow(store addStore, p platform.Platform) (*addWorkflow, error) {
	pending, err := loadPendingAdd()
	if err != nil {
		return nil, err
	}
	if pending.Name == "" {
		return nil, errors.New("pending account-add state is incomplete")
	}
	appData, err := p.AppDataPath()
	if err != nil {
		return nil, err
	}
	lock, err := acquireOperationLock("operation")
	if err != nil {
		return nil, err
	}
	return &addWorkflow{
		store:              store,
		platform:           p,
		appData:            appData,
		live:               platform.CookiesPath(appData),
		name:               pending.Name,
		previous:           pending.Previous,
		stage:              addWaitingLogin,
		resumed:            true,
		lock:               lock,
		sessionUsable:      profile.HasActiveSessionAt,
		loginEvidence:      profile.HasLoginEvidenceAt,
		accountLogState:    profile.LatestAccountLogStateSince,
		accountStateReady:  profile.HasPersistedAccountStateAt,
		accountStateID:     profile.PersistedAccountStateFingerprintAt,
		persistPending:     writePendingAdd,
		removePending:      clearPendingAdd,
		diagnostic:         writeAddDiagnostic,
		loginPoll:          time.Second,
		loginSettle:        3 * time.Second,
		sessionPoll:        250 * time.Millisecond,
		sessionTimeout:     2 * time.Second,
		createdAt:          pending.CreatedAt,
		loginLogOffset:     pending.LoginLogOffset,
		persistenceRetries: pending.PersistenceRetries,
	}, nil
}

func (w *addWorkflow) recover(cause error) error {
	// Cancel resumability before recovery can leave partially restored state.
	if err := w.removePending(); err != nil {
		w.setStage(addCancelled)
		w.finishLock()
		w.record("recovery-failed", err.Error())
		return errors.Join(cause, profile.ErrRecoveryRequired, fmt.Errorf("remove pending login before recovery: %w", err))
	}
	if errors.Is(cause, profile.ErrRecoveryRequired) {
		w.setStage(addCancelled)
		w.finishLock()
		w.record("recovery-failed", cause.Error())
		return cause
	}
	running, stateErr := w.platform.IsRunning()
	if stateErr != nil {
		w.setStage(addCancelled)
		w.finishLock()
		w.record("recovery-failed", stateErr.Error())
		return fmt.Errorf("%w; recovery could not verify whether Claude is closed: %v", cause, stateErr)
	}
	if running {
		if stopErr := w.stopClaude(); stopErr != nil {
			w.setStage(addCancelled)
			w.finishLock()
			w.record("recovery-failed", stopErr.Error())
			return fmt.Errorf("%w; recovery could not safely stop Claude: %v", cause, stopErr)
		}
	}
	var restoreErr error
	if w.previous == "" {
		restoreErr = w.store.WipeAt(w.appData, w.live)
	} else {
		restoreErr = w.store.RestoreAt(w.previous, w.appData, w.live)
	}
	if restoreErr != nil {
		w.setStage(addCancelled)
		w.finishLock()
		w.record("recovery-failed", restoreErr.Error())
		return errors.Join(cause, fmt.Errorf("restore previous account state: %w", restoreErr))
	}
	launchErr := w.platform.LaunchApp()
	w.mu.Lock()
	w.stage = addCancelled
	w.mu.Unlock()
	w.finishLock()
	w.record("recovered", cause.Error())
	if launchErr != nil {
		return fmt.Errorf("%w; restored profile but Claude could not restart: %v", cause, launchErr)
	}
	if errors.Is(cause, errAddCancelled) {
		return nil
	}
	return cause
}

func (w *addWorkflow) finishLock() {
	if w.lock != nil {
		w.lock.Release()
		w.lock = nil
	}
}
