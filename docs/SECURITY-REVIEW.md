# gitsi security review guide

For security teams evaluating gitsi on a managed machine. It describes what gitsi is, what it does on the machine that runs it as
an **edge**, and what it does NOT do, so a reviewer can decide whether to allow it and how (for example an exception by hash or
path, signing with an organization certificate, or a rejection). To report a vulnerability, see [`SECURITY.md`](../SECURITY.md).

## 1. What it is

A command-line tool written in Go (a single `gitsi.exe`, with no third-party dependencies: the module has no `require`). It is used
to work between two machines of the same user: the managed machine (the "edge") and another machine the user controls (the
"hub"). It moves Git repos, files and text between them, opens local tunnels to services on the hub and runs commands there.

Design principle: **the edge only makes outbound connections**. Nothing is exposed from the edge.

## 2. What it does on the edge

### Network
- **Outbound** SSH connections to the hub, always with the system's `ssh.exe` and `scp.exe` (`C:\Windows\System32\OpenSSH`).
  gitsi does not implement SSH itself.
- A TCP probe of the hub's SSH port (`gitsi check` diagnosis) and a UDP `Dial` that sends no data, to learn the local IP that
  reaches the hub.
- Listeners: only on `127.0.0.1`, for the services' `-L` tunnels (disabled by default) and a test tunnel in `gitsi check`.
- It does **not** run `sshd`, does **not** use `ssh -R` or reverse forwarding, and does **not** open ports reachable from the
  network. `gitsi check` verifies it and warns if anything of gitsi listened outside loopback.
- `gitsi watch` (optional, opened by the user in a terminal or in the tray) keeps an outbound SSH session over which the hub sends
  **notices** (new version, item available, job finished). It never receives commands through it. With the `clip` policy on, the
  same session also carries **clipboard items** both ways (text, image or files the user copied and chose to share: the hotkey, or
  every copy with `clip.auto`).

### Processes it starts
| Process | What for | When |
|---|---|---|
| `ssh.exe`, `scp.exe` | all traffic to the hub | always |
| `git.exe` | repos (`share`, `get`, `sync`, `publish`; `run -repo` builds a snapshot of the folder with a temporary index and pushes it to the hub) | when using repos |
| `ssh-keygen.exe`, `ssh-keyscan.exe` | create its own ed25519 key and read the hub's fingerprint | `gitsi setup edge`, `gitsi setup key` |
| `ipconfig /all`, `netstat -ano -p tcp` | diagnosis (detect a VPN, confirm there are no listeners) | `gitsi check` |
| `powershell.exe -NoProfile -NonInteractive -STA` | read or write the clipboard | `gitsi send` / `gitsi take` from or to the clipboard |
| `powershell.exe -NoProfile -NonInteractive` | add the docker client's folder to the account's PATH (registry `HKCU\Environment`) | `gitsi docker` |
| `docker.exe` (the official, signed Docker client) | drive the hub's Docker over SSH | `gitsi docker` and when the user runs `docker` |
| `gitsi.exe` itself | `watch`'s reactions (update, pull an item, leave a report) | only when the user enabled them |
| `git.exe fetch` | the `watch.auto_fetch` reaction: update the remote branches of the repo where watch runs when it changes on the hub | only when the user enabled it |
| `gitsi.exe watch -tray` | the same binary, detached (no console) so it stays in the tray | only when the user runs `gitsi watch -tray` |
| `notepad.exe` | open watch's log from the tray menu | only when the user picks it |

Every helper process whose output gitsi reads is started with `CREATE_NO_WINDOW`, so no console window flashes on the desktop.

### Files it writes
- `%USERPROFILE%\.gitsi\`: `config.json` (configuration), `hub-layout.json` (cache), `watch-state.json`, `audit.log` (rotates past
  1 MiB).
- `%USERPROFILE%\.ssh\config` (adds/replaces **only** the `gitsi-hub` block), `known_hosts` (gitsi pins the hub's fingerprint when
  the user gives it), `gitsi_ed25519` and `.pub` (its own key).
- Received items: `%USERPROFILE%\Downloads\gitsi` (or the folder the user gives). Files and folders taken to the clipboard:
  `%LOCALAPPDATA%\gitsi\clipboard\<id>\` (deleted after 7 days).
- With the shared clipboard: received files and images in `%LOCALAPPDATA%\gitsi\clipboard\<id>\` (deleted after 7 days), and in
  the tray, watch's output in `%LOCALAPPDATA%\gitsi\watch.log` (rotates past 1 MiB).
- Docker client (when `gitsi docker` is used): `%LOCALAPPDATA%\gitsi\docker\docker-cli\docker.exe`, plugins in
  `%USERPROFILE%\.docker\cli-plugins`, the `gitsi-hub` context in `%USERPROFILE%\.docker`, and that folder added to the account's
  PATH.
- Transfer temporaries: `%TEMP%\gitsi-*` and `.gitsi-*.part` in the destination folder; deleted at the end (abandoned partial
  downloads, after 7 days).
- **Self-update** (`gitsi update`, or `gitsi watch` when the user enabled `watch.auto_update`): downloads the binary published by
  the hub, **verifies its SHA256 against the one the hub reports** and replaces its own executable (renames the current one to
  `.old` and puts the new one in place). Security tools often watch this pattern, which is why it is documented here.

## 3. What it does NOT do on the edge
- It does not scan or read the user's files on its own: it only sends what the user explicitly tells it to.
- It does not persist: no services, scheduled tasks, Run keys or autostart on the edge (`watch -autostart` is refused there).
  `gitsi watch` is started by hand, in a terminal (closes with Ctrl+C) or in the tray (a visible icon; quits from its menu).
- It does not monitor the clipboard unless the user enabled the `clip` policy and runs `gitsi watch`; then it is notified of
  clipboard changes, and reads the clipboard only on the hotkey, or on every copy when the user turned on `clip.auto`. Auto mode
  skips copies marked private by password managers. Every item sent or received is recorded in `audit.log` (never its content).
- It does not elevate privileges: it runs as the user and does not need more.
- It uses no hiding techniques: no code injection, no obfuscation or packing. It is built **with** its symbol table (and without
  the build machine's paths), which makes it easier to inspect.
- It does not contact the Internet or cloud services: it uses no HTTP; the only destination is the hub address the user
  configured.

### But the binary CONTAINS code that does not run on the edge (transparency)
There is a single `gitsi.exe` for both roles, so the file includes, besides the edge's code, the code of `gitsi setup hub`, which
**only runs on the hub and requires an Administrator** (the command checks it and refuses otherwise). That code, which static
analysis of the file will see, can:
- install/start the `sshd` service and set it to start automatically (`Set-Service`, `Start-Service`, `Add-WindowsCapability`);
- write to the registry at `HKLM:\SOFTWARE\OpenSSH` (the SSH server's default shell);
- edit `sshd_config` (hardening it: no passwords) and `authorized_keys` / `administrators_authorized_keys`;
- create/replace the `gitsi-ssh` firewall rule (the edge's IP only) and adjust permissions with `icacls`.

Windows API calls, all to the system's own DLLs through Go's standard library (no own or third-party DLLs):
- `shell32!IsUserAnAdmin`, to know whether the process is an administrator;
- for the shared clipboard and the tray (only with the `clip` policy on, or `watch -tray`): `RegisterHotKey` (the user's send
  hotkey; a global hotkey, not a keyboard hook: it sees no other keys), `AddClipboardFormatListener`,
  `GetClipboardSequenceNumber`, `OpenClipboard`/`GetClipboardData`/`SetClipboardData` (text), `Shell_NotifyIconW` and a popup
  menu, a hidden window and its message loop, and `CreateMutexW` (one watch per user);
- on the hub only, `RegSetValueExW` on `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` (value `gitsi-watch`), when the user
  runs `gitsi watch -autostart on`.

## 4. Verification (check it yourself)
- **Full source code** in the project repository.
- **Reproducible build**: `scripts/build.ps1` (Go version in `go.mod`), with `-trimpath` and the commit's date. The binary carries
  build information readable with `go version -m gitsi.exe` (git revision and date) and with `gitsi version`. Each GitHub release
  publishes `SHA256SUMS.txt`.
- **No external dependencies**: `go.mod` declares no third-party module; everything is the Go standard library.
- **Opt-in policy** (section 5): sensitive features come disabled.

## 5. Feature policy (opt-in)
**Disabled** by default: `send` (send to the hub), `take` (bring from the hub), `exec` (remote execution and Docker) and `services`
(tunnels). Only Git works from the start. They are enabled one by one, explicitly (`gitsi policy set <feature> on`, or answering "y"
when gitsi asks at the terminal on first use), and are **audited** in `audit.log` (action, name, size; never content). `gitsi
watch`'s automatic reactions come disabled and are enabled separately.

## 6. If a security tool flags it
Data that would help understand why (usually only visible in the security console):
1. Name and type of the detection (machine learning, behavior, hash reputation, IOC).
2. Severity, associated tactic and technique, and the action taken (quarantine / removal).
3. Path and hash of the affected file, and **which process wrote or ran it** (e.g. `scp.exe` during `gitsi update`).
4. When: on download, on execution or on rename (self-update renames the executable in use).

Note: some endpoint protection products quarantine Go binaries built without a symbol table (`-s -w`). That is why the build keeps
the symbols.
