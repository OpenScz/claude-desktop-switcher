package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TinoA/claude-desktop-switcher/internal/profile"
)

type workflowStoreFake struct {
	events         []string
	current        string
	matched        string
	matchErr       error
	checkpointErr  error
	checkpointErrs map[string]error
	restoreErr     error
}

func (s *workflowStoreFake) Exists(name string) bool { return name != "" && name == s.current }
func (s *workflowStoreFake) Current() (string, error) {
	s.events = append(s.events, "current")
	return s.current, nil
}
func (s *workflowStoreFake) SetCurrent(name string) error {
	s.events = append(s.events, "set-current:"+name)
	s.current = name
	return nil
}
func (s *workflowStoreFake) FindByAccountIdentityAt(string) (string, error) {
	return s.matched, s.matchErr
}
func (s *workflowStoreFake) CheckpointAt(name, _, _ string) error {
	s.events = append(s.events, "checkpoint:"+name)
	if s.checkpointErrs != nil {
		return s.checkpointErrs[name]
	}
	return s.checkpointErr
}
func (s *workflowStoreFake) WipeAt(_, _ string) error {
	s.events = append(s.events, "wipe")
	return nil
}
func (s *workflowStoreFake) RestoreAt(name, _, _ string) error {
	s.events = append(s.events, "restore:"+name)
	return s.restoreErr
}

type workflowPlatformFake struct {
	events           []string
	running          bool
	killErr          error
	killKeepsRunning bool
}

func (p *workflowPlatformFake) AppDataPath() (string, error) { return `C:\synthetic`, nil }
func (p *workflowPlatformFake) IsRunning() (bool, error) {
	p.events = append(p.events, "is-running")
	return p.running, nil
}
func (p *workflowPlatformFake) KillApp() error {
	p.events = append(p.events, "kill")
	if !p.killKeepsRunning {
		p.running = false
	}
	return p.killErr
}

func TestAddWorkflowContinuesWhenKillTimesOutAfterClaudeExited(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true, killErr: errors.New("exit timeout")}
	w := newWorkflowTest(t, store, platform)

	if err := w.Begin("work"); err != nil {
		t.Fatal(err)
	}
	if workflowStage(w) != addWaitingLogin || !platform.running {
		t.Fatalf("stage/running = %s/%v", workflowStage(w), platform.running)
	}
	want := []string{"is-running", "kill", "is-running", "launch"}
	if !reflect.DeepEqual(platform.events, want) {
		t.Fatalf("platform events = %v, want %v", platform.events, want)
	}
}

func TestAddWorkflowKillFailureKeepsExistingClaudeRunning(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true, killErr: errors.New("access denied"), killKeepsRunning: true}
	w := newWorkflowTest(t, store, platform)

	if err := w.Begin("work"); err == nil {
		t.Fatal("Begin should fail while Claude is still running")
	}
	if !platform.running || workflowStage(w) != addCancelled {
		t.Fatalf("running/stage = %v/%s", platform.running, workflowStage(w))
	}
	want := []string{"is-running", "kill", "is-running", "is-running"}
	if !reflect.DeepEqual(platform.events, want) {
		t.Fatalf("platform events = %v, want %v", platform.events, want)
	}
}
func (p *workflowPlatformFake) LaunchApp() error {
	p.events = append(p.events, "launch")
	p.running = true
	return nil
}

func newWorkflowTest(t *testing.T, store *workflowStoreFake, platform *workflowPlatformFake) *addWorkflow {
	t.Helper()
	lockPath := filepath.Join(t.TempDir(), "operation.lock")
	file, err := os.Create(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	lock := &operationLock{file: file, path: lockPath}
	t.Cleanup(lock.Release)
	return &addWorkflow{
		store:             store,
		platform:          platform,
		appData:           `C:\synthetic`,
		live:              `C:\synthetic\Network\Cookies`,
		stage:             addIdle,
		lock:              lock,
		sessionUsable:     func(string) bool { return true },
		accountStateReady: func(string) bool { return true },
		accountStateID:    func(string) string { return "stable-state" },
		persistPending:    func(pendingAdd) error { return nil },
		removePending:     func() error { return nil },
		sessionPoll:       time.Millisecond,
		sessionTimeout:    5 * time.Millisecond,
	}
}

func workflowStage(w *addWorkflow) addStage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stage
}

func TestAddRecoveryKeepsClaudeClosedWhenOriginalsNeedRecovery(t *testing.T) {
	for _, initialFailure := range []bool{false, true} {
		t.Setenv("USERPROFILE", t.TempDir())
		t.Setenv("HOME", t.TempDir())
		if err := writePendingAdd(pendingAdd{Name: "work", Previous: "personal"}); err != nil {
			t.Fatal(err)
		}
		store := &workflowStoreFake{current: "personal", restoreErr: profile.ErrRecoveryRequired}
		platform := &workflowPlatformFake{}
		w := newWorkflowTest(t, store, platform)
		w.removePending = clearPendingAdd
		w.previous = "personal"
		cause := errors.New("login failed")
		if initialFailure {
			cause = profile.ErrRecoveryRequired
		}
		err := w.recover(cause)
		if !errors.Is(err, profile.ErrRecoveryRequired) || workflowStage(w) != addCancelled {
			t.Fatalf("incomplete recovery = %v/%s", err, workflowStage(w))
		}
		if containsEvent(platform.events, "launch") {
			t.Fatalf("unsafe launch after incomplete recovery: %v", platform.events)
		}
		if initialFailure && len(store.events) != 0 {
			t.Fatalf("mutated state requiring manual recovery: %v", store.events)
		}
		if _, err := loadPendingAdd(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tray restart could resume an incomplete recovery: %v", err)
		}
	}
}

func TestAddRecoveryStopsIfPendingLoginCannotBeCancelled(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{}
	w := newWorkflowTest(t, store, platform)
	w.previous = "personal"
	removeErr := errors.New("pending login is locked")
	w.removePending = func() error { return removeErr }
	err := w.recover(errors.New("login failed"))
	if !errors.Is(err, removeErr) || !errors.Is(err, profile.ErrRecoveryRequired) {
		t.Fatalf("pending cleanup error was lost: %v", err)
	}
	if len(store.events) != 0 || len(platform.events) != 0 {
		t.Fatalf("mutated state despite resumable pending login: %v/%v", store.events, platform.events)
	}
}

func TestAddWorkflowBeginCompleteAndRelaunchesClaude(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)

	if err := w.Begin("work"); err != nil {
		t.Fatal(err)
	}
	if workflowStage(w) != addWaitingLogin {
		t.Fatalf("stage after Begin = %s", workflowStage(w))
	}
	if err := w.Complete(); err != nil {
		t.Fatal(err)
	}
	wantStore := []string{"current", "checkpoint:personal", "wipe", "checkpoint:work", "restore:work"}
	wantPlatform := []string{"is-running", "kill", "launch", "kill", "launch"}
	if !reflect.DeepEqual(store.events, wantStore) {
		t.Fatalf("store events = %v, want %v", store.events, wantStore)
	}
	if !reflect.DeepEqual(platform.events, wantPlatform) {
		t.Fatalf("platform events = %v, want %v", platform.events, wantPlatform)
	}
	if workflowStage(w) != addCompleted || platform.running != true {
		t.Fatalf("stage/running = %s/%v", workflowStage(w), platform.running)
	}
}

func TestAddWorkflowCreatesFirstAccountWithoutPreviousProfile(t *testing.T) {
	store := &workflowStoreFake{}
	platform := &workflowPlatformFake{}
	w := newWorkflowTest(t, store, platform)
	var sessionReady atomic.Bool
	w.sessionUsable = func(string) bool { return sessionReady.Load() }

	if err := w.Begin("first"); err != nil {
		t.Fatal(err)
	}
	sessionReady.Store(true)
	if err := w.Complete(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.events, []string{"current", "wipe", "checkpoint:first", "restore:first"}) {
		t.Fatalf("store events = %v", store.events)
	}
	if !reflect.DeepEqual(platform.events, []string{"is-running", "launch", "kill", "launch"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
}

func TestAddWorkflowCancelsFirstAccountWithoutRestoreProfile(t *testing.T) {
	store := &workflowStoreFake{}
	platform := &workflowPlatformFake{}
	w := newWorkflowTest(t, store, platform)
	w.sessionUsable = func(string) bool { return false }

	if err := w.Begin("first"); err != nil {
		t.Fatal(err)
	}
	if err := w.Cancel(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.events, []string{"current", "wipe", "wipe"}) {
		t.Fatalf("store events = %v", store.events)
	}
	if !reflect.DeepEqual(platform.events, []string{"is-running", "launch", "is-running", "kill", "launch"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
}

func TestAddWorkflowCompleteBeforeLoginKeepsWaiting(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	w.name = "work"
	w.previous = "personal"
	w.stage = addWaitingLogin
	w.sessionUsable = func(string) bool { return false }
	w.loginEvidence = func(string, string) bool { return false }

	err := w.Complete()
	if !errors.Is(err, errAddLoginNotReady) {
		t.Fatalf("Complete error = %v", err)
	}
	if workflowStage(w) != addWaitingLogin {
		t.Fatalf("stage = %s", workflowStage(w))
	}
	if len(store.events) != 0 || len(platform.events) != 0 {
		t.Fatalf("premature finish changed state: store=%v platform=%v", store.events, platform.events)
	}
}

func TestAddWorkflowAcceptsLatestLoginWithStableEncryptedState(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	w.createdAt = time.Now()
	w.loginLogOffset = 42
	w.loginEvidence = func(string, string) bool { return false }
	w.accountLogState = func(_ string, offset int64, createdAt time.Time) profile.AccountLogState {
		if offset == 42 && !createdAt.IsZero() {
			return profile.AccountLogSignedIn
		}
		return profile.AccountLogUnknown
	}
	w.sessionUsable = func(string) bool { return false }

	if w.loginReady() {
		t.Fatal("locked cookies should remain unavailable until Claude closes")
	}
	if !w.loginDetected() {
		t.Fatal("login event with complete encrypted account state was not detected")
	}
	if w.loginState() == "" {
		t.Fatal("latest login with stable encrypted account state should be ready for completion")
	}
}

func TestAddWorkflowRejectsOlderLoginWhenLatestStateIsSignedOut(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	w.sessionUsable = func(string) bool { return false }
	w.loginEvidence = func(string, string) bool { return true }
	w.accountLogState = func(string, int64, time.Time) profile.AccountLogState {
		return profile.AccountLogSignedOut
	}

	if w.loginDetected() || w.loginState() != "" {
		t.Fatal("an older login event was trusted after a newer logout")
	}
}

func TestAddWorkflowRejectsLoginEventWithoutCompleteAccountState(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	w.createdAt = time.Now()
	w.loginLogOffset = 42
	w.accountStateReady = func(string) bool { return false }
	w.accountLogState = func(string, int64, time.Time) profile.AccountLogState {
		return profile.AccountLogSignedIn
	}
	w.loginEvidence = func(string, string) bool { return false }
	w.sessionUsable = func(string) bool { return false }

	if w.loginDetected() {
		t.Fatal("login event without persisted encrypted account state was accepted")
	}
}

func TestAddWorkflowCompleteValidatesCookiesAfterStoppingClaude(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	w.name = "work"
	w.previous = "personal"
	w.stage = addWaitingLogin
	w.loginEvidence = func(string, string) bool { return true }
	w.sessionUsable = func(string) bool { return true }

	if err := w.Complete(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.events, []string{"checkpoint:work", "restore:work"}) {
		t.Fatalf("store events = %v", store.events)
	}
	if !reflect.DeepEqual(platform.events, []string{"kill", "launch"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
	if workflowStage(w) != addCompleted || !platform.running {
		t.Fatalf("stage/running = %s/%v", workflowStage(w), platform.running)
	}
}

func TestAddWorkflowCompleteContinuesWhenKillTimesOutAfterClaudeExited(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true, killErr: errors.New("exit timeout")}
	w := newWorkflowTest(t, store, platform)
	w.name = "work"
	w.previous = "personal"
	w.stage = addWaitingLogin

	if err := w.Complete(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.events, []string{"checkpoint:work", "restore:work"}) {
		t.Fatalf("store events = %v", store.events)
	}
	if !reflect.DeepEqual(platform.events, []string{"kill", "is-running", "launch"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
}

func TestAddWorkflowWaitsForSessionPersistenceAfterStoppingClaude(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	w.name = "work"
	w.previous = "personal"
	w.stage = addWaitingLogin
	w.loginEvidence = func(string, string) bool { return true }
	var checks atomic.Int32
	w.sessionUsable = func(string) bool {
		check := checks.Add(1)
		return check == 1 || check >= 3
	}

	if err := w.Complete(); err != nil {
		t.Fatal(err)
	}
	if checks.Load() < 3 {
		t.Fatalf("session checks = %d", checks.Load())
	}
	if !reflect.DeepEqual(store.events, []string{"checkpoint:work", "restore:work"}) {
		t.Fatalf("store events = %v", store.events)
	}
}

func TestAddWorkflowIncompleteSessionReopensAndKeepsWaiting(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	w.name = "work"
	w.previous = "personal"
	w.stage = addWaitingLogin
	w.loginEvidence = func(string, string) bool { return true }
	w.sessionUsable = func(string) bool { return platform.running }
	removed := false
	var pending pendingAdd
	w.persistPending = func(value pendingAdd) error {
		pending = value
		return nil
	}
	w.removePending = func() error {
		removed = true
		return nil
	}

	err := w.Complete()
	if !errors.Is(err, errAddLoginNotReady) {
		t.Fatalf("Complete error = %v", err)
	}
	if len(store.events) != 0 {
		t.Fatalf("store events = %v", store.events)
	}
	if !reflect.DeepEqual(platform.events, []string{"kill", "launch"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
	if removed || workflowStage(w) != addWaitingLogin || !platform.running {
		t.Fatalf("pending/stage/running = %v/%s/%v", removed, workflowStage(w), platform.running)
	}
	if pending.PersistenceRetries != 1 {
		t.Fatalf("persistence retries = %d, want 1", pending.PersistenceRetries)
	}
}

func TestAddWorkflowStopsPersistenceRestartLoop(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	w.name = "work"
	w.previous = "personal"
	w.stage = addWaitingLogin
	w.persistenceRetries = maxPersistenceRetries
	w.sessionUsable = func(string) bool { return platform.running }
	removed := false
	w.removePending = func() error {
		removed = true
		return nil
	}

	err := w.Complete()
	if err == nil || errors.Is(err, errAddLoginNotReady) {
		t.Fatalf("Complete error = %v", err)
	}
	if !reflect.DeepEqual(store.events, []string{"restore:personal"}) {
		t.Fatalf("store events = %v", store.events)
	}
	if !reflect.DeepEqual(platform.events, []string{"kill", "is-running", "launch"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
	if !removed || workflowStage(w) != addCancelled || !platform.running {
		t.Fatalf("pending/stage/running = %v/%s/%v", removed, workflowStage(w), platform.running)
	}
}

func TestPendingAddPersistsRestartLimit(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	want := pendingAdd{
		Name:               "work",
		Previous:           "personal",
		AppData:            `C:\Claude`,
		Live:               `C:\Claude\Network\Cookies`,
		CreatedAt:          time.Now().Truncate(time.Nanosecond),
		LoginLogOffset:     42,
		PersistenceRetries: maxPersistenceRetries,
	}
	if err := writePendingAdd(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadPendingAdd()
	if err != nil {
		t.Fatal(err)
	}
	gotCreatedAt := got.CreatedAt
	wantCreatedAt := want.CreatedAt
	got.CreatedAt = time.Time{}
	want.CreatedAt = time.Time{}
	if !gotCreatedAt.Equal(wantCreatedAt) || !reflect.DeepEqual(got, want) {
		t.Fatalf("pending add = %+v, want %+v", got, want)
	}
}

func TestAddWorkflowResumesPendingLoginWhenClaudeIsClosed(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{}
	w := newWorkflowTest(t, store, platform)
	w.stage = addWaitingLogin
	w.resumed = true
	var pending pendingAdd
	w.persistPending = func(value pendingAdd) error {
		pending = value
		return nil
	}

	if err := w.ResumePendingLogin(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(platform.events, []string{"is-running", "launch"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
	if !platform.running || workflowStage(w) != addWaitingLogin {
		t.Fatalf("running/stage = %v/%s", platform.running, workflowStage(w))
	}
	if pending.CreatedAt.IsZero() {
		t.Fatal("resumed login did not reset its detection baseline")
	}
}

func TestAddWorkflowDoesNotRelaunchPendingLoginWhenClaudeIsOpen(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	w.stage = addWaitingLogin
	w.resumed = true

	if err := w.ResumePendingLogin(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(platform.events, []string{"is-running"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
}

func TestAddWorkflowRetriesAfterSessionFinishesPersisting(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	if err := w.Begin("work"); err != nil {
		t.Fatal(err)
	}
	w.loginPoll = time.Millisecond
	w.loginSettle = 0
	w.sessionUsable = func(string) bool {
		launches := 0
		for _, event := range platform.events {
			if event == "launch" {
				launches++
			}
		}
		return platform.running || launches >= 2
	}

	if err := w.WaitAndComplete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.events, []string{"current", "checkpoint:personal", "wipe", "checkpoint:work", "restore:work"}) {
		t.Fatalf("store events = %v", store.events)
	}
	if !reflect.DeepEqual(platform.events, []string{"is-running", "kill", "launch", "kill", "launch", "kill", "launch"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
	if workflowStage(w) != addCompleted || !platform.running {
		t.Fatalf("stage/running = %s/%v", workflowStage(w), platform.running)
	}
}

func TestRestartLoginProcessRecreatesClaude(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	workflow := newWorkflowTest(t, store, platform)
	if err := restartLoginProcess(workflow, platform); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.events, []string{"wipe"}) {
		t.Fatalf("store events = %v", store.events)
	}
	if !reflect.DeepEqual(platform.events, []string{"is-running", "kill", "launch"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
	if !platform.running {
		t.Fatal("Claude should be running after login restart")
	}
}

func TestAddWorkflowCancelRestoresPreviousProfile(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)

	if err := w.Begin("work"); err != nil {
		t.Fatal(err)
	}
	if err := w.Cancel(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.events, []string{"current", "checkpoint:personal", "wipe", "restore:personal"}) {
		t.Fatalf("store events = %v", store.events)
	}
	if !reflect.DeepEqual(platform.events, []string{"is-running", "kill", "launch", "is-running", "kill", "launch"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
	if workflowStage(w) != addCancelled || !platform.running {
		t.Fatalf("stage/running = %s/%v", workflowStage(w), platform.running)
	}
}

func TestAddWorkflowFailedNewCheckpointRecoversWithoutRetry(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)

	if err := w.Begin("work"); err != nil {
		t.Fatal(err)
	}
	store.checkpointErr = errors.New("login cookie missing")
	err := w.Complete()
	if err == nil || errors.Is(err, errAddLoginNotReady) {
		t.Fatalf("Complete error = %v", err)
	}
	if !reflect.DeepEqual(store.events, []string{"current", "checkpoint:personal", "wipe", "checkpoint:work", "restore:personal"}) {
		t.Fatalf("store events = %v", store.events)
	}
	if !reflect.DeepEqual(platform.events, []string{"is-running", "kill", "launch", "kill", "is-running", "launch"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
	if workflowStage(w) != addCancelled || !platform.running {
		t.Fatalf("stage/running = %s/%v", workflowStage(w), platform.running)
	}
}

func TestAddWorkflowDuplicateAccountUpdatesExistingAndRestarts(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)

	if err := w.Begin("work"); err != nil {
		t.Fatal(err)
	}
	store.matched = "personal"
	err := w.Complete()
	var duplicate *profile.DuplicateAccountError
	if !errors.As(err, &duplicate) || duplicate.ExistingName != "personal" {
		t.Fatalf("Complete error = %v", err)
	}
	wantStore := []string{"current", "checkpoint:personal", "wipe", "checkpoint:personal", "restore:personal", "set-current:personal"}
	if !reflect.DeepEqual(store.events, wantStore) {
		t.Fatalf("store events = %v, want %v", store.events, wantStore)
	}
	wantPlatform := []string{"is-running", "kill", "launch", "kill", "launch"}
	if !reflect.DeepEqual(platform.events, wantPlatform) {
		t.Fatalf("platform events = %v, want %v", platform.events, wantPlatform)
	}
	if workflowStage(w) != addCompleted || !platform.running {
		t.Fatalf("stage/running = %s/%v", workflowStage(w), platform.running)
	}
}

func TestAddWorkflowDuplicateFallbackRestoresDetectedProfile(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)

	if err := w.Begin("work"); err != nil {
		t.Fatal(err)
	}
	store.checkpointErrs = map[string]error{
		"work": &profile.DuplicateAccountError{ExistingName: "personal"},
	}
	err := w.Complete()
	var duplicate *profile.DuplicateAccountError
	if !errors.As(err, &duplicate) || duplicate.ExistingName != "personal" {
		t.Fatalf("Complete error = %v", err)
	}
	wantStore := []string{"current", "checkpoint:personal", "wipe", "checkpoint:work", "checkpoint:personal", "restore:personal", "set-current:personal"}
	if !reflect.DeepEqual(store.events, wantStore) {
		t.Fatalf("store events = %v, want %v", store.events, wantStore)
	}
	wantPlatform := []string{"is-running", "kill", "launch", "kill", "launch"}
	if !reflect.DeepEqual(platform.events, wantPlatform) {
		t.Fatalf("platform events = %v, want %v", platform.events, wantPlatform)
	}
	if workflowStage(w) != addCompleted || !platform.running {
		t.Fatalf("stage/running = %s/%v", workflowStage(w), platform.running)
	}
}

func TestAddWorkflowRecoversPendingDuplicateFromLoggedOutClaude(t *testing.T) {
	store := &workflowStoreFake{current: "personal", matched: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	w.name = "work"
	w.previous = "personal"
	w.stage = addWaitingLogin
	w.resumed = true
	w.sessionUsable = func(string) bool { return false }

	handled, err := w.RecoverPendingDuplicate()
	var duplicate *profile.DuplicateAccountError
	if !handled || !errors.As(err, &duplicate) || duplicate.ExistingName != "personal" {
		t.Fatalf("RecoverPendingDuplicate = %v, %v", handled, err)
	}
	wantStore := []string{"restore:personal", "set-current:personal"}
	if !reflect.DeepEqual(store.events, wantStore) {
		t.Fatalf("store events = %v, want %v", store.events, wantStore)
	}
	wantPlatform := []string{"is-running", "kill", "launch"}
	if !reflect.DeepEqual(platform.events, wantPlatform) {
		t.Fatalf("platform events = %v, want %v", platform.events, wantPlatform)
	}
	if workflowStage(w) != addCompleted || !platform.running {
		t.Fatalf("stage/running = %s/%v", workflowStage(w), platform.running)
	}
}

func TestAddWorkflowUpdatesPendingDuplicateWithUsableLiveSession(t *testing.T) {
	store := &workflowStoreFake{current: "personal", matched: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	w.name = "work"
	w.previous = "personal"
	w.stage = addWaitingLogin
	w.resumed = true

	handled, err := w.RecoverPendingDuplicate()
	var duplicate *profile.DuplicateAccountError
	if !handled || !errors.As(err, &duplicate) || duplicate.ExistingName != "personal" {
		t.Fatalf("RecoverPendingDuplicate = %v, %v", handled, err)
	}
	if !reflect.DeepEqual(store.events, []string{"checkpoint:personal", "restore:personal", "set-current:personal"}) {
		t.Fatalf("store events = %v", store.events)
	}
	if !reflect.DeepEqual(platform.events, []string{"kill", "launch"}) {
		t.Fatalf("platform events = %v", platform.events)
	}
}

func TestOperationLockRejectsConcurrentOperation(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	first, err := acquireOperationLock("test-operation")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if _, err := acquireOperationLock("test-operation"); err == nil {
		t.Fatal("second operation should be rejected")
	}
}

func TestOperationLockWaitsForPreviousOwnerToExit(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	first, err := acquireOperationLock("test-operation")
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		first.Release()
		close(released)
	}()

	second, err := acquireOperationLockWithWait("test-operation", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
	<-released
}

func TestOperationLockReleaseDoesNotRemoveReplacement(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	first, err := acquireOperationLock("test-operation")
	if err != nil {
		t.Fatal(err)
	}
	first.Release()

	replacement, err := acquireOperationLock("test-operation")
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Release()
	first.Release()

	if _, err := os.Stat(replacement.path); err != nil {
		t.Fatalf("replacement lock was removed: %v", err)
	}
}

func TestAddWorkflowAutoCompletesWhenLoginAppears(t *testing.T) {
	store := &workflowStoreFake{current: "personal"}
	platform := &workflowPlatformFake{running: true}
	w := newWorkflowTest(t, store, platform)
	if err := w.Begin("work"); err != nil {
		t.Fatal(err)
	}
	var active atomic.Bool
	w.sessionUsable = func(string) bool { return active.Load() }
	w.loginPoll = time.Millisecond
	go func() {
		time.Sleep(5 * time.Millisecond)
		active.Store(true)
	}()
	if err := w.WaitAndComplete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if workflowStage(w) != addCompleted || !platform.running {
		t.Fatalf("stage/running = %s/%v", workflowStage(w), platform.running)
	}
}
