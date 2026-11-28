# `gitsi mirror` - origin branches on the bridge (design)

## 1. Problem

The hub does not reach the origin (GitHub, GitLab...); the edge does. The hub often has to stay up to date with origin branches
that change without anyone on the edge touching them: the team's `development`, `main`, `release/*`. `share` and `sync` carry
what the edge shares, but nothing *follows* an origin branch: someone would have to pull and sync each repo on the edge, by hand,
every time.

`gitsi mirror` has the edge keep those branches mirrored on the bridge, on its own and cheaply, and lets the hub know what
changed and when the edge last checked.

## 2. Limits

| Principle | How it is honored |
|---|---|
| Each machine decides what is its own | Only the edge decides what is followed (`mirror add`, `rm`). The hub never supplies a URL or a branch: what it receives is a report of what the edge did. For many repos, the hub can leave a suggested list in the outbox; the edge imports it with `mirror add -list` after seeing it (it shows the list and asks). |
| The edge never acts as a door to other resources | Origin URLs are `https://`, `ssh://` or `user@host:path` (`safe.GitURL`): no `file://`, local path, `ext::` or leading dash, so a suggested list cannot make the edge send something of its own to the hub. Toward the origin git may only use https and ssh (`protocol.allow`), also after the user's `insteadOf` rewrites. |
| Nothing hidden or unattended on the edge | The cycles run inside `gitsi watch` (`watch.mirror on`), started by hand and visible, and stop with it; `mirror sync` runs one in the foreground. No service, scheduled task or logon entry. |
| Opt-in | `watch.mirror` is off by default. No policy is needed: moving content to the bridge through git needs none (`share`, `sync`), and `mirror add` is itself the conscious act. |
| Nothing is merged or touched | The mirror lives in hidden refs of the bridge repo; no user branch changes, on the edge or on the hub. |
| Content-agnostic | gitsi does not read what it mirrors (versions, build files): that is the user's scripts' business, on top of `gitsi --json mirror ls`. |

## 3. How it works

```
EDGE                                              HUB (bridge repo <name>.git)
~/.gitsi/mirrors/<name>.git  --- push only what moved --->  refs/gitsi/mirror/<branch>
   ^ git fetch (https/ssh, never interactive)               <home>/mirror/<user@machine>.json  (the edge's report)
origin                                                      working clone: git fetch -> mirror/<branch>
```

- **Add.** `mirror add <name> <url> -branch <b>` creates the bridge repo when it does not exist (like `share`) and seeds a local
  bare (`~/.gitsi/mirrors/<name>.git`) from the bridge, so the first fetch only brings from the origin what the bridge lacks. The
  seed refs stay until that first fetch: without them git would not tell the origin those objects are already there.
- **Cycle** (`mirror sync`, or watch every 10 minutes and when it starts). Per repo, with a 2-minute deadline and without stopping
  the others when one fails: fetch the branches from the origin; compare each tip with the one the bridge last confirmed
  (`refs/gitsi/pushed/<b>` in the local bare); push only those that moved, to `refs/gitsi/mirror/<b>`. The mirror is exact (a
  rewritten origin branch is pushed as it is) and every push carries an explicit lease, the tip last confirmed: when it fails
  (another edge, a local record that fell behind), the real tips are read and the push is tried once more.
- **Never waiting for a person.** `GIT_TERMINAL_PROMPT=0`, `GCM_INTERACTIVE=never`, and git runs without a console, so neither
  ssh nor the credential manager can ask: an expired credential is that repo's error.
- **One at a time.** A named mutex per profile: a manual `mirror sync` and watch's cycle never push the same branch twice.
- **Report.** After each cycle (and each `rm`) the edge sends the hub its whole state in a `KindMirror` frame, over a session of its
  own, behind the `mirror` capability: per branch, the tip, when it changed, when it was checked and the last error; the origin's
  host, not its URL. A hub without the capability (older) only produces a warning. This is the cycle's only connection to the hub
  when nothing moved.

## 4. What the hub sees

```
gitsi mirror ls           # per edge, repo and branch: tip, changed, checked, error; STALE after 30 min without a cycle
gitsi --json mirror ls    # the same, for the user's scripts
```

In a working clone (`repo get` on the hub), `repo get` and `repo sync` add `+refs/gitsi/mirror/*:refs/remotes/mirror/*` to
`origin`'s fetch refspecs, so `git fetch` and `git log HEAD..mirror/development` work without knowing the hidden refs. `mirror/`,
not `upstream/`, which many clones already use for a remote of their own.

`watch`'s `repo.updated` notices only read `refs/heads/`, so the edge is not notified of its own mirrors.

## 5. Retention

`mirror rm` removes the bridge's mirror refs and the local copy (the bridge repo stays). On the hub an edge's report goes after 14
days without a cycle; on the edge `gitsi clean` removes the local copy of a mirror no longer followed, once it is a day old.

## 6. Not in this version

Branch globs (`release/*`), tags, keeping the tip a rewrite replaced, a notice to the hub when a mirror changes, the hub asking for
a cycle now, a configurable interval. Two edges following the same bridge repo from different origins would correct each other:
the lease only makes sure neither overwrites the other unknowingly.

## 7. Tests

Real git over temporary folders, with origins reached through `insteadOf` (`internal/modules/mirrorcmd`): seeding from the bridge,
no push when nothing moved, rewrites, the lease that recovers, one failing repo among others, the protocol limit, an origin that
asks for a password failing at once, lists validated whole. The report with in-memory pipes and the hub's real handler
(`internal/mirror`); the watch reaction with a fake runner (no overlap, a lasting failure said once).
