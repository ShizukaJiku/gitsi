# Changelog

All notable changes to gitsi are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [semantic versioning](https://semver.org/spec/v2.0.0.html).

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

[1.0.0]: https://github.com/ShizukaJiku/gitsi/releases/tag/v1.0.0
