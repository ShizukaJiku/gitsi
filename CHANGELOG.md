# Changelog

All notable changes to gitsi are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [semantic versioning](https://semver.org/spec/v2.0.0.html).

## [1.3.1] - 2026-11-25

### Fixed
- The shared clipboard did not work for an edge whose `watch` connected before `clip` was enabled on the hub: the hub decided it
  once per session. The hub now reads its clip policy live, so turning it on or off applies to the sessions already open.
- A `watch` run with another profile (for example the end-to-end tests) was refused while the user's own `watch` ran: the
  single-instance lock is now per profile.

## [1.3.0] - 2026-11-25

### Added
- Shared clipboard between hub and edge (`docs/CLIP.md`): with the new `clip` policy on both machines and `gitsi watch` running on
  both, Ctrl+Shift+Alt+C sends what is copied and the other machine pastes it with Ctrl+V. Text, images and files copied in
  Explorer travel inside the watch session to a single slot on the hub that expires after 10 minutes. `clip.auto` sends every
  copy (skipping copies marked private by password managers); `clip.hotkey` and `clip.max_size_mb` tune it.
- `gitsi watch -tray` keeps watch in the tray, with a menu (send now, send every copy, log, quit) and notifications; its output goes
  to `%LOCALAPPDATA%\gitsi\watch.log`. Only one watch runs per user and machine.
- `gitsi watch` on the hub, to share its clipboard, and `gitsi watch -autostart on|off` (hub only) to start it on logon.

### Changed
- The edge's principle "nothing in the background" is now "nothing hidden or unattended": long-running things are started by hand
  and stay visible, in a terminal or in the tray; nothing starts on its own.
- The watch session carries frames both ways (clipboard items from the edge); protocol frames `KindClip` and `KindClipData` and
  the `clip` capability are new.

### Fixed
- Helper processes (PowerShell, git, ssh) no longer flash console windows when gitsi, or its tests, run from a process without a
  console, and background jobs started through WMI start hidden.

## [1.2.0] - 2026-11-23

### Added
- `gitsi publish` shows how many commits the branch and the PR have, whose they are and how much they change. It finds the branch the
  PR goes to; `-base` chooses another.
- `gitsi publish` warns when the PR will conflict with its base or the bridge has newer commits, and stops when there is nothing to
  publish (for example, a branch whose PR was merged).
- `gitsi sync` on the edge, when both sides have new commits, shows the hub's and asks whether to replace them; the replaced version
  stays recoverable on the bridge.
- `gitsi sync` carries an amended or rebased edge branch to the hub, and on the hub takes the edge's version when nothing of the
  hub's own is lost. It warns about uncommitted changes.

### Changed
- `gitsi publish` pushes the branch as one commit authored by the edge's Git user, replacing the branch on origin with
  `--force-with-lease`. The local branch and the bridge do not change. It stops when someone's commits on origin would be lost, and
  never rewrites shared branches.
- `gitsi repo status` compares a published branch with what was published.

### Fixed
- `gitsi publish` and `gitsi sync` with a detached HEAD (or during a rebase) stop instead of using `main`.
- `gitsi sync` no longer reports uncommitted changes that block it as diverging branches.

## [1.1.0] - 2026-11-21

### Added
- "Did you mean" for typos: a mistyped command or subcommand ("stauts", "repo lst") gets the closest names in one line instead
  of the whole help, and a mistyped option ("lst" for take) gets the closest options and the hint to run `gitsi take -h`
  instead of a usage dump.

### Fixed
- A mistyped option exits with code 2 (wrong usage), as documented, and its message is printed once.
- `gitsi job logs | stop | get` reject an argument that is neither a list number nor a job id at once, without connecting.
- A hub whose profile folder has accented letters (`C:\Users\José`) now works: its default layout uses the folder's 8.3 short
  name. When that is not possible (an apostrophe, or 8.3 names disabled), `gitsi doctor` on the hub reports it with the fix
  (`gitsi config set home ...`) and the edge's error says the same.
- `gitsi update` downloads from a hub path with spaces also with OpenSSH clients older than 9.0 (legacy scp protocol).
- An unreachable hub is reported as a network failure (exit code 11, with its action) in about 10 s, instead of "is gitsi
  published on the hub?" after 20 s.
- `gitsi help <command>` shows the command's help instead of running it with "help" as an argument. Help (`-h`, `--help`) no
  longer needs the feature enabled or a configured hub, never connects (`gitsi share -h` asked the hub first), and exits 0.
- Each option's default appears once in the help (`take -dir` printed it twice, the second time escaped).
- `gitsi repo get` on the hub uses the hub's layout as the default `-repos-dir` in every case.
- `gitsi take` reports a failure to finish writing a download (e.g. a full disk) as such, not as a hash mismatch.

### Security
- Names (user, host, alias, repo, remote) can no longer start with a dash, and ssh/scp get `--` before the target, so no value
  can be read as an ssh option.
- The hub values of a hand-edited `config.json` are checked again before connecting; an invalid one stops hub commands with
  the fix (`gitsi config set <key> <value>`) instead of reaching ssh.

## [1.0.0] - 2026-11-20

First public release.

### Added
- **Pairing and provisioning**: `gitsi setup hub` installs and hardens OpenSSH Server (key only, the given user only, local
  forwarding only), limits the firewall to the edge and validates with `sshd -t`, restoring the previous configuration on failure;
  `gitsi setup edge` creates the key, writes the `~/.ssh/config` block and pins the hub's fingerprint; `gitsi setup key` rotates
  the edge's key without cutting access.
- **Repo bridge**: `share`, `repo get`, `sync` (fast-forward only), `publish` (to origin, never to the bridge), `repo list` and
  `repo status`.
- **Mailbox**: `send` and `take` on both machines for files, folders, text and clipboard content, with end-to-end SHA256,
  resumable transfers, numbered lists and clipboard integration.
- **Services**: `svc` opens `-L` tunnels in the foreground, including one per port published by the hub's containers
  (`svc up -docker`).
- **Remote execution**: `run` with live output and the remote exit code, `run -repo` over the folder as it is, `-get` to bring
  results back, and background jobs (`job start | ls | logs | stop | get`) that survive the SSH session.
- **Hub notices**: `gitsi watch` on the edge and `gitsi edges` on the hub, with opt-in reactions (`auto_update`, `auto_take`,
  `report`, `auto_fetch`).
- **Docker**: `gitsi docker` sets up the official Docker client on the edge against the hub's engine over SSH.
- **Operations**: `status`, `check [--active]`, `update [-check | -publish]`, `policy`, `config`, `clean [-dry-run]`, tab
  completion for PowerShell, global `-v`, `-q` and `--json`, and stable exit codes per error category.
- Release archives for Windows amd64 and arm64 with SHA256 checksums.

[1.3.1]: https://github.com/ShizukaJiku/gitsi/releases/tag/v1.3.1
[1.3.0]: https://github.com/ShizukaJiku/gitsi/releases/tag/v1.3.0
[1.2.0]: https://github.com/ShizukaJiku/gitsi/releases/tag/v1.2.0
[1.1.0]: https://github.com/ShizukaJiku/gitsi/releases/tag/v1.1.0
[1.0.0]: https://github.com/ShizukaJiku/gitsi/releases/tag/v1.0.0
