package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/TinoA/claude-desktop-switcher/internal/platform"
	"github.com/TinoA/claude-desktop-switcher/internal/profile"
	"github.com/spf13/cobra"
)

var cmdUse = &cobra.Command{
	Use:   "use [name]",
	Short: "Switch to a saved profile (kills and restarts Claude Desktop)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if dryRun {
			name := "<interactive>"
			if len(args) == 1 {
				name = args[0]
			}
			return printDryRun("use: checkpoint, atomic restore, Desktop restart", name)
		}
		store, err := profile.NewStore()
		if err != nil {
			return err
		}

		name, err := profileNameFromArgs(args, store)
		if err != nil {
			return err
		}
		if name == "" {
			return nil
		}

		return switchProfile(name, store)
	},
}

var (
	errLiveSessionUnrecognized = errors.New("live Claude session is not recognized by a saved profile; no profiles were changed")
	errLiveSessionUnverified   = errors.New("live Claude session cannot be verified; no profiles were changed")
)

func profileNameFromArgs(args []string, store *profile.Store) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}

	profiles, err := store.List()
	if err != nil {
		return "", err
	}
	if len(profiles) == 0 {
		fmt.Println("No profiles saved. Run 'claude-desktop-swap save <name>' to create one.")
		return "", nil
	}

	current := ""
	if appData, err := platform.Current().AppDataPath(); err == nil {
		current, _ = store.MatchLiveAt(platform.CookiesPath(appData))
	}
	enrichLiveAccounts(store, profiles)
	return runPicker(profiles, current)
}

func switchProfile(name string, store *profile.Store) error {
	lock, err := acquireOperationLock("operation")
	if err != nil {
		return err
	}
	defer lock.Release()
	overlay := startSwitchOverlay()
	defer overlay.Close()
	err = switchProfileWith(name, store, platform.Current(), os.Stdout)
	if err == nil {
		if pending, pendingErr := loadPendingAdd(); pendingErr == nil && pending.Previous == name {
			_ = clearPendingAdd()
		}
	}
	return err
}

type switchStore interface {
	Exists(string) bool
	Inspect(string) profile.Inspection
	Current() (string, error)
	Checkpoint(string, string) error
	Restore(string, string) error
}

type pathSwitcher interface {
	CheckpointAt(string, string, string) error
	RestoreAt(string, string, string) error
}

const (
	launchVerificationPoll     = 250 * time.Millisecond
	launchVerificationSettle   = 3 * time.Second
	launchVerificationFallback = 5 * time.Second
	launchVerificationTimeout  = 12 * time.Second
)

func switchProfileWith(name string, store switchStore, p platform.Platform, out io.Writer) error {
	appData, err := p.AppDataPath()
	if err != nil {
		return err
	}

	if !store.Exists(name) {
		return fmt.Errorf("profile %q not found — run 'claude-desktop-swap list' to see available profiles", name)
	}
	inspection := store.Inspect(name)
	if inspection.Health != profile.HealthUsable {
		return fmt.Errorf("profile %q is %s; reauthentication is required before switching", name, inspection.Health)
	}

	wasRunning, err := p.IsRunning()
	if err != nil {
		return fmt.Errorf("detect Claude Desktop: %w", err)
	}
	if !wasRunning {
		fmt.Fprintf(out, "Restoring profile %q...\n", name)
		var restoreErr error
		if routed, ok := store.(pathSwitcher); ok {
			restoreErr = routed.RestoreAt(name, appData, platform.CookiesPath(appData))
		} else {
			restoreErr = store.Restore(name, appData)
		}
		if restoreErr != nil {
			return fmt.Errorf("restore profile: %w", restoreErr)
		}
		fmt.Fprintln(out, "Starting Claude Desktop...")
		if err := launchRestoredProfile(name, store, p, appData); err != nil {
			return err
		}
		fmt.Fprintf(out, "Switched to %q.\n", name)
		return nil
	}
	current, currentErr := store.Current()
	liveName, liveHealth, hasLiveMatcher := matchLiveProfile(store, platform.CookiesPath(appData))
	if currentErr != nil {
		current = ""
	}
	if hasLiveMatcher {
		liveName, err = resolveLiveProfileIdentity(store, appData, liveName, liveHealth)
		if err != nil {
			return fmt.Errorf("identify live Claude session: %w", err)
		}
	}

	if hasLiveMatcher && liveName == name && liveHealth == profile.HealthUsable {
		if err := markCurrentProfile(store, name); err != nil {
			return fmt.Errorf("track active profile: %w", err)
		}
		if wasRunning {
			fmt.Fprintf(out, "Profile %q is already active.\n", name)
			return nil
		}
		fmt.Fprintln(out, "Starting Claude Desktop...")
		if err := launchRestoredProfile(name, store, p, appData); err != nil {
			return err
		}
		fmt.Fprintf(out, "Switched to %q.\n", name)
		return nil
	}

	fmt.Fprintln(out, "Stopping Claude Desktop...")
	stopped := false
	if wasRunning {
		if err := p.KillApp(); err != nil {
			return fmt.Errorf("stop Claude: %w", err)
		}
		stopped = true
	}
	relaunchPrevious := func(operationErr error) error {
		if !stopped || errors.Is(operationErr, profile.ErrRecoveryRequired) {
			return operationErr
		}
		if launchErr := p.LaunchApp(); launchErr != nil {
			return fmt.Errorf("%w; could not relaunch previous Claude session: %v", operationErr, launchErr)
		}
		return operationErr
	}

	if hasLiveMatcher {
		liveName, liveHealth, _ = matchLiveProfile(store, platform.CookiesPath(appData))
		liveName, err = resolveLiveProfileIdentity(store, appData, liveName, liveHealth)
		if err != nil {
			return relaunchPrevious(fmt.Errorf("identify live Claude session: %w", err))
		}
		switch liveHealth {
		case profile.HealthUsable:
			if liveName == "" {
				return relaunchPrevious(errLiveSessionUnrecognized)
			}
		case profile.HealthUnknown:
			return relaunchPrevious(errLiveSessionUnverified)
		}
	}

	outgoing := ""
	if !hasLiveMatcher {
		outgoing = current
	} else if liveHealth == profile.HealthUsable {
		outgoing = liveName
	}
	if outgoing != "" && outgoing != name {
		fmt.Fprintf(out, "Checkpointing profile %q...\n", outgoing)
		var checkpointErr error
		if routed, ok := store.(pathSwitcher); ok {
			checkpointErr = routed.CheckpointAt(outgoing, appData, platform.CookiesPath(appData))
		} else {
			checkpointErr = store.Checkpoint(outgoing, appData)
		}
		if checkpointErr != nil {
			return relaunchPrevious(fmt.Errorf("checkpoint outgoing profile: %w", checkpointErr))
		}
	}

	if liveName == name && liveHealth == profile.HealthUsable {
		if err := markCurrentProfile(store, name); err != nil {
			return fmt.Errorf("track active profile: %w", err)
		}
		fmt.Fprintln(out, "Starting Claude Desktop...")
		if err := launchRestoredProfile(name, store, p, appData); err != nil {
			return err
		}
		fmt.Fprintf(out, "Switched to %q.\n", name)
		return nil
	}

	fmt.Fprintf(out, "Restoring profile %q...\n", name)
	var restoreErr error
	if routed, ok := store.(pathSwitcher); ok {
		restoreErr = routed.RestoreAt(name, appData, platform.CookiesPath(appData))
	} else {
		restoreErr = store.Restore(name, appData)
	}
	if restoreErr != nil {
		return relaunchPrevious(fmt.Errorf("restore profile: %w", restoreErr))
	}

	fmt.Fprintln(out, "Starting Claude Desktop...")
	if err := launchRestoredProfile(name, store, p, appData); err != nil {
		return err
	}

	fmt.Fprintf(out, "Switched to %q.\n", name)
	return nil
}

type switchIdentityStore interface {
	FindByAccountIdentityAt(string) (string, error)
}

func launchRestoredProfile(name string, store switchStore, p platform.Platform, appData string) error {
	logOffset := profile.LoginLogOffsetAt(appData)
	startedAt := time.Now()
	if err := p.LaunchApp(); err != nil {
		if retryErr := p.LaunchApp(); retryErr != nil {
			return fmt.Errorf("profile %q is active but Claude could not start; launch manually: %w", name, err)
		}
	}
	identityStore, ok := store.(switchIdentityStore)
	if !ok {
		return nil
	}

	deadline := time.Now().Add(launchVerificationTimeout)
	var readySince time.Time
	var signedOutSince time.Time
	for {
		running, err := p.IsRunning()
		if err != nil {
			return fmt.Errorf("verify Claude after opening profile %q: %w", name, err)
		}
		if !running {
			return fmt.Errorf("claude closed before profile %q could be verified", name)
		}

		logState := profile.LatestAccountLogStateSince(appData, logOffset, startedAt)
		if logState == profile.AccountLogSignedOut {
			if signedOutSince.IsZero() {
				signedOutSince = time.Now()
			}
			if time.Since(signedOutSince) >= launchVerificationSettle {
				return fmt.Errorf("claude opened, but profile %q did not remain signed in", name)
			}
			readySince = time.Time{}
		} else {
			signedOutSince = time.Time{}
		}
		identity, identityErr := identityStore.FindByAccountIdentityAt(appData)
		identityReady := identityErr == nil && identity == name && profile.HasPersistedAccountStateAt(appData)
		logReady := logState == profile.AccountLogSignedIn ||
			logState == profile.AccountLogUnknown && time.Since(startedAt) >= launchVerificationFallback
		if logState != profile.AccountLogSignedOut && identityReady && logReady {
			if readySince.IsZero() {
				readySince = time.Now()
			}
			if time.Since(readySince) >= launchVerificationSettle {
				return nil
			}
		} else {
			readySince = time.Time{}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("claude opened, but profile %q could not be verified as signed in", name)
		}
		time.Sleep(launchVerificationPoll)
	}
}

func matchLiveProfile(store switchStore, cookies string) (name string, health profile.Health, supported bool) {
	matcher, ok := store.(interface {
		MatchLiveAt(string) (string, profile.Health)
	})
	if !ok {
		return "", profile.HealthUnknown, false
	}
	name, health = matcher.MatchLiveAt(cookies)
	return name, health, true
}

func resolveLiveProfileIdentity(store switchStore, appData, name string, health profile.Health) (string, error) {
	if name != "" || health != profile.HealthUsable {
		return name, nil
	}
	identityStore, ok := store.(switchIdentityStore)
	if !ok {
		return "", nil
	}
	return identityStore.FindByAccountIdentityAt(appData)
}

func markCurrentProfile(store switchStore, name string) error {
	setter, ok := store.(interface{ SetCurrent(string) error })
	if !ok {
		return nil
	}
	return setter.SetCurrent(name)
}
