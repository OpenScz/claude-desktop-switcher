# Regression review checklist

Use this before publishing a release.

- Run `.\scripts\project.ps1 -Task Validate -Race` on a supported Windows build environment.
- Confirm a local `dev.*` build does not announce a GitHub release; a normal three-part release does only when GitHub has a newer stable release.
- Confirm the tray shows `Open Claude Desktop` only when Claude is closed, and `Close Claude Desktop` only when it is open.
- Confirm closing from the tray preserves the active profile and reopening restores that profile.
- Confirm the current-version item opens the repository home page and the update item opens the latest release page.
- Exercise add, switch, delete, export, and import with Claude both open and closed.
- Verify no profile, cookie, or backup content appears in logs or in Git status.
- Confirm `git status --ignored` does not offer `.claude-swap`, `.csb`, `.bak`, or log files for commit.
- Install from a clean Windows user profile, launch from Start, and verify the tray opens without a console window.
- Start the portable executable by double-click and from a desktop shortcut, and verify the tray opens.
- During a switch, confirm only the small progress card appears, the screen is not dimmed, and the taskbar stays usable.
- Switch away from and back to an account twice in a row, and confirm both outgoing profiles were saved without an error.

## Permanent regression coverage

| Previously observed problem | Permanent check |
|-----------------------------|-----------------|
| A second launch created another tray process | `TestTrayLockRejectsDuplicateImmediately`, `TestOperationLockRejectsConcurrentOperation` |
| Double-clicking the app did not start tray mode | `TestStartupArgs` |
| Start and Startup shortcuts opened the wrong executable or a console | `TestWindowsInstallerUsesNativeLauncher`, `TestCommandsDoNotCreateConsoleWindows` |
| A closed Claude instance still showed an active account | `TestResolveActiveProfileUsesOnlyVerifiedOrTrustedState` |
| Active accounts were selectable or removable at the wrong time | `TestResolveActiveProfileUsesOnlyVerifiedOrTrustedState`, `TestResolveDeleteActivityRequiresVerifiedLiveState` |
| Claude processes timed out during close | `TestStopWindowsProcessesAcceptsLateExit`, `TestStopWindowsProcessesStillRejectsStuckProcess` |
| The tray Open/Close command did not follow Claude state | `TestClaudeMenuState` |
| A switch failed with "Access is denied" after Claude had closed | `TestCheckpointSucceedsWhileTheSavedProfileIsBrieflyRead`, `TestRenamePathWaitsForABriefReader`, `TestBackgroundReadsPauseWhileProfilesChange` |
| The tray process would stop after 2000 window checks | `TestEnumTopLevelWindowsCanRunMoreTimesThanGoAllowsCallbacks` |
| Reopening Claude took a maximized window out of its maximized size | `TestShowCommandKeepsAMaximizedWindowMaximized` |
| A double-click or desktop shortcut on the executable did nothing | `TestExplorerStartIsNotStoppedByTheCommandLineGuard` |

Every confirmed regression must receive a focused test before its fix is
considered complete.
