<p align="center">
  <a href="https://github.com/TinoA/claude-desktop-switcher">
    <img src="cmd/assets/windows-claude-swap-icon-v2.png" alt="Claude Desktop Switcher — Claude account switching for Windows" width="190">
  </a>
</p>

<h1 align="center">Claude Desktop Switcher</h1>

<p align="center">
  Switch between Claude Desktop accounts from the Windows system tray.<br>
  No repeated sign-outs. No command line required.
</p>

<p align="center">
  <a href="https://github.com/TinoA/claude-desktop-switcher/releases/latest/download/Claude-Desktop-Switcher-Setup-amd64.exe">
    <img src="https://img.shields.io/badge/Download_for_Windows_x64-2F2D2A?style=for-the-badge&logo=windows11&logoColor=white" alt="Download for Windows x64">
  </a>
</p>

<p align="center">
  <a href="https://github.com/TinoA/claude-desktop-switcher/releases/latest">All downloads</a>
  ·
  <a href="https://github.com/TinoA/claude-desktop-switcher/releases/latest/download/Claude-Desktop-Switcher-Setup-arm64.exe">Windows ARM64</a>
  ·
  <a href="https://github.com/TinoA/claude-desktop-switcher/issues">Report a problem</a>
</p>

<p align="center">
  <img src="https://img.shields.io/github/v/release/TinoA/claude-desktop-switcher?label=latest" alt="Latest release">
  <img src="https://img.shields.io/badge/Windows-10%20%7C%2011-0078D4?logo=windows11&logoColor=white" alt="Windows 10 and 11">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="MIT license"></a>
</p>

<p align="center">
  <img src="docs/assets/tray-menu-preview.png" alt="Claude Desktop Switcher account menu in the Windows system tray" width="900">
</p>

## Install in a minute

1. Download the **Windows x64 installer** using the button above. If your Windows device uses an ARM processor, choose **Windows ARM64** instead.
2. Open `Claude-Desktop-Switcher-Setup-amd64.exe`.
3. Complete the installer and launch **Claude Desktop Switcher**.
4. Find its icon in the Windows notification area. It may be inside the hidden-icons menu (`^`).

The app installs for your Windows user and normally does not require administrator access. The installer can start it with Windows and adds a standard uninstaller.

## What it does

- Keeps multiple Claude Desktop accounts available under names you recognize.
- Switches accounts from a small system-tray menu.
- Guides you through adding another account and saves it automatically after sign-in.
- Safely closes and reopens Claude Desktop when a switch requires it.
- Saves a renewed session when Claude Desktop closes normally.
- Imports and exports complete account backups.
- Lets you remove one saved account without affecting the others.
- Shows a tray notification when a newer release is available.

## Everyday use

### Switch accounts

Right-click the tray icon, open **Accounts**, and select the account you want. Claude Desktop Switcher prepares the saved session and opens Claude Desktop again with that account. A small progress card stays in the middle of the screen while it works.

Switching closes Claude Desktop. Claude Desktop keeps running in the system tray when its window closes and offers no way for another program to make it quit with its own cleanup, so Claude Code sessions running inside Claude Desktop are stopped. They can be continued after Claude Desktop reopens.

### Add an account

Choose **Add account...**, give the profile a recognizable name, and sign in through Claude Desktop. The new account is saved automatically and appears in the account list.

### Delete a saved account

Choose **Delete account...**, select a profile, and confirm. This removes only the Switcher's local copy. It does **not** delete the Anthropic account, and other saved accounts remain untouched.

### Back up your accounts

Open **Backup** and choose:

- **Password-protected...** for a portable encrypted backup protected by a password you choose.
- **Without password...** for a backup protected automatically with your current Windows account.

Choose **Import backup** to restore a file. The app detects the protection type automatically and asks for a password only when one is required.

For a complete backup, Claude Desktop may briefly close while the active profile is refreshed. If the session cannot be verified safely, the backup stops without replacing saved account data.

## Your sessions and chats

Claude Desktop Switcher stores the local Claude Desktop data needed to reopen each account, including encrypted cookies and browser storage. On Windows, cookie values are copied in their encrypted form and are never decrypted, displayed, or written to logs.

Chats are not copied by this app. Conversation history belongs to each Claude account and is loaded by Claude Desktop from Anthropic when that account is active.

Claude or Anthropic may still request sign-in or device verification after a session expires, is revoked, or triggers a security check. No local application can guarantee that an external service will never request verification again.

## Backups and reinstalling

Uninstalling Claude Desktop Switcher keeps your saved profiles by default, so reinstalling the app does not normally remove your accounts. Profiles are stored in:

```text
%USERPROFILE%\.claude-swap\profiles\<name>\
```

To start completely fresh, uninstall the app and then manually remove `%USERPROFILE%\.claude-swap`.

> [!WARNING]
> Never share your `.claude-swap` folder or backup files publicly. They contain private Claude Desktop session data.

Backups protected by your Windows account are intended for the same user and computer. Password-protected backups are portable, but Claude may still require a new sign-in on another device because Chromium session data can depend on Windows security keys and device trust.

## If Claude Desktop does not open

1. Make sure Claude Desktop is installed and up to date.
2. Close any unresponsive Claude Desktop window or process.
3. Use the tray menu to switch to a known working account.
4. If Claude requests verification, complete it and close Claude Desktop normally so the renewed session can be saved.

The app detects common Claude Desktop installations, including standard, Squirrel, MSIX, and supported portable layouts.

---

## Technical details

Claude Desktop Switcher is a Go application with a native Windows tray interface. Account switching works by stopping Claude Desktop safely, checkpointing the current local session, restoring the selected profile, and starting Claude Desktop again.

Each profile can include:

- Claude Desktop's Chromium Cookies database, with encrypted values kept intact.
- Local Storage and its device-trust state.
- IndexedDB and Session Storage.
- Account identity hashes and profile metadata used for safer matching.

The Windows tray never decrypts Chromium cookie values. Profiles are isolated under `.claude-swap`, and tests use temporary data rather than a live Claude Desktop profile. The legacy macOS CLI account-label helper can use the macOS keychain to query account metadata; it is not used by the Windows tray.

### Build from source

Requirements: Windows, Go as declared in [`go.mod`](go.mod), and Visual C++
Build Tools for the native launcher.

```powershell
.\scripts\project.ps1 -Task Validate
.\scripts\project.ps1 -Task Build -Arch amd64 -Version dev
```

The binary is created under `installer\dist\windows_amd64`. Use `-Arch arm64`
for Windows on ARM. Inno Setup is also required when using `-Task Installer` or
`-Task All`.

A locally compiled executable is separate from the installed release. Building
it does not uninstall Claude Desktop or remove saved profiles.

### Releases and updates

GitHub Actions verifies module metadata, runs `go vet`, race-enabled tests, formatting/import linting, and reachable-vulnerability scanning before publishing versioned Windows installers for `amd64` and `arm64`, CLI archives, and checksums. The tray checks this repository for new releases and links to the latest download; updates are not installed silently.

The Windows build is graphical: a double-click, a desktop shortcut, or the installer's Start and Startup shortcuts open the tray without showing a console window. CLI archives remain console applications.

This Windows-focused project is maintained at [`TinoA/claude-desktop-switcher`](https://github.com/TinoA/claude-desktop-switcher) and is based on [`FranCalveyra/claude-desktop-swap`](https://github.com/FranCalveyra/claude-desktop-swap).

## License

Released under the [MIT License](LICENSE).

## Developer documentation

The technical design is described in [Architecture](docs/architecture.md).
Contributors should also read [Contributing](CONTRIBUTING.md), the
[regression checklist](docs/regression-review.md), and the
[visual-assets guide](docs/assets.md).
