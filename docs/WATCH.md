# `gitsi watch` - hub notices to the edge (design)

## 1. Problem

Without `watch`, the edge only learns what happens on the hub when someone runs a command: the hub publishes a version and nobody
updates the edge; a job finishes and nobody knows until `job ls`; the hub leaves something in the outbox and someone has to
remember to `take` it.

Goal: the edge **learns by itself** what happens on the hub and can react with predefined actions, and the hub knows whether the
edge is listening and with which version.

## 2. What does NOT change (design limits)

`watch` has to fit the principles in `PHILOSOPHY.md`. Therefore:

| Principle | How it is honored |
|---|---|
| The edge starts every connection | `gitsi watch` opens the SSH session from the edge, like any other command. The hub never connects to the edge. |
| The hub never makes the edge run anything arbitrary | What travels are **notices** with typed data (a known kind and its fields), never commands. The edge decides what to do with each kind, and can only take fixed actions built into gitsi. An unknown kind is ignored. |
| Nothing in the background on the edge | `watch` runs in the foreground in a terminal, like `svc up`, and closes with Ctrl+C. It is not a service or a scheduled task. |
| Explicit opt-in | Listening to notices moves no content. Each automatic reaction (pull from the outbox, update, report to the hub) is enabled separately and still requires the existing policy (`take`, `send`). |
| Record | Connections, notices and actions are written to `audit.log`. |

For the same principles, any way for the hub to use the edge to reach other resources (gateway, proxy, reverse forwarding) is out
of scope.

## 3. How it works

```
EDGE (open terminal)                                  HUB
gitsi watch -- ssh -- gitsi serve --stdio  -------->  watch session (one process per connected edge)
     <----------- notice: update.available {1.0.1, sha256}    checks every 2 s: published binary,
     <----------- notice: outbox.item {id, name, size}         outbox, finished jobs, bridge repos
     <----------- notice: job.finished {id, code}
     <----------- heartbeat every 30 s                         writes <home>/watch/<user>@<machine>.json
```

1. `gitsi watch` opens the same session `send` and `take` use (`hubclient.Dial` over `serve --stdio`) and asks for the `watch`
   capability in the greeting. A hub that does not announce it gives an actionable error (`gitsi update -publish` on the hub).
2. The edge sends a `Watch` frame with **what it already knows**: hash of its installed binary, outbox ids already seen, jobs whose
   end it already reported, bridge branch tips.
3. The hub's process compares that state with the real one and sends a notice for each difference. Then it keeps checking every
   2 s and reports each change. **No event log is needed on the hub**: since the edge says what it knows, a reconnection never
   loses anything nor repeats what was already seen.
4. The hub sends a heartbeat every 30 s; the edge only listens. When the edge goes 75 s without receiving anything (network down,
   hub off), it closes the session and retries with growing waits (5 s, 10 s, 30 s, 1 min, up to 5 min), showing the state in the
   terminal.

None of this needs a new process on the hub besides the session itself: each connected edge's `serve --stdio` does the watching and
dies with the session.

## 4. Notices

| Kind | Fields | When |
|---|---|---|
| `update.available` | version, sha256 | the binary published on the hub changes and is not the edge's |
| `outbox.item` | id, kind, name, size | a new item appears in the outbox |
| `job.finished` | id, exit code | a job started with `job start` ends |
| `repo.updated` | repo, branch, commit | a bridge branch changes or appears (e.g. an agent on the hub pushed `feature/x`) |
| `hello` | hub version, time | on connection (to show which hub is talking) |

The hub reads branch tips from each bare repo's ref files (loose and `packed-refs`), without starting git on every check. The edge
remembers the ones already notified (`heads` in its state) and the hub only notifies the differences; a state without them asks for
the baseline, so existing branches do not show up as new. A deleted branch is not notified.

Each field is validated on receipt (`safe`): an odd name or id discards the whole notice.

## 5. Edge reactions

Always: a line in the terminal with the time.

Optional, each one enabled in `watch`'s configuration and with its policy:

| Reaction | Enabled with | Requires | What it does |
|---|---|---|---|
| Pull from the outbox | `watch.auto_take <folder>` | `take` | `take` of that item into the folder (never overwrites, verifies SHA256) |
| Update | `watch.auto_update on` | - | `update` with its hash verification. **Once per hash**: when it fails (e.g. security software removes the file), it does not retry that same hash on its own |
| Fetch the repo | `watch.auto_fetch on` | - | when `watch` runs inside that repo (its bridge remote is that repo), `git fetch`: it only updates the remote branches, it does not touch the branch or the files |
| Report to the hub | `watch.report on` | `send` | after each reaction, leaves a text in the hub's inbox (`watch-update.txt`, `watch-take.txt`) with the result |

Example cycle with `auto_update` and `report` on: the hub publishes, the edge receives `update.available`, updates verifying the
hash, restarts with the new binary and leaves "1.0.1 installed, hash ok" in the hub's inbox. On the hub, `gitsi take -show` shows
it.

**Restart after updating:** `watch` is `gitsi.exe` itself. After replacing itself, it starts the new binary in the same terminal
(`gitsi watch`, with the same options) and exits; the user sees a "restarted with 1.0.1" line. When the new binary does not start,
it says so and stays stopped (it does not loop).

## 6. What the hub sees

Each edge's session keeps `<hub home>/watch/<user@machine>.json`: connected since, last heartbeat, the edge's gitsi version and
which reactions it has enabled (names only). `gitsi edges`, on the hub, lists them:

```
EDGE                             VERSION   STATE          LAST HEARTBEAT   REACTIONS
user@workstation                 1.0.1     listening      12 s ago         update, report
```

When the last heartbeat is older than 90 s, the edge shows as disconnected (the session died without closing the file).

## 7. Protocol

- `watch` capability in the greeting; `Watch` frames (edge -> hub, known state), `Event` (hub -> edge, a notice) and `Ping` (the
  hub's heartbeat). The hub notices the edge left because writing the heartbeat fails.
- First connection (the edge has no saved state yet): the hub sends a `baseline` notice with what already exists (outbox, finished
  jobs, branches) and the edge saves it without showing it; that way weeks-old jobs do not appear as new.
- A watch session does not mix other operations (send/take open their own session).
- Cost on the hub: every 2 s, the published binary's size and date (the hash only when they change), an outbox listing, the jobs'
  `exit` files and the bridge repos' refs. Negligible.

## 8. Tests

- Protocol and watching with in-memory pipes and a fake clock (like the mailbox): known state -> expected notices, reconnection
  without losses or repeats, heartbeats and dead-session detection.
- Reactions with a fake hub: `auto_update` once per hash, `auto_take` without overwriting, `report` requires `send`.
- Against a real sshd (`scripts/e2e-ssh.ps1`): a new outbox item reaches the edge and the hub records who listens.

## 9. Decisions

1. **`auto_update`**: off by default (notify only) until enabled by hand.
2. **Reports**: one hub inbox item per reaction.
3. **Desktop notifications**: not yet (see `ROADMAP.md`).
4. **Start**: by hand, like `svc up`; never on login (that would be a background process).
