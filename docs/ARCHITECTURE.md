# gitsi architecture

> **gitsi**: a bridge between a hub (which serves) and an edge (which only consumes).
> A single Go binary, two roles. Principles in `PHILOSOPHY.md`; current decisions in `DECISIONS.md`.

## 1. Invariants

Summary of [`PHILOSOPHY.md`](PHILOSOPHY.md):

1. **Only consume, never expose.** The edge starts every connection and only listens on `127.0.0.1`. The *type* enforces it: the
   `Transport` interface has no reverse forwarding API and no way to listen outside loopback, so it cannot be asked for by mistake.
2. **The hub never initiates anything toward the edge.** Every hub -> edge flow happens because the edge pulls it (the *mailbox*
   model).
3. **Explicit opt-in**: whatever moves content or runs something is disabled by default and audited.
4. **Transport = the system's `ssh.exe` and `git.exe`**, behind interfaces.
5. **Content-agnostic**: integrity and a record, not judgment.

## 2. Core idea: the hub as a service over stdio on SSH

Instead of remote PowerShell scripts for everything, the edge runs the **same binary** on the hub:

```
edge:  gitsi send photo.png
           |  ssh.exe ... user@hub  gitsi serve --stdio        (the edge starts it; no new ports)
           v
hub:      gitsi serve --stdio  <-- framed protocol over the SSH channel's stdin/stdout
```

Benefits: no extra ports on the hub (only 22), **binary** flows without corruption (PowerShell pipes cannot carry them), resumable
transfers, checksums, compression, manifests for folders, and a single place for the hub-side logic. Git commands and tunnels
keep using `git.exe`/`ssh.exe` directly.

## 3. Layers and packages

```
cmd/gitsi            entry point (only calls app.Run)
internal/
  app                composition root: registers the modules, dispatches, enforces preconditions (policy, hub) and help
  module             Module/Command/Env contract, roles (edge|hub) and interleaved flag parsing
  -- core (leaves or nearly) --
  config             ~/.gitsi/config.json: role, hub, policy, services (atomic writes)
  fault              errors with category, cause and action; exit codes (leaf)
  fsx                atomic file writes and reads with retry (leaf)
  cleanup            retention rules: hub mailbox and jobs, partial downloads and -clip copies on the receiving machine
  safe               validation of names, branches and paths that end up in PowerShell commands (leaf)
  policy             per-feature permissions and the audit log
  progress           console progress (phases, transfers) (leaf)
  version            version, commit and build date (leaf)
  winps              launches powershell.exe without the PSModulePath inherited from PowerShell 7 (leaf)
  procx              helper processes without a console window of their own; detached processes for the tray (leaf)
  testhome           tests only: TestMain with a temporary profile (nothing touches the user's real data)
  -- system adapters --
  transport          Transport interface + SSHExec (ssh.exe/scp.exe): Run, Stream, Forward (-L only); optional
                     capabilities by type assertion: Executor (live output), Fetcher (download), *Diagnostics (stderr)
  gitx               git.exe wrapper (injectable Runner)
  clipboard          reading and writing the Windows clipboard (PowerShell -STA, through winps)
  desktop            the user's desktop through the Windows API (syscall): hotkey, clipboard listener and counter, direct
                     text, tray icon and menu, single-instance lock, the hub's logon entry
  install            publishing the binary itself (moves the previous one aside when in use) and hashes
  dockercli          the official docker client for the edge: publish it on the hub, download and verify it, account PATH, context
  -- protocol and mailbox --
  proto              versioned hub<->edge frames and the base64 text wire (armor) (leaf)
  hub                protocol server (Serve + Handler); it does not know the CLI
  hubclient          opens the `serve --stdio` session through Transport (the hub finds its gitsi in its own config)
  mailbox            mailbox store (Store), the hub's Handler and the edge's Client; pure domain, no CLI
  archive            safe tar of folders and of what Explorer copies (no links, no paths that escape)
  watch              hub notices to the edge: the hub's Handler (watches binary, outbox, jobs, repos), the edge's Client (reconnects);
                     the session also carries the shared clipboard both ways
  clip               the shared clipboard: the item, its frames, the hub's slot and the Sharer (read, filter, send, receive, write)
  layout             the hub's layout (Home, Repos, Binary, Mailbox, Jobs): computed by the hub, sent in the greeting
  hubview            how the edge discovers it (greeting + 10 min cache per user@host:port)
  -- features (one package per CLI group) --
  modules/
    provision        setup edge | hub | key, config      (OpenSSH, firewall, keys, ~/.ssh/config)
    repo             share get sync publish list status (get on the hub: working clone of the local bridge)
    drop             send take (both roles)             (mailbox: files, folders, text, clipboard)
    services         svc add rm ls up                    (-L tunnels in the foreground)
    remote           run, job start ls logs stop get     (jobs.go: commands; jobs_hub.go: scripts that run on the hub)
    doctor           check                               (diagnosis of edge and hub, report, active tests)
    policycmd        policy
    update           update [-check | -publish]
    watchcmd         watch (both: notices and clipboard; -tray, -autostart on the hub), edges (hub)
    cleancmd         clean [-dry-run]                    (applies retention on the spot)
    dockercmd        docker                              (edge: sets up the official docker against the hub's Docker)
    statuscmd        status                              (gathers the read-only probes app hands it: drop.Pending,
                                                         remote.Jobs, watchcmd.Listening, repo.Here)
    completion       setup completion [-print | -off]    (fixed block in the PowerShell profile; app gives the candidates)
    servecmd         serve --stdio (hidden; gets from app the mailbox and watch handlers, which share the session through hub.Mux)
  e2e                tests with the real binary as a process and against a disposable real sshd (opt-in)
```

Each feature is a **Module** that declares its commands, the role they apply to, their place in the CLI (`Group`, `Shortcut`) and
their preconditions (`Feature`, `NeedsHub`), and for tab completion its options (`Flags`) and the values it proposes
(`Complete`). `app` serves `gitsi __complete` from the registry: commands and groups by role, subcommands, options and values. The
**role** (edge/hub) is a saved property of the machine, never detected. Adding a capability = adding a package under `modules/`
and registering it in `app`; the core is not touched.

### 3.1 CLI map
```
setup   edge | hub | key | completion    prepare the machine, set its role, change the key, Tab in PowerShell
repo    share get list sync status publish
svc     add rm ls up
job     start ls logs stop get
check   [--active]                       diagnosis
run, update, policy, config, watch      top level (+ edges on the hub; watch also on the hub, for the clipboard)
clean, docker                           top level
daily use: status send take share sync publish (status, send and take are top level)
```
One way to do each thing: no aliases.

### 3.2 Errors, exit codes and output modes
- **`internal/fault`** (leaf package): `fault.Error{Kind, Msg, Hint, Err}`. `fault.Classify(stderr, Where)` recognizes the typical
  `ssh`/`scp`/`git` failures (rejected key, changed fingerprint, network, name that does not resolve, binary missing on the hub)
  and **never invents a cause**: when it does not recognize the text, the caller keeps its original message. The transport
  classifies when ssh itself fails (code 255), `Stream` and the tunnels capture their stderr (`StreamDiagnostics`,
  `TunnelDiagnostics`), and the dispatcher prints cause + action with its category's code.
- **Exit codes** (CLI contract): 0 ok, 1 generic, 2 usage, 10 configuration, 11 network, 12 authentication, 13 fingerprint, 14 gitsi
  missing on the hub, 15 feature disabled, 16 integrity, 17 timeout. `run` propagates the remote command's (`module.ExitError`).
- **Deadlines**: every ssh/scp connection uses `ConnectTimeout=10`.
- **Output modes** (`module.Env`: `Verbose`, `Quiet`, `JSON`): global flags only *before* the command. `-v` turns on
  `SSHExec.Trace` (one line per ssh/scp with the shortened command and its duration, to stderr); `-q` suppresses progress and
  side information; `--json` makes stdout JSON only.

### 3.3 The hub's layout: the hub decides it and announces it
- `layout.ForHub(cfg, profile)` computes `Home` (config.home or `<profile>/gitsi`), `Repos` (config.repos_dir or `<Home>/repos`)
  and hangs the binary, the mailbox and the jobs from `Home`. It is the only place with that rule.
- `serve --stdio` includes it in the greeting (`proto.Hello.Info`, an open map: adding keys does not break an older client, so the
  protocol version does not change).
- The edge asks for it with `env.HubLayout(ctx)`; on the hub, the same method computes it. `hubview.Resolver` caches it for 10
  minutes in `~/.gitsi/hub-layout.json` and validates the cache and **everything the hub announces** (those paths end up in
  PowerShell commands). A hub that does not announce it asks for `gitsi update -publish`.
- Path-free start: the launch command (`hubclient.launchFromHubConfig`) reads the hub's `~/.gitsi/config.json` to find its
  `gitsi.exe`; when that fails, it tries `gitsi` from the PATH.

### 3.4 Patterns

Dependencies (checked with `go list`): no cycles. **No module depends on another module**; `app` is the only composition root
(e.g. the mailbox and watch handlers reach `serve` as functions: `servecmd.New(drop.HubHandler, watchcmd.HubHandler)`). The leaves
(`fault`, `fsx`, `safe`, `proto`, `progress`, `version`, `winps`) depend on nothing internal. Every local `powershell.exe` is
launched with `winps` (or with its environment, `winps.Env`).

| Pattern / principle | Where |
|---|---|
| Plugin / explicit module registry (no magic `init()`) | `app.Modules()`, with duplicate detection |
| Ports and adapters | `transport.Transport`, `gitx.Runner`, `doctor.Deps`, `provision.System`, `provision.KeyDeps`, `dockercli.Runner` |
| Dependency inversion | modules receive `Env`; only `app` creates `SSHExec` |
| Segregated interfaces and optional capabilities | `Transport` without -R (what is forbidden cannot be asked for); `Executor`/`Fetcher`/`*Diagnostics` by type assertion, like `io.WriterTo` |
| Declarative preconditions | `Command.Feature` and `Command.NeedsHub`, enforced by `app.authorize`: a command cannot forget the policy |
| One writer per user file | `config.Save`; `provision.writeSSHConfig` for `~/.ssh/config` |
| Atomic writes | `fsx.WriteFileAtomic` for `config.json`, `~/.ssh/config`, `known_hosts`; `install` moves the binary in use aside |
| Remote scripts with validated inputs | every path or name that ends up in PowerShell goes through `safe`; long scripts in `*_hub.go` |
| Tests as a contract | no `-R`, administrators SID, policy, reused PID, PATH without expanding variables |

Deliberate exception: the hub's `sshd_config` is written in place, not atomically, because renaming over it would change the Windows
permissions sshd requires; it is protected by `sshd -t` validation with restoration of the previous configuration.

`config.Save` is the only writer of `config.json` (atomic); commands call it when they change something and always before any long
wait (`svc up` saves before opening the tunnels; `watch`'s reactions run separate processes that read the config again), so no late
write overwrites a change made in the meantime. A file that grows past about 400 lines is split by responsibility (like `mailbox`:
`item.go`, `upload.go`, `store.go`; `drop`: `send.go`, `take.go`, `items.go`).

## 4. The mailbox (`drop`): files, folders, text and images

Since the hub cannot push to the edge, both directions go through a store on the hub (`<hub home>/mailbox/`, see `layout`):

```
edge --send--> hub/inbox/    (what the edge sends; the hub picks it up with gitsi take)
edge <--take-- hub/outbox/   (what the hub leaves with gitsi send; the edge pulls it)
```

- **Item** = metadata (id, kind, name, size, sha256, date, origin) + content.
- **Kinds:** file, folder (tar stream, keeps the tree), text, image (clipboard or file).
- **Large items:** split into blocks with sha256, **resumable**, with progress.
- **Direction always started by the edge.** The hub only leaves items in its outbox.

## 5. Policy and audit

`config.Policy` enables by direction and kind (`allow_send`, `allow_take`, `allow_services`, `allow_exec`), all **false by default
except Git**. Each transfer or opened tunnel writes a line to `~/.gitsi/audit.log` (date, action, item/port, size). It does not
inspect content: it only keeps a record.

## 6. Method

- Decisions in `DECISIONS.md`; planned work in `ROADMAP.md`.
- Tests: `Transport` and `gitx` behind interfaces; fake runners, real temporary bare repos, PowerShell scripts really run over
  temporary folders and, opt-in, a disposable real sshd (`scripts/e2e-ssh.ps1`).
- The hub builds; the edge receives the binary with `gitsi update` (scp) with a verified hash.
- Semantic versioning (see D22).
