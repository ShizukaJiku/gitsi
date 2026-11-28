# gitsi

[![CI](https://github.com/ShizukaJiku/gitsi/actions/workflows/ci.yml/badge.svg)](https://github.com/ShizukaJiku/gitsi/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A single Go binary that connects two machines with a deliberate asymmetry: a **hub** that *serves* and an **edge** that *only
consumes*. Git repos, files, clipboard, services and remote execution, always started by the edge, over plain SSH.

```
   EDGE (only consumes)                           HUB (serves)
   gitsi / ssh / git   ---- SSH, started by the edge ---->   sshd (key only) + gitsi
   127.0.0.1:5432      <---- local tunnel (-L) -----------   services (Docker, databases, ...)
   gitsi take          <---- the edge pulls it -----------   mailbox (outbox)
```

## Why
- **Only consume, never expose.** The edge starts every connection and opens nothing: no reverse shell, no reverse proxy, no
  `ssh -R`, no server on the edge. At most it listens on `127.0.0.1`.
- **The hub never initiates toward the edge.** Whatever goes from the hub to the edge, the edge *pulls* (mailbox).
- **Explicit opt-in.** Mailbox, services and remote execution come disabled; they are enabled with `gitsi policy set <feature> on`
  and audited.
- **Standard pieces.** It uses the system's `ssh`, `scp` and `git`; no agents or daemons on the edge, no third-party Go modules.
- **Content-agnostic, with integrity.** It does not judge what you share; it verifies SHA256 end to end and resumes transfers.

Details in [`docs/PHILOSOPHY.md`](docs/PHILOSOPHY.md).

## Vocabulary
| Term | Meaning |
|---|---|
| **hub** | The machine that serves: it keeps the repo *bridge*, the mailbox and the services. |
| **edge** | The machine that only consumes: it starts every connection and exposes nothing. |
| **bridge** | One bare repo per project on the hub; on the edge it is the `hub` remote. |
| **origin** | The usual target repo (e.g. GitHub). `publish` pushes to `origin`, never to the bridge. |

## Requirements
- Windows 10/11 on both machines, with Windows PowerShell 5.1 (included) and the OpenSSH client (included).
- The hub needs administrator rights once, for `gitsi setup hub` (it installs and hardens OpenSSH Server).
- [Git for Windows](https://git-scm.com/download/win) on both machines for the repo features.
- Optional: Docker Desktop on the hub for `gitsi docker` and `gitsi svc up -docker`.
- The edge must reach the hub's SSH port (same LAN, VPN or any routed network).

## Install
Download `gitsi-<version>-windows-amd64.zip` (or `arm64`) from the [releases](https://github.com/ShizukaJiku/gitsi/releases),
check it against `SHA256SUMS.txt`, and put `gitsi.exe` in a folder on your PATH:

```powershell
Get-FileHash .\gitsi-1.2.0-windows-amd64.zip -Algorithm SHA256     # compare with SHA256SUMS.txt
Expand-Archive .\gitsi-1.2.0-windows-amd64.zip $env:LOCALAPPDATA\gitsi\bin
[Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$env:LOCALAPPDATA\gitsi\bin", "User")
gitsi version
```

Only the hub needs a download: once paired, the edge gets the exact same binary from the hub with `gitsi update` (copy it by any
means for the very first run). To build from source, see [Development](#development).

## Quick start
The pairing ends with the exact command the hub prints, including the **hub's fingerprint** to verify and pin. The hub decides its
own folder layout (repos, mailbox, jobs, binary) and announces it: the edge never configures hub paths.

```powershell
# 1. EDGE: creates the key ~/.ssh/gitsi_ed25519 and the ~/.ssh/config block. Until the hub is set up the login fails;
#    when the hub answers but does not know the key yet, it prints the exact 'gitsi setup hub' command to run there.
gitsi setup edge -host <hub-ip> -user <user-on-the-hub>

# 2. HUB (as administrator): installs and hardens OpenSSH (key only, local forwarding only), firewall limited to the edge.
#    The public key is the edge's ~/.ssh/gitsi_ed25519.pub (as a file or as text with -public-key "ssh-ed25519 ...").
gitsi setup hub -edge-ip <edge-ip> -public-key-file .\gitsi_ed25519.pub -user <user-on-the-hub> [-dry-run]
#    ...creates its folders, publishes itself in its home and prints:  gitsi setup edge -host <hub-ip> -user <user> -hostkey SHA256:...

# 3. EDGE: verifies and pins the hub's fingerprint, tests the login and discovers the hub's layout
gitsi setup edge -host <hub-ip> -user <user-on-the-hub> -hostkey SHA256:...
gitsi check                                   # end-to-end report
```

Without `-hostkey` the fingerprint is shown so you can compare it; with `-hostkey`, a different fingerprint aborts **before**
anything is saved. `gitsi` with no arguments says whether the machine is an **EDGE** or a **HUB** and shows only what applies to
it. The role is saved in `~/.gitsi/config.json` during setup; it is never detected.

## Tab completion
```powershell
gitsi setup completion      # once per machine: Tab completes commands, options, services, repos and config keys
```
It goes into the PowerShell profile (Windows PowerShell and PowerShell 7) and takes effect in a new console; `gitsi setup
completion -off` removes it. In Windows PowerShell 5.1 options appear with Tab after a space (`gitsi take <Tab>`); PowerShell 7
also completes `gitsi take -l<Tab>`. If the execution policy does not allow loading the profile, gitsi says so; then, per console:
`gitsi setup completion -print | Out-String | Invoke-Expression`.

## Commands at a glance
| Group | Purpose | Commands |
|---|---|---|
| `setup` | prepare the machine and set its role | `edge`, `hub`, `key` (change the edge's key without cutting access), `completion` (Tab in PowerShell) |
| `repo` | Git repos through the bridge | `share` `get` `list` `sync` `status` `publish` |
| `mirror` | origin branches the edge keeps mirrored on the bridge | `add` `ls` `rm` `sync` |
| `svc` | hub services through `-L` tunnels | `add` `rm` `ls` `up` |
| `job` | long jobs on the hub | `start` `ls` `logs` `stop` `get` |
| (top level) | | `send` `take` (mailbox, on both machines), `status`, `check` (`--active`), `config`, `run`, `update`, `policy`, `watch` (+ `edges` on the hub), `clean`, `docker` |

Daily shortcuts: `status`, `share`, `sync`, `publish`, `send`, `take`. `gitsi <group>` lists the group's commands and
`gitsi <command> -h` shows its options.

## Guide: repos through the bridge
```powershell
gitsi share                 # EDGE: copies the current repo to the hub (right branch and owner) and adds the 'hub' remote
gitsi repo get <name>       # HUB: working clone of the local bridge, in the current folder
gitsi sync                  # both: pulls and pushes the current branch (never a silent merge)
gitsi publish               # EDGE: pushes the branch to origin as one commit of yours (never to the bridge) and prints the PR URL
gitsi repo get <name>       # EDGE: brings a repo born on the hub (without origin: git remote add origin <url>)
gitsi repo list             # the bridge's repos
gitsi repo status [-fetch]  # what is left to send/receive against the bridge and origin
```
When the edge rewrote commits it already had (amend, rebase), `sync` replaces them on the bridge; `gitsi sync` on the hub then takes
the edge's version. If both sides have new commits, `sync` on the edge shows the hub's and asks whether to replace them. A replaced
version stays in a hidden ref of the bridge (the last one of each branch). On the hub, `sync` never discards commits that were not
on the bridge, nor uncommitted changes. The edge only sees what the hub has synced: sync there before switching machines.

`publish` squashes the branch and sets your Git user as the author, without touching your local branch or the bridge, and replaces
the branch on origin with `--force-with-lease`, showing the commits and their authors. It stops when origin has someone's commits
that would be lost, asks before replacing commits it did not publish, and never rewrites shared branches (the default one,
`develop`, `release/*`...). The PR goes to the shared branch yours comes from; `-base <branch>` chooses another.

The full cycle (edge -> hub -> origin) is in [`docs/WORKFLOW.md`](docs/WORKFLOW.md).

## Guide: following origin branches (`mirror`)
The hub does not reach the origin, so the edge keeps the branches the hub must follow (the team's `development`, `main`...)
mirrored on the bridge. Only the edge decides what is followed, and nothing is merged or touched: the mirror lives in hidden refs
of the bridge repo (`refs/gitsi/mirror/<branch>`), apart from the branches shared with `sync`.
```powershell
gitsi mirror add app https://github.com/acme/app.git -branch development   # EDGE: follow a branch (repeat -branch for more)
gitsi mirror add -list repos.txt     # EDGE: several, one "<name> <origin-url> <branch>..." per line; shows them and asks
gitsi mirror sync                    # EDGE: bring what changed now (only the branches whose tip moved travel)
gitsi mirror ls                      # EDGE: what it follows, the last check and the last error of each
gitsi mirror rm app [-branch dev]    # EDGE: stop following it (the bridge repo stays)
gitsi mirror ls                      # HUB: what each edge mirrors, its last cycle and errors; STALE when it stopped checking
```
The first `add` seeds the edge's copy from the bridge, so only what the bridge lacks comes from the origin. Toward the origin git
never asks anything: an expired credential is an error of that repo, and the others go on. Origin URLs are `https://`, `ssh://` or
`user@host:path`.

## Guide: mailbox for files, folders, text and images (opt-in)
The hub never pushes to the edge: everything goes through a mailbox on the hub and the edge starts it. Transfers resume and are
verified with SHA256. `send` and `take` are the same verbs on both machines; each knows which mailbox to use:

| | `gitsi send` | `gitsi take` |
|---|---|---|
| edge | to the hub's inbox | from the hub's outbox |
| hub | to the outbox (for the edge) | from the inbox (what the edge sent) |

Bare, on both: `send` sends what you copied (files or folders from Explorer, an image or a text) and `take` brings the latest item
to the clipboard, ready to paste. On the edge, the first time gitsi explains what it enables and asks whether to turn it on (y/N);
it is recorded in audit.log. The hub does not need it.
```powershell
gitsi send                                 # what you copied (Ctrl+C in Explorer, a screenshot, a text)
gitsi take                                 # the latest, to the clipboard: text as text; a file or folder to paste with Ctrl+V
gitsi send report.pdf folder\              # paths: files and folders (compressed tar); repeating the command resumes
gitsi send proj\ -exclude node_modules -exclude *.log   # skips entries by name when packing folders (it does not read content)
gitsi send -text "message"                 # or: type notes.txt | gitsi send -text - -name notes.txt
gitsi take -last                           # the latest, to Downloads\gitsi
gitsi take -list                           # [1] report.pdf  [2] meeting notes ...
gitsi take 2                               # that list number (or its id)
gitsi take -dir D:\in -keep 1 2            # those, to another folder, keeping them in the mailbox; never overwrites
gitsi take -show 1                         # view a text without taking it out of the mailbox
gitsi take -rm 2 3                         # discard without bringing them
```

## Guide: hub services through local tunnels (opt-in)
The edge sees the service on `127.0.0.1`; it never exposes anything.
```powershell
gitsi policy set services on
gitsi svc add postgres                     # predefined port 5432 (also redis, mysql, mongo, ...); others: gitsi svc add api 3000 -local 13000
gitsi svc ls                               # catalog, local tunnel and whether the service answers on the hub
gitsi svc up                               # opens the tunnels in the foreground; Ctrl+C closes them
gitsi svc up postgres                      # a well-known one (postgres, redis, mysql...) is added when missing
gitsi svc up -docker                       # one tunnel per port the hub's containers publish (no catalog)
gitsi svc rm api                           # removes a service from the catalog
```

## Guide: remote execution (opt-in)
```powershell
gitsi policy set exec on
gitsi run -cwd <path-on-the-hub> docker compose up -d      # live output; returns the remote command's exit code
gitsi run -repo npm test                                   # from a shared repo: the folder as it is (uncommitted changes too)
                                                           # goes to the hub's working clone and the command runs there
gitsi run -repo -get TestResults dotnet test               # when it ends (even on failure) brings that path to Downloads\gitsi
gitsi job start -cwd <path-on-the-hub> dotnet test         # long job: it keeps going when the session closes
gitsi job start -repo dotnet test                          # the same over the folder as it is (like run -repo)
gitsi job ls ; gitsi job logs 1 -f ; gitsi job stop 1      # by list number (or by id)
gitsi job get 1 TestResults                                # finished: brings that path of its folder to Downloads\gitsi
```

## Guide: the hub's Docker from the edge
The edge uses the hub's Docker with the **official**, unmodified Docker client: Docker talks over SSH on its own
(`host=ssh://gitsi-hub`, the same alias and key as gitsi), without opening Docker ports or touching the firewall.
```powershell
gitsi docker     # EDGE: downloads docker.exe and the compose/buildx plugins from the hub (SHA256), adds their folder to the
                 # account's PATH, creates the gitsi-hub context, makes it current and checks the hub's engine. Re-run to update.
docker ps        # (in a new terminal) everything goes to the hub's Docker: run, build (with your local files), compose...
```
The hub publishes the client with `gitsi update -publish` (when Docker Desktop is installed). Docker Desktop must be running on
the hub. Remember: `-p 5432:5432` publishes the port on the hub (to use it from the edge: `gitsi svc up`) and `-v C:\path:/x`
mounts a hub folder.

## Guide: hub notices (`watch`)
The edge keeps a terminal listening and the hub tells it what happens. The edge opens the session and only notices travel, never
commands ([`docs/WATCH.md`](docs/WATCH.md)).
```powershell
gitsi watch      # EDGE: new version, outbox item, finished job, bridge branch that changes; Ctrl+C quits
gitsi edges      # HUB: which edges listen, with which version and their last heartbeat
```
Automatic reactions, opt-in (all off by default):
```powershell
gitsi config set watch.auto_update on                           # install the new version on its own (and restart watch)
gitsi config set watch.auto_take C:\Users\me\Downloads\gitsi    # pull whatever reaches the outbox
gitsi config set watch.report on                                # leave each reaction's result in the hub's inbox
gitsi config set watch.auto_fetch on                            # with watch open inside a repo: git fetch when it changes on the hub
gitsi config set watch.mirror on                                # every 10 min: gitsi mirror sync (the origin branches this edge follows)
```
With `gitsi watch -tray` it lives in the tray instead, and its menu covers daily use without typing commands: the mailbox
(take, view, discard, send what you copied), the repos' working folders (state, sync, publish, get one from the hub), the hub's
services (open and close tunnels), jobs (output, stop), the settings and the optional features (turned on after explaining them).
The icon's dot shows the state ([`docs/TRAY.md`](docs/TRAY.md)).

When the network drops or the hub goes off, `watch` reconnects on its own (5 s, 10 s, 30 s, 1 min, up to 5 min) and neither
repeats nor loses notices.

## Guide: shared clipboard
Copy on one machine and paste on the other: Ctrl+C, then **Ctrl+Shift+Alt+C** sends what you copied, and Ctrl+V on the other
machine. It works both ways while `watch` runs on both, inside the session the edge already opens ([`docs/CLIP.md`](docs/CLIP.md)).
```powershell
gitsi policy set clip on              # on each machine
gitsi watch -tray                     # on each machine: watch in the tray (or gitsi watch, in a terminal)
gitsi watch -autostart on             # HUB only: start it when you log on (the edge always starts it by hand)
gitsi config set clip.auto on         # send every copy, not only on the hotkey (also from the tray menu)
gitsi config set clip.hotkey ctrl+shift+alt+v
gitsi config set clip.max_size_mb 50  # largest item sent or accepted (20 MB by default)
```
Text, images and files copied in Explorer travel; only the latest copy, for 10 minutes. Auto mode does not send what password
managers mark as private.

## Status, diagnosis and updates
```powershell
gitsi status                # at a glance: the hub and its version, the mailbox, the jobs and this folder's repo
                            # (on the hub: also what waits in the outbox for the edge, and since when)
gitsi check                 # everything that changes nothing: tools, network, login, hub, -L tunnel, protocol and version
gitsi check --active        # also tests sending and running, only when those features are enabled
gitsi update -publish       # HUB: publishes its binary
gitsi update                # EDGE: downloads it and verifies its SHA256 (gitsi update -check only compares)
gitsi policy                # which optional features are enabled; gitsi policy set <feature> on|off
```

## What is deleted on its own (`clean`)
So that nothing piles up, gitsi applies a fixed retention when used (no background processes: when a session opens on the hub, when
the mailbox is used or when something is received; at most once an hour on the hub).

| What | Where | Deleted |
|---|---|---|
| Mailbox items (inbox and outbox) | hub | after 14 days |
| Finished jobs (folder and log) | hub | after 14 days; running ones never |
| Partial `take` downloads | the receiving machine (edge or hub) | after 7 days (recent ones resume) |
| `take -clip` copies and files received on the shared clipboard | the receiving machine (edge or hub) | after 7 days |
| The shared clipboard's slot | hub | after 10 minutes |
| `watch.log` (watch in the tray) | both | past 1 MiB it becomes `watch.log.1` |
| `audit.log` | both | past 1 MiB it becomes `audit.log.1` |

```powershell
gitsi clean -dry-run      # what would be deleted now (on the hub or the edge, depending on the role)
gitsi clean               # delete it now
```

## Where everything lives on the hub
Nobody has to choose paths: the hub uses defaults under its home folder and **announces them** in the protocol greeting.
```
<hub profile>\gitsi\               <- Home (changed on the hub: gitsi config set home <path>)
    gitsi.exe                       the binary edges download (gitsi update)
    repos\                          the repo bridge (<name>.git); created by the first share
    mailbox\                        the mailbox (inbox / outbox)
    jobs\                           background jobs
    clip\                           the shared clipboard's slot (the latest copy, for 10 minutes)
    work\<repo>\                    each repo's working clone for run -repo (keeps ignored files)
```
A hub that already keeps its repos elsewhere sets `gitsi config set repos_dir <path>` on the hub. The edge **discovers** the layout
with a greeting and caches it for 10 minutes (`gitsi check` shows and refreshes it); it neither stores nor accepts hub paths.

## Configuration
```powershell
gitsi config                       # shows the configuration (JSON)
gitsi config get hub.user          # one value
gitsi config set hub.host 10.0.0.9 # validates the value and, when it affects the connection, also updates the ~/.ssh/config block git uses
gitsi config path                  # where the file is
```
Edge keys: `hub.host`, `hub.user`, `hub.port`, `hub.key_file`, `hub.alias`, `remote_name`, `watch.*`. **Hub-only** keys: `home`,
`repos_dir` (rejected on an edge: the hub decides them). The role is not changed here: use `gitsi setup`.

## Global options and output
They go **before** the command (after it they belong to the command, so they never clash with its flags):
```powershell
gitsi -v repo list        # verbose: shows each real ssh command and how long it takes (to stderr)
gitsi -q send report.pdf  # quiet: no progress or side information; results and errors stay
gitsi --json check        # pure JSON on stdout (also: svc ls, job ls, take -list, repo list, edges)
```
Also `GITSI_VERBOSE=1` and `GITSI_QUIET=1`. `-v` and `-q` do not combine. `gitsi version` shows each binary's version, commit and
build date.

## Errors and exit codes
Network, key and fingerprint failures are **explained**: they give the cause, what to do, and exit with a stable code per category.
```
$ gitsi repo list
repo list: could not connect to the hub (192.0.2.10) [ssh: connect to host ... Connection timed out]
  -> check that the hub is on, reachable from this network (a VPN may hide the local network) and that its firewall admits this machine. Details: gitsi check
```
| Code | Meaning |
|---|---|
| 0 | success |
| 1 | uncategorized error |
| 2 | wrong usage (unknown command or flag, wrong role) |
| 10 | configuration missing or invalid (hub not set up, missing key, name that does not resolve) |
| 11 | network: the hub does not answer (off, another network, firewall) |
| 12 | authentication: the hub rejected the key |
| 13 | hub fingerprint: it does not match the known one |
| 14 | gitsi is not published on the hub (`gitsi update -publish` on the hub) |
| 15 | optional feature disabled (`gitsi policy set <feature> on`) |
| 16 | integrity: the content does not match its hash |
| 17 | timeout |

`run` also returns the remote command's exit code. Connections fail within 10 s (they do not hang on a hub that is off).

## Development
Go (version in `go.mod`), standard library only.
```powershell
.\scripts\build.ps1 [-Publish] [-Dist] [-Fast] [-SkipTests] [-Version X]   # vet + tests + build with version/commit/date
.\scripts\e2e-ssh.ps1 [-RealClipboard]       # tests against a disposable REAL sshd: starts, tests and cleans up on its own
.\scripts\race.ps1 [-Count N] [-Run regex]   # race detector (-race) in a Linux Docker container, read-only code
```
`build.ps1 -Publish` makes the new binary available to edges (`gitsi update`); `-Dist` builds the release archives and checksums
in `dist\`. `e2e-ssh.ps1` does not touch the system's sshd service, the firewall or `authorized_keys`; with `-RealClipboard` it
also tests `send -clip` (it overwrites your session's clipboard). Contribution rules are in [`CONTRIBUTING.md`](CONTRIBUTING.md).

## Documentation
- [`docs/PHILOSOPHY.md`](docs/PHILOSOPHY.md) - the project's principles
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) - layers, mailbox, protocol, CLI map
- [`docs/DECISIONS.md`](docs/DECISIONS.md) - current design decisions, by topic
- [`docs/WORKFLOW.md`](docs/WORKFLOW.md) - the edge <-> hub <-> origin workflow
- [`docs/WATCH.md`](docs/WATCH.md) - design of `gitsi watch` (hub notices and reactions)
- [`docs/CLIP.md`](docs/CLIP.md) - the shared clipboard between hub and edge (design and behavior)
- [`docs/SECURITY-REVIEW.md`](docs/SECURITY-REVIEW.md) - what gitsi does on a managed machine, for security reviewers
- [`docs/ROADMAP.md`](docs/ROADMAP.md) - planned improvements
- [`CHANGELOG.md`](CHANGELOG.md) - release notes

## License
[MIT](LICENSE) © 2026 Shizuka
