# Architecture

Claude Desktop Switcher keeps the command-line core and the Windows tray in one Go
binary. Platform-specific files isolate Windows and macOS behavior with Go build
tags.

## Main components

| Area | Responsibility |
|------|----------------|
| `cmd/` | CLI commands, tray behavior, native dialogs, and account workflows |
| `internal/platform/` | Claude Desktop discovery, process control, and OS paths |
| `internal/profile/` | Profile snapshots, identity matching, backups, and recovery |
| `internal/winproc/` | Hidden Windows child-process execution |
| `launcher/` | Small native Windows launcher used by Start and Startup shortcuts |
| `installer/` | Inno Setup installer and first-run guide |

Profiles are stored under `%USERPROFILE%\.claude-swap\profiles`. Claude Desktop
continues to own its live data under `%APPDATA%\Claude`. The application copies
encrypted session state but never decrypts cookie values.

## Account flows

### Add

1. Preserve the currently recognized profile when one exists.
2. prepare Claude Desktop for a new sign-in.
3. Wait for both login evidence and stable encrypted account state.
4. Close Claude Desktop, verify persistence, and create the profile snapshot.
5. Reopen Claude Desktop with the newly saved account and refresh the tray.

Cancellation restores the prior recognized profile. An interrupted addition is
recovered from the pending workflow state on the next tray start.

### Switch

1. Refuse a damaged or incomplete destination profile.
2. Close Claude Desktop using the graceful-to-forced process sequence.
3. Refresh the recognized outgoing profile when safe.
4. Restore the destination snapshot atomically.
5. Reopen Claude Desktop and report it active only after signed-in state is
   verified.

Claude Desktop hides in the system tray when its window closes, so the close
sequence normally ends with `taskkill` and Claude's own quit cleanup does not
run. `WM_ENDSESSION` is no way around this: Electron ends the process about
40 ms after that message, before an asynchronous cleanup can finish, so the
switcher does not send it.

### Delete

When Claude Desktop is open, the verified active profile is protected. When it
is closed, deleting the tracked profile first restores the best remaining
profile. Live Claude data is cleared only when the final profile is removed.

### Backup and import

Exports include all complete profiles and tracking metadata. Password-protected
backups use a user password; local backups use Windows account protection.
Import detects the backup type, validates its contents, rejects duplicate
identities, and replaces the store through a recoverable staged operation.

## Stability rules

- Never overwrite an unrecognized live session.
- Never log cookies, tokens, or backup contents.
- Never mark an account active only from a stale tracking file.
- Keep one tray process per Windows user.
- Use atomic or recoverable profile-store mutations.
- Preserve non-account Claude settings while swapping account state.
- Pause background profile reads (menu refresh, email discovery) while a
  switch, an account addition or any operation lock is active: Windows refuses
  to rename a folder while a file inside it is open.
- Retry profile-store renames for a few seconds when Windows reports an access,
  sharing or lock violation, and keep the original in place if it persists.
- Share one `EnumWindows` callback: Go never frees Windows callbacks and stops
  the process after 2000 of them.
- Show progress in a small card that never takes focus, clicks or the taskbar.
- Keep cobra's console guard disabled, so a tray started by Explorer opens.

`cmd/tray_windows.go` intentionally remains a single file for the first stable
release. A later cleanup may move related functions into files in the same
package without changing their signatures, ordering, or behavior.
