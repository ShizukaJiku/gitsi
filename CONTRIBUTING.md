# Contributing to gitsi

gitsi connects two machines: a **hub** that serves (repos, mailbox, services, execution, Docker) and an **edge** that only
consumes. A single Go binary. Before changing something, read what applies:

| Document | Contents |
|---|---|
| `docs/PHILOSOPHY.md` | principles (non-negotiable) |
| `docs/DECISIONS.md` | current decisions, by topic |
| `docs/ARCHITECTURE.md` | packages, layers, patterns, CLI map |
| `docs/ROADMAP.md` | planned improvements |
| `docs/WATCH.md` | design of `gitsi watch` |

## Principles the code cannot break
- **The edge only consumes**: it listens on nothing outside `127.0.0.1`, never uses `ssh -R` and never acts as a door from the hub
  to other resources. `transport.Transport` has no reverse forwarding API on purpose.
- **The hub never initiates anything toward the edge**: whatever goes from the hub to the edge, the edge pulls (mailbox, `watch`
  notices).
- **Opt-in**: whatever moves content or runs something on the edge is declared with `Command.Feature` and audited.
- **Nothing in the background on the edge**: no services or scheduled tasks; long-running things run in the foreground (`svc up`,
  `watch`).
- **Simplicity**: one way to do each thing; good defaults (bare commands do the common thing).
- **Semantic versioning**: the CLI, the exit codes, the configuration file and the hub <-> edge protocol are the public interface.
  Code and docs describe the current state, without "previously..." notes; history lives in git and in `CHANGELOG.md`.

## Architecture
- `internal/app` is the only composition root: it registers the modules (`app.Modules()`), dispatches and enforces preconditions.
- **No module (`internal/modules/*`) depends on another module.** Shared code goes to an `internal/` package (e.g. `dockercli`,
  `cleanup`, `install`); a handler reaches another module as a function from `app` (`servecmd.New(drop.HubHandler, ...)`).
- Domain packages without CLI or ssh (`mailbox`, `watch`, `hub`, `proto`) receive an `io.ReadWriter` or interfaces.
- Leaf packages without internal dependencies: `fault`, `fsx`, `safe`, `proto`, `progress`, `version`, `winps`.
- Every local `powershell.exe` is started with `winps.Command` (or `cmd.Env = winps.Env(os.Environ())`): otherwise, from a
  PowerShell 7 console it inherits its `PSModulePath` and basic cmdlets fail.
- Everything that touches the system is injectable (a function or an interface with a real default): `transport.Transport`,
  `gitx.Runner`, `provision.System`, `dockercli.Runner`, `var X = func...` variables for tests.
- Optional transport capabilities by type assertion (`transport.Executor`, `transport.Fetcher`), like `io.WriterTo`.
- New functionality = a package in `internal/modules/<x>` with `New() module.Module`, registered in `app.Modules()`, documented in
  the README (a test requires it) and, if it decides something, in `docs/DECISIONS.md`.

## Code style
- Go with the standard library only: **no third-party dependencies** (`go.mod` without `require`). `gofmt` and `go vet` clean.
- Everything in English: identifiers, comments, messages and documents. Source files are ASCII, except test data that
  deliberately covers other characters.
- Comments explain the *why* and the risk they avoid, not what the code says. Every package and every exported identifier has a
  comment.
- **Commands**: `module.Command{Group, Name, Summary, Roles, Feature, NeedsHub, Run, Flags, Complete}`. `Flags` lists the options
  for completion (a test compares them with the FlagSet); `Complete`, optional, proposes values (fast: it runs on every Tab).
  `Summary` is half a line (at most 75 characters; a test requires it); details go in the options (`gitsi <cmd> -h`). Options use
  `module.NewFlagSet(name, env.Stderr)` (a test requires it: on a mistyped option it proposes the closest one instead of dumping
  the usage) and `module.ParseInterleaved` (options and arguments in any order). Options are parsed before anything connects:
  `-h` never needs the hub (a test runs every command's `-h` offline).
- **Errors**: lowercase, they state the cause and what to do. Categorizable ones use `fault.New(kind, message, action)` (each
  category has its exit code); the rest `fmt.Errorf("...: %w", err)`. Never invent a cause.
- **Output**: results to `env.Stdout`, diagnostics to `env.Stderr`; honor `env.Quiet` and `env.JSON` (`env.PrintJSON`); progress
  with `env.Progress()`. Text that comes from outside is cleaned of control characters before being shown.
- **Remote PowerShell**: scripts are built in Go; every input that ends up in them is validated first (`safe.Name`, `safe.Path`,
  `safe.Branch` or a dedicated validation) and goes between single quotes. When a script must fail on an error, it starts with
  `$ErrorActionPreference = 'Stop'`. Long hub scripts live in `*_hub.go` files.
- **User files**: `fsx.WriteFileAtomic` (never `os.WriteFile` for the config, `~/.ssh/config`, `known_hosts`), a single place in
  the code writes each file, temporaries are deleted. The config is saved (`Config.Save`) before any long wait, never after: a
  process that saves late would overwrite what another changed in the meantime. Nothing piles up: whatever gitsi leaves has a
  retention (`cleanup`).
- **Concurrency**: `context.Context` in everything that waits; deadlines on everything that goes over the network; no races
  (`scripts/race.ps1`).

## Tests
- Every change comes with tests. A bug is **reproduced first with a failing test**, then fixed.
- PowerShell scripts are tested by really running them over **temporary** folders and registry keys; never over the user's real
  configuration (PATH, `~/.ssh`, the clipboard: the latter only with `GITSI_REAL_CLIPBOARD=1`, restoring it).
- Every test package that resolves profile paths (`os.UserHomeDir`, `os.UserCacheDir`) has
  `func TestMain(m *testing.M) { testhome.Run(m) }`: the test binary runs with a temporary profile. In `internal/modules/*` a test
  requires it.
- On critical paths, check with a mutation that the test catches the defect it claims to cover.
- Before each commit: `go vet ./...` and `go test ./... -count=1`. When touching ssh, the protocol, `serve`, jobs or `watch`:
  `scripts/e2e-ssh.ps1` (disposable real sshd). When touching concurrency: `scripts/race.ps1`.

## Documentation
- `README.md` mentions every visible command (a test checks it), links every `docs/*.md` (a test checks it) and shows daily use.
- Commands quoted in code, docs and scripts must exist with those options (a test checks it).
- `docs/DECISIONS.md` is updated in place when a decision changes; `docs/ROADMAP.md` holds what is pending (removed when done);
  `CHANGELOG.md` gets an entry for every user-visible change.

## Versions and releases
- The version lives in `internal/version/version.go`. Breaking changes bump the major version, additions the minor one, fixes the
  patch one.
- Build: `scripts\build.ps1` (vet, tests, build with symbols and `-trimpath`); `-Publish` makes it available to edges. Do not strip
  the symbol table (`-s -w`): some endpoint protection products quarantine stripped Go binaries.
- Release: update the version and `CHANGELOG.md`, commit, tag `vX.Y.Z` and push the tag. The release workflow checks that the tag
  matches the code, runs the tests, builds the archives with `scripts\build.ps1 -Dist` and publishes the GitHub release with the
  changelog section as notes.
- Commit titles: `gitsi X.Y.Z: summary` for a release, or a descriptive title otherwise; the body says what and why.
