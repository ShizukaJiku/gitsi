# Changelog

All notable changes to gitsi are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [semantic versioning](https://semver.org/spec/v2.0.0.html).

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

[1.1.0]: https://github.com/ShizukaJiku/gitsi/releases/tag/v1.1.0
[1.0.0]: https://github.com/ShizukaJiku/gitsi/releases/tag/v1.0.0
