# gitsi design decisions

The decisions that shape gitsi, by topic. The principles behind them are in `PHILOSOPHY.md`; the code structure, in
`ARCHITECTURE.md`. A decision that changes is updated in place.

## Model

**D1 - Two roles, and the edge only consumes.** One machine is the *hub* (it serves: repos, mailbox, services, execution) and the
other the *edge* (it consumes). The edge starts every connection and exposes nothing: no `sshd`, no `ssh -R`, no listeners outside
`127.0.0.1`. The type enforces it: `transport.Transport` has no reverse forwarding API. The role is a property saved in the
configuration, never detected; the help always says which role the machine has.

**D2 - The hub never initiates anything toward the edge.** Whatever goes from the hub to the edge, the edge *pulls*: the mailbox
(outbox) and `watch`'s notices travel over sessions the edge opens.

**D3 - Standard pieces.** The transport is the system's `ssh.exe`/`scp.exe` and `git.exe`, behind interfaces. Every connection uses
`ConnectTimeout=10`: a hub that is off fails within seconds.

## Protocol

**D4 - The hub as a service over stdio.** The edge runs `gitsi serve --stdio` on the hub over SSH and they speak a protocol of
versioned frames; there are no extra ports. Each frame travels as a base64 line, because the remote shell (PowerShell 5.1) may alter
a native process's bytes. The greeting announces capabilities (`drop`, `watch`) and hub data (folder layout, version); unknown
fields are ignored. Several handlers share the session through `hub.Mux`.

**D5 - The hub decides its layout and announces it.** The hub computes where its repos, mailbox, jobs and binary live
(`layout.ForHub`) and announces it in the greeting; the edge does not configure hub paths, it discovers them and caches them for 10
minutes. What is announced is validated: those paths end up inside PowerShell commands.

## Security and control

**D6 - Optional features, enabled deliberately.** Whatever moves content or runs something (`send`, `take`, `services`, `exec`,
`clip`) is off on the edge. It is enabled with `gitsi policy set <feature> on` or by answering "y" when gitsi asks at a terminal on first use
(without a terminal, or with `--json`, it does not ask: it fails with the hint). The hub does not require it: there those commands
only move files within its own mailbox. `clip` is the exception: it touches the hub user's clipboard, so the hub requires its own. Each enablement and each transfer is recorded in `~/.gitsi/audit.log` (action, name, size;
never content), which rotates past 1 MiB.

**D7 - Inputs validated before reaching a shell.** Every name, branch or path that ends up in a PowerShell command goes through
`safe` (or through each case's own validation, such as public keys). `watch`'s notices are validated, and their texts are cleaned of
control characters before being shown.

**D8 - Errors with cause and action.** `fault` classifies the typical ssh/scp/git failures (rejected key, changed fingerprint,
network, name that does not resolve, gitsi missing on the hub) without inventing causes, and each category has a stable exit code
(0 ok, 1 generic, 2 usage, 10 configuration, 11 network, 12 authentication, 13 fingerprint, 14 gitsi missing on the hub, 15 feature
disabled, 16 integrity, 17 timeout).

## Mailbox

**D9 - Verified, resumable items.** An item is a file, a folder (gzip-compressed tar) or a text, with end-to-end SHA256; transfers
resume. The hub has two mailboxes, one per direction: the *inbox* receives what the edge sends and the *outbox* keeps what the hub
leaves for the edge. `-exclude` skips entries by name when packing folders.

**D10 - `send` and `take` on both machines.** The same verbs and options on the hub and the edge; each uses the mailbox that fits.
Bare, they use the clipboard: `send` sends what was copied (files or folders from Explorer, an image or a text) and `take` brings the
latest item to the clipboard (a text as text; a file or folder as a path, left in `%LOCALAPPDATA%\gitsi\clipboard\<id>`; an image,
also as an image). With options: `-last`, `-dir`, `-keep`, `-list`, `-show` (view a text without taking it) and `-rm` (discard).
Received items go to `Downloads\gitsi` by default.

## Features

**D11 - Repos through a bridge.** The hub keeps one bare repo per project. `share` creates it (with the right branch and owner) and
pushes; `get` brings it (on the hub, it makes the working clone); `sync` pulls and pushes the current branch, without merging. When
the branches diverge the edge's wins only if nothing is lost unseen: the edge replaces the bridge's branch when its own branch had
already contained everything there (its reflog proves it), or after showing the hub's new commits and asking. The push has an
explicit lease and leaves the replaced version in `refs/gitsi/replaced/<branch>/tip` (one per branch). The hub adopts a replaced
branch (`git reset --keep`) when all its commits had been on the bridge (reflog of the tracking ref); it never pushes back what the
edge dropped; `publish` pushes to `origin` (never to the bridge) and leaves the upstream there. It pushes one commit, authored by
the edge's Git user: squash and author reset are done on a commit built aside (`git commit-tree`), so the local branch and the
bridge keep their history and `sync` does not diverge. The base is the shared branch with the fewest commits of difference (the
default one on a tie), or `-base`, and it is remembered. The last publish is noted in the branch's Git configuration. The branch on
origin is replaced with an explicit `--force-with-lease=<ref>:<commit>`, only when everything in it is in the local branch or was
published by gitsi; what this clone did not publish is replaced only after asking. Shared branches (the remote's default one, the
base, `develop`, `release/*`...) get a plain push. Nothing uses `-u` toward the bridge: a plain `git push` does not go to the hub.

**D12 - Services through local tunnels.** `svc` opens `-L` tunnels in the foreground toward the hub's services, always on
`127.0.0.1`. `svc up <name>` adds a well-known service (postgres, redis...) to the catalog on its own. `svc up -docker` asks the hub
which TCP ports its containers publish (`docker ps`, read-only) and opens one tunnel per port on the same local port, without
saving them in the catalog; a port already taken on the edge is reported and skipped.

**D13 - Remote execution.** `run` runs on the hub with live output and returns the command's exit code. `run -repo`, from a shared
repo, takes the folder as it is (uncommitted changes and new files, honoring `.gitignore`) to a working clone of the repo on the hub
(`<home>/work/<repo>`) and runs there: the snapshot is built with a temporary index (it touches neither the user's index nor their
branch) and travels in a hidden bridge ref (`refs/gitsi/run`), without touching its branches; the working clone is made equal to the
snapshot on every run and keeps ignored files (`node_modules`, `bin`) for fast builds. `run -repo -get <path>` brings that path of
the working clone to `Downloads\gitsi` when it finishes (also when the command failed): the hub leaves it in its outbox with its own
gitsi and the edge pulls it like `take` (it requires the `take` policy; the path is relative to the clone and cannot leave it).
`job start -repo` does the same as `run -repo` in the background. Each job remembers its folder, and `-repo` does not prepare the
clone while a job is still running in it (it would overwrite it halfway). `job get <n|id> <path>` brings, like `-get`, a path of a
finished job's folder (`-cwd` or the `-repo` clone); while the job is still running it refuses, because the folder would be half
written. `job start` launches through WMI, outside the SSH session's process tree, so it survives the disconnection; the state lives
in files. A job only counts as alive when its PID has the start time saved at launch (Windows reuses PIDs). `job logs -tail N` asks
only for the end of the log.

**D14 - Status and diagnosis.** `status` is the daily glance: the hub and its version, what waits in the mailbox, the jobs (when
`exec` is on), on the hub what waits in the outbox and since when, the listening edges (on the hub) and the current folder's repo
against the bridge. Each line comes from a read-only probe of the module that knows the data; when the hub does not answer, what
depends on it is not queried and status exits with that error. `check` does everything that changes nothing (tools, network, login,
hub, tunnel, protocol, version) and leaves a report to paste; `check --active` also tests sending and running.

**D15 - Hub notices (`watch`).** The edge opens `gitsi watch` (in a terminal, or in the tray with `-tray`) and the hub sends it
typed notices (new version,
outbox item, finished job, bridge branch that changes). The edge says what it knows when it connects and the hub only notifies the
differences (the first time, a baseline); heartbeat every 30 s and reconnection with growing waits. `gitsi edges` shows on the hub
who is listening. Opt-in reactions (`watch.auto_update`, `watch.auto_take`, `watch.report`, `watch.auto_fetch`, `watch.mirror`; `auto_fetch` only in
the repo where watch runs) run gitsi itself without interactive input; a new version is attempted only once and, once installed,
watch restarts with it. Full design in `WATCH.md`.

**D26 - Shared clipboard.** With the `clip` policy on both machines, `watch` shares the clipboard: a hotkey
(`clip.hotkey`, Ctrl+Shift+Alt+C) sends what is copied, or every copy with `clip.auto`, and what arrives is written to the clipboard.
It travels inside the watch session (frames both ways) to a single slot on the hub that each copy replaces and that expires after
10 minutes; only what is copied while watch runs is delivered, and each side applies its own `clip.max_size_mb`. On the hub, `watch`
runs in the user's session (sshd sessions and services cannot reach the clipboard) and may start on logon (`-autostart`); on the
edge it is always started by hand, in a terminal or in the tray. Hotkey, change listener, text and tray use the Windows API through
`syscall`; images and files, PowerShell. Auto mode skips this process's own writes and copies marked private by password managers.
Full design in `CLIP.md`.

**D27 - The tray menu runs gitsi itself.** For whoever prefers not to type commands, `watch -tray` offers the daily part of gitsi
in its menu: mailbox, repos' working folders, services, jobs, edges, settings and features. Each entry runs the same binary as a
child (`gitsi take -clip <id>`, `gitsi repo sync` in the folder) without interactive input, instead of calling the modules: the
same policies, audit and messages, and `watchcmd` depends on no other module. Lists come from caches read in the background
(`--json`), so the menu opens at once. Turning a policy on from the menu explains it, asks with No as the default and goes through
`gitsi policy set`. Tunnels opened from the menu live in a job object and close with it. To list working folders, `repo share`,
`repo get` and `repo sync` record them in `~/.gitsi/workdirs.json` (at most 50, missing ones forgotten). The menu is native (Win32
through `syscall`, submenus, glyphs, dark mode); the icon, the glyphs and the executable's icon are drawn in code. Details in
`TRAY.md`.

**D16 - The hub's Docker with the official client.** The hub publishes `docker.exe` and the `compose` and `buildx` plugins. `gitsi
docker` (it requires `exec`) downloads them verifying their SHA256, adds their folder to the account's PATH and creates the
`gitsi-hub` context (`host=ssh://<alias>`): Docker talks over SSH on its own and gitsi stays out of that traffic. The PATH is edited
in the registry without expanding `%VARIABLES%` or duplicating entries.

## Machines

**D17 - Provisioning.** `setup hub` (administrator) installs and hardens OpenSSH (key only, only the given user, local forwarding
only), limits the firewall to the edge's IP, registers the key and sets PowerShell as the SSH shell; it validates with `sshd -t` and,
on failure, restores the previous configuration. `-dry-run` shows the plan. `setup edge` creates the key, writes the
`~/.ssh/config` block and pins the hub's fingerprint when given. `setup key` changes the edge's key without cutting access at any
moment.

**D18 - Updates from the hub.** The hub publishes its binary (`gitsi update -publish`, which also publishes the docker client); the
edge downloads it with `gitsi update`, verifies the SHA256 and puts it in place, moving the current one aside (it may be in use).
Publishing does not fail with the hub's binary in use either. The build keeps the symbol table and uses `-trimpath`: a stripped
binary is harder to inspect, and some endpoint protection products quarantine stripped Go executables.

**D19 - Retention: nothing piles up.** Mailbox items and finished jobs on the hub, after 14 days; partial downloads and `take -clip`
copies on the receiving machine, after 7; the hub's clipboard slot, after 10 minutes; `watch.log` (the tray) and `audit.log`
rotate past 1 MiB. No background processes: cleanup is triggered by use (at most once an hour on the hub)
and `gitsi clean [-dry-run]` applies it on the spot.

**D20 - Files without half states.** `config.json`, `~/.ssh/config` and `known_hosts` are written atomically (temporary file and
rename), each from a single place in the code. The exception is `sshd_config`, written in place to keep its permissions and
protected by `sshd -t` with restoration.

## Shape of the CLI and the project

**D21 - One way to do each thing.** Groups for flows with several steps (`setup`, `repo`, `svc`, `job`) and top-level commands for
the rest; the daily ones (`send`, `take`, `share`, `sync`, `publish`) are named without a group. Each help summary fits in half a
line; option details come from `gitsi <command> -h`.

**D22 - Semantic versioning.** From 1.0.0 on, gitsi follows semantic versioning: the command line, the exit codes, the
configuration file and the hub <-> edge protocol are the public interface. A breaking change bumps the major version; additions
bump the minor one and fixes the patch one. Hub and edge should run the same version (`gitsi status` shows both and `gitsi update`
aligns them).

**D23 - Tests.** Everything that touches the system is injectable. PowerShell scripts are tested by really running them over
temporary folders and registry keys; `scripts/e2e-ssh.ps1` tests against a disposable real sshd and `scripts/race.ps1` runs the race
detector in a Linux container. CI runs the full suite on Windows and the race detector on Linux.

**D24 - List numbers instead of ids.** `take -list` and `job ls` number what they show (`[1]`, `[2]`...; the mailbox from oldest to
newest, jobs by start time) and wherever an item or a job is asked for, that number or the id works. A number of 1 to 3 digits is
always a position: ids have 8 or 16 hexadecimal characters.

**D25 - Completion resolved by the binary.** `gitsi setup completion` leaves a fixed block in the PowerShell profile that, on each
Tab, asks `gitsi __complete`; completion thus follows the installed version without touching the profile again. Each command
declares its options (`Command.Flags`, which a test compares with its FlagSet) and, optionally, the values it proposes
(`Command.Complete`: services, repos, config keys). Whatever must be asked of the hub has a short deadline: when it does not arrive,
nothing is proposed. gitsi does not change the execution policy: when it does not allow loading the profile, gitsi says so.
