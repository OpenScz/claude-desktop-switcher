package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TinoA/claude-desktop-switcher/internal/profile"
)

func TestSwitchProfileOrdersStopCheckpointRestoreAndLaunch(t *testing.T) {
	events := []string{}
	store := &fakeSwitchStore{events: &events, exists: true, current: "outgoing", health: profile.HealthUsable}
	p := &fakePlatform{events: &events, appData: t.TempDir(), running: true}
	if err := switchProfileWith("incoming", store, p, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"app-data", "inspect:incoming", "current", "stop", "checkpoint:outgoing", "restore:incoming", "launch"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if store.current != "incoming" {
		t.Fatalf("current = %q", store.current)
	}
}

func TestSwitchProfileStopsAfterCheckpointOrRestoreFailure(t *testing.T) {
	tests := []struct {
		name                      string
		checkpointErr, restoreErr error
		forbidden                 string
	}{
		{"checkpoint failure", errors.New("checkpoint failed"), nil, "restore:incoming"},
		{"restore interruption", nil, errors.New("restore failed"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := []string{}
			store := &fakeSwitchStore{events: &events, exists: true, current: "outgoing", health: profile.HealthUsable, checkpointErr: tt.checkpointErr, restoreErr: tt.restoreErr}
			p := &fakePlatform{events: &events, appData: t.TempDir(), running: true}
			if err := switchProfileWith("incoming", store, p, &bytes.Buffer{}); err == nil {
				t.Fatal("switch should fail")
			}
			if tt.forbidden != "" && strings.Contains(strings.Join(events, ","), tt.forbidden) {
				t.Fatalf("forbidden event %q in %v", tt.forbidden, events)
			}
			if store.current != "outgoing" {
				t.Fatalf("tracking advanced to %q", store.current)
			}
		})
	}
}

func TestSwitchProfileRefusesUnusableTargetBeforeStopping(t *testing.T) {
	for _, health := range []profile.Health{profile.HealthExpired, profile.HealthMissing, profile.HealthUnknown} {
		t.Run(string(health), func(t *testing.T) {
			events := []string{}
			store := &fakeSwitchStore{events: &events, exists: true, health: health}
			p := &fakePlatform{events: &events, appData: t.TempDir()}
			if err := switchProfileWith("incoming", store, p, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "reauthentication") {
				t.Fatalf("error = %v", err)
			}
			if containsEvent(events, "stop") {
				t.Fatalf("stopped app for unusable target: %v", events)
			}
		})
	}
}

func TestSwitchProfileKeepsClaudeClosedWhenRecoveryIsRequired(t *testing.T) {
	events := []string{}
	store := &fakeSwitchStore{events: &events, exists: true, current: "outgoing", health: profile.HealthUsable, restoreErr: profile.ErrRecoveryRequired}
	p := &fakePlatform{events: &events, appData: t.TempDir(), running: true}
	err := switchProfileWith("incoming", store, p, &bytes.Buffer{})
	if !errors.Is(err, profile.ErrRecoveryRequired) {
		t.Fatalf("recovery marker was lost: %v", err)
	}
	if containsEvent(events, "launch") || !containsEvent(events, "stop") {
		t.Fatalf("unsafe launch after incomplete recovery: %v", events)
	}
}

func TestSwitchProfileLaunchFailureKeepsCommittedIncomingState(t *testing.T) {
	events := []string{}
	store := &fakeSwitchStore{events: &events, exists: true, current: "outgoing", health: profile.HealthUsable}
	p := &fakePlatform{events: &events, appData: t.TempDir(), running: true, launchErr: errors.New("launch failed")}
	err := switchProfileWith("incoming", store, p, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "launch manually") {
		t.Fatalf("error = %v", err)
	}
	if store.current != "incoming" {
		t.Fatalf("committed current = %q", store.current)
	}
}

func TestSwitchProfileRestoresCurrentProfileWhenLiveCookiesAreMissing(t *testing.T) {
	events := []string{}
	store := &fakeSwitchStore{events: &events, exists: true, current: "incoming", health: profile.HealthUsable}
	p := &fakePlatform{events: &events, appData: t.TempDir(), running: true}
	if err := switchProfileWith("incoming", store, p, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"app-data", "inspect:incoming", "current", "stop", "restore:incoming", "launch"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestSwitchProfileRestoresDirectlyWhenClaudeIsClosed(t *testing.T) {
	events := []string{}
	store := &fakeSwitchStore{events: &events, exists: true, current: "outgoing", health: profile.HealthUsable}
	p := &fakePlatform{events: &events, appData: t.TempDir()}
	if err := switchProfileWith("incoming", store, p, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"app-data", "inspect:incoming", "restore:incoming", "launch"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestSwitchProfileDoesNotReportSuccessAfterClaudeLogsOut(t *testing.T) {
	events := []string{}
	appData := t.TempDir()
	store := &fakeVerifiedSwitchStore{
		fakeSwitchStore: &fakeSwitchStore{events: &events, exists: true, current: "outgoing", health: profile.HealthUsable},
		identity:        "incoming",
	}
	p := &fakePlatform{
		events:  &events,
		appData: appData,
		launchHook: func() {
			logs := filepath.Join(appData, "logs")
			if err := os.MkdirAll(logs, 0700); err != nil {
				t.Fatal(err)
			}
			line := time.Now().Format("2006-01-02 15:04:05") + " [info] [account] User logged out during IPC wait\n"
			if err := os.WriteFile(filepath.Join(logs, "main.log"), []byte(line), 0600); err != nil {
				t.Fatal(err)
			}
		},
	}
	var output bytes.Buffer
	err := switchProfileWith("incoming", store, p, &output)
	if err == nil || !strings.Contains(err.Error(), "did not remain signed in") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(output.String(), "Switched to") {
		t.Fatalf("success was reported after logout: %q", output.String())
	}
}

func TestSwitchProfileReportsSuccessAfterStableSignedInStartup(t *testing.T) {
	events := []string{}
	appData := t.TempDir()
	store := &fakeVerifiedSwitchStore{
		fakeSwitchStore: &fakeSwitchStore{events: &events, exists: true, current: "outgoing", health: profile.HealthUsable},
		identity:        "incoming",
	}
	p := &fakePlatform{
		events:  &events,
		appData: appData,
		launchHook: func() {
			if err := os.WriteFile(
				filepath.Join(appData, "config.json"),
				[]byte(`{"oauth:tokenCache":"djEwYQ==","lastKnownAccountUuid":"account-id"}`),
				0600,
			); err != nil {
				t.Fatal(err)
			}
			logs := filepath.Join(appData, "logs")
			if err := os.MkdirAll(logs, 0700); err != nil {
				t.Fatal(err)
			}
			line := time.Now().Format("2006-01-02 15:04:05") + " [info] claude.ai account active and logged in\n"
			if err := os.WriteFile(filepath.Join(logs, "main.log"), []byte(line), 0600); err != nil {
				t.Fatal(err)
			}
		},
	}
	var output bytes.Buffer
	if err := switchProfileWith("incoming", store, p, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `Switched to "incoming"`) {
		t.Fatalf("success was not reported: %q", output.String())
	}
}

func TestSwitchProfileSkipsRestoreForAlreadyActiveProfile(t *testing.T) {
	events := []string{}
	store := &fakeLiveSwitchStore{
		fakeSwitchStore: &fakeSwitchStore{events: &events, exists: true, current: "incoming", health: profile.HealthUsable},
		liveName:        "incoming",
		liveHealth:      profile.HealthUsable,
	}
	p := &fakePlatform{events: &events, appData: t.TempDir(), running: true}
	if err := switchProfileWith("incoming", store, p, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"app-data", "inspect:incoming", "current", "match"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestSwitchProfileRefusesUnknownLiveSessionAfterStopping(t *testing.T) {
	events := []string{}
	store := &fakeLiveSwitchStore{
		fakeSwitchStore: &fakeSwitchStore{events: &events, exists: true, current: "outgoing", health: profile.HealthUsable},
		liveHealth:      profile.HealthUnknown,
	}
	p := &fakePlatform{events: &events, appData: t.TempDir(), running: true}
	err := switchProfileWith("incoming", store, p, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot be verified") {
		t.Fatalf("error = %v", err)
	}
	if containsEvent(events, "checkpoint:outgoing") || containsEvent(events, "restore:incoming") {
		t.Fatalf("unknown live session was modified: %v", events)
	}
	if !containsEvent(events, "launch") {
		t.Fatalf("previous Claude session was not relaunched: %v", events)
	}
}

func TestSwitchProfileDoesNotCheckpointMissingLiveSession(t *testing.T) {
	events := []string{}
	store := &fakeLiveSwitchStore{
		fakeSwitchStore: &fakeSwitchStore{events: &events, exists: true, current: "outgoing", health: profile.HealthUsable},
		liveHealth:      profile.HealthMissing,
	}
	p := &fakePlatform{events: &events, appData: t.TempDir()}
	if err := switchProfileWith("incoming", store, p, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if containsEvent(events, "checkpoint:outgoing") {
		t.Fatalf("missing live session was checkpointed: %v", events)
	}
	if !containsEvent(events, "restore:incoming") {
		t.Fatalf("incoming profile was not restored: %v", events)
	}
}

func TestSwitchProfileNeverOverwritesUnrecognizedSession(t *testing.T) {
	events := []string{}
	store := &fakeLiveSwitchStore{
		fakeSwitchStore: &fakeSwitchStore{events: &events, exists: true, current: "outgoing", health: profile.HealthUsable},
		liveHealth:      profile.HealthUsable,
	}
	p := &fakePlatform{events: &events, appData: t.TempDir(), running: true}
	err := switchProfileWith("incoming", store, p, &bytes.Buffer{})
	if !errors.Is(err, errLiveSessionUnrecognized) {
		t.Fatalf("error = %v", err)
	}
	if containsEvent(events, "checkpoint:outgoing") || containsEvent(events, "restore:incoming") {
		t.Fatalf("unrecognized session was modified: %v", events)
	}
	if !containsEvent(events, "launch") {
		t.Fatalf("previous Claude session was not relaunched: %v", events)
	}
}

func TestResolveLiveProfileIdentityReconcilesRecognizedSession(t *testing.T) {
	events := []string{}
	store := &fakeVerifiedSwitchStore{
		fakeSwitchStore: &fakeSwitchStore{events: &events, exists: true, current: "outgoing", health: profile.HealthUsable},
		identity:        "outgoing",
	}
	name, err := resolveLiveProfileIdentity(store, t.TempDir(), "", profile.HealthUsable)
	if err != nil {
		t.Fatal(err)
	}
	if name != "outgoing" {
		t.Fatalf("identity match = %q, want outgoing", name)
	}
}

type fakeSwitchStore struct {
	events        *([]string)
	exists        bool
	current       string
	health        profile.Health
	checkpointErr error
	restoreErr    error
}

type fakeLiveSwitchStore struct {
	*fakeSwitchStore
	liveName   string
	liveHealth profile.Health
}

type fakeVerifiedSwitchStore struct {
	*fakeSwitchStore
	identity    string
	identityErr error
}

func (s *fakeVerifiedSwitchStore) FindByAccountIdentityAt(string) (string, error) {
	return s.identity, s.identityErr
}

func (s *fakeLiveSwitchStore) MatchLiveAt(string) (string, profile.Health) {
	*s.events = append(*s.events, "match")
	return s.liveName, s.liveHealth
}

func (s *fakeSwitchStore) Exists(string) bool { return s.exists }
func (s *fakeSwitchStore) Inspect(name string) profile.Inspection {
	*s.events = append(*s.events, "inspect:"+name)
	return profile.Inspection{Health: s.health}
}
func (s *fakeSwitchStore) Current() (string, error) {
	*s.events = append(*s.events, "current")
	return s.current, nil
}
func (s *fakeSwitchStore) Checkpoint(name, path string) error {
	*s.events = append(*s.events, "checkpoint:"+name)
	return s.checkpointErr
}
func (s *fakeSwitchStore) Restore(name, path string) error {
	*s.events = append(*s.events, "restore:"+name)
	if s.restoreErr == nil {
		s.current = name
	}
	return s.restoreErr
}

type fakePlatform struct {
	events     *([]string)
	appData    string
	appDataErr error
	running    bool
	launchErr  error
	launchHook func()
}

func (p *fakePlatform) AppDataPath() (string, error) {
	*p.events = append(*p.events, "app-data")
	return p.appData, p.appDataErr
}
func (p *fakePlatform) IsRunning() (bool, error) { return p.running, nil }
func (p *fakePlatform) IsInstalled() bool        { return p.appDataErr == nil }
func (p *fakePlatform) KillApp() error {
	*p.events = append(*p.events, "stop")
	p.running = false
	return nil
}
func (p *fakePlatform) LaunchApp() error {
	*p.events = append(*p.events, "launch")
	if p.launchErr == nil {
		p.running = true
		if p.launchHook != nil {
			p.launchHook()
		}
	}
	return p.launchErr
}

func containsEvent(events []string, event string) bool {
	for _, got := range events {
		if got == event {
			return true
		}
	}
	return false
}
