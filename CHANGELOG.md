# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.2.2](https://github.com/OpenScz/claude-desktop-switcher/compare/v1.2.1...v1.2.2) (2026-10-26)


### Bug Fixes

* scope all progress overlays to the Claude window ([6dce87c](https://github.com/OpenScz/claude-desktop-switcher/commit/6dce87c9a8009f62fca3570f06f76dbc94225abe))

## [1.2.1](https://github.com/OpenScz/claude-desktop-switcher/compare/v1.2.0...v1.2.1) (2026-10-26)


### Bug Fixes

* detect current Windows Claude profile and login log paths ([#14](https://github.com/OpenScz/claude-desktop-switcher/issues/14)) ([543acf5](https://github.com/OpenScz/claude-desktop-switcher/commit/543acf506c696bce28eb0a08e8b093817648cb20))
* make account switching on Windows more reliable ([#17](https://github.com/OpenScz/claude-desktop-switcher/issues/17)) ([ed99588](https://github.com/OpenScz/claude-desktop-switcher/commit/ed99588dd69efb967d8cc639813ec6e2c1a78837))

## [1.2.0](https://github.com/TinoA/claude-desktop-switcher/compare/v1.1.0...v1.2.0) (2026-07-25)


### Features

* rename product to Claude Desktop Switcher ([7373970](https://github.com/TinoA/claude-desktop-switcher/commit/7373970f0ab4850acaae52003702bdecc1c8876d))

## [1.1.0](https://github.com/TinoA/claude-desktop-swap/compare/v1.0.3...v1.1.0) (2026-07-25)


### Features

* improve Windows tray and documentation ([5a563fe](https://github.com/TinoA/claude-desktop-swap/commit/5a563fe878213a82e1772a193472c6850597006e))
* prepare Windows Claude Swap release ([f980d2b](https://github.com/TinoA/claude-desktop-swap/commit/f980d2bc8b0f40ced354af4d1cd42f01a9c2bccc))

## [1.0.3](https://github.com/TinoA/claude-desktop-swap/compare/v1.0.2...v1.0.3) (2026-07-14)


### Bug Fixes

* save renewed Claude sessions automatically ([96f9783](https://github.com/TinoA/claude-desktop-swap/commit/96f97831579b24a2cbe8397197bf11771b798e98))

## [1.0.2](https://github.com/TinoA/claude-desktop-swap/compare/v1.0.1...v1.0.2) (2026-07-13)


### Bug Fixes

* check fork releases for updates ([c35362f](https://github.com/TinoA/claude-desktop-swap/commit/c35362fbe16d55264de4a7c698572487b04bb9ff))

## [1.0.1](https://github.com/TinoA/claude-desktop-swap/compare/v1.0.0...v1.0.1) (2026-07-13)


### Bug Fixes

* publish Windows installers ([b3a4665](https://github.com/TinoA/claude-desktop-swap/commit/b3a4665df62f270dccda42ced98f579a045723fa))

## 1.0.0 (2026-07-13)


### Features

* add interactive profile picker and account info display ([3813d97](https://github.com/TinoA/claude-desktop-swap/commit/3813d974f079f4a3a342b01b037b3879260915b9))
* add Windows Claude Swap tray and release automation ([1331cd8](https://github.com/TinoA/claude-desktop-swap/commit/1331cd83e73435b7cfb3fc59384574017dcdff80))
* **cli:** initial implementation of claude-swap ([bff1ae5](https://github.com/TinoA/claude-desktop-swap/commit/bff1ae57739f0f881e23fbc9f0a3498ae62a743f))
* make save automatic by stopping and relaunching Claude Desktop ([09ef9aa](https://github.com/TinoA/claude-desktop-swap/commit/09ef9aab99b2aa2486abe2f74eab3352fe7e2a0b))
* rename to claude-desktop-swap, add interactive 'add' command, auto-prime on save ([8d68249](https://github.com/TinoA/claude-desktop-swap/commit/8d68249940134bf7e975ec6e1d70e23f4e921d54))
* show live account email and plan in list and picker ([c928767](https://github.com/TinoA/claude-desktop-swap/commit/c92876701bf0defbcd0d1c2ccff11eed5c275efd))


### Bug Fixes

* **cd:** remove unsupported changelog path field ([c2cb3db](https://github.com/TinoA/claude-desktop-swap/commit/c2cb3db971363f75f54be1b781456038722277b8))
* checkpointed profile switching and cookie-only v2 profiles ([#3](https://github.com/TinoA/claude-desktop-swap/issues/3)) ([abda7b1](https://github.com/TinoA/claude-desktop-swap/commit/abda7b1e73e17877753890e24d46ce460bebca32))
* decrypt real sessionKey and read correct account API fields ([c52aaa7](https://github.com/TinoA/claude-desktop-swap/commit/c52aaa7cc152e5ab92be9d4ca253c54eec5d1cf4))
* preserve device trust across account swaps ([5cdd6c4](https://github.com/TinoA/claude-desktop-swap/commit/5cdd6c4cbe16e0537e3ac679c3c1f18985e9cd23))
* **profile:** clear Cookies-journal on restore ([3a29f63](https://github.com/TinoA/claude-desktop-swap/commit/3a29f6395895dd9a12ea0084443dd6f5754820d4)), closes [#1](https://github.com/TinoA/claude-desktop-swap/issues/1)
* **profile:** wipe IndexedDB and Session Storage on restore ([1238355](https://github.com/TinoA/claude-desktop-swap/commit/1238355292e8b95f7c3b9af206d88195b6b3fd10))

## [Unreleased]

### Fixed
- Preserve per-account Local Storage, IndexedDB, Session Storage, and Cloudflare security cookies during switches to avoid unnecessary device or human verification prompts.
- Roll back every replaced account-state directory when a restore fails, including paths that did not exist before the attempted switch.
- Refresh the active profile before export and refuse incomplete legacy-profile backups; imports identify accounts that still need a one-time refresh.

## [0.3.1] - 2026-06-19

### Added
- Local profile health states (`usable`, `expired`, `missing`, and `unknown`) based on non-secret SQLite evidence.
- Cookie-only v2 profiles with integrity metadata, secure permissions, atomic generation recovery, and compatible v1 reads.

### Changed
- Switching now fully stops Claude and its helpers, checkpoints the outgoing profile before restoration, replaces live Cookies atomically, and advances tracking only after commit.
- `save --force` now refuses to snapshot an open database; all saves require Claude to be stopped.
- Local Storage, IndexedDB, and Session Storage are cleared as volatile caches instead of stored in profiles; global and machine data remain untouched.

### Fixed
- Refreshed outgoing cookies are no longer discarded during repeated account switches.
- Interrupted profile writes and live-cookie replacement retain a recoverable prior generation.

## [0.3.0]

### Added
- `add <name>` command — snapshots your current session, gives Claude Desktop a clean slate to log into a new account, then snapshots that new session. Removes the manual logout/login dance for adding accounts.

### Changed
- Binary renamed from `claude-swap` to `claude-desktop-swap` to avoid collision with an unrelated tool
- `save` now primes the on-disk state right after capturing the profile (sets it as active and wipes stale per-account caches), so the next switch always lands cleanly without needing a manual `use` first

### Fixed
- Profile snapshots now include `IndexedDB`, `Session Storage`, and `ant-did` — stale per-account state from a previous profile was invalidating the restored session and forcing a re-login

### Added
- `save` command — snapshot the current Claude Desktop session as a named profile
- `use` command — switch to a saved profile (kills and restarts Claude Desktop)
- `list` command — list saved profiles with creation date and last used timestamp
- `delete` command — remove a saved profile
- `status` command — show the currently active profile
- Full session isolation: both cookies (SQLite) and local storage (LevelDB) are swapped on profile switch
- macOS platform implementation with graceful SIGTERM → SIGKILL shutdown sequence
- Platform abstraction layer for future Windows support
- CI pipeline (lint + tests on Ubuntu and macOS) via GitHub Actions
- CD pipeline (GoReleaser) triggered on SemVer tags — builds macOS binaries (amd64 + arm64)
- `CODEOWNERS` and branch protection documentation for maintainer-approval workflow
- `.gitmessage` commit template following Conventional Commits
