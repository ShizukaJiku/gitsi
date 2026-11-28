# Workflow: edge <-> hub <-> origin

```
                                   (network)                 (SSH, started by the edge)
 origin (e.g. GitHub) <------------ EDGE <----------------------------------> HUB
   ^   PR                            | pull/push origin                        |  builds, Docker, agents
   |                                 | pull/push hub                           |  (never reach origin)
```

Rules:

1. **`origin` = the target repo (e.g. GitHub). `hub` = the bridge.** They are independent: pushing to `hub` never reaches origin.
2. **The PR comes from the edge** (`gitsi publish`), because the edge is the machine that reaches `origin`.
3. Branches track `origin`; the bridge does not change their upstream (gitsi never uses `-u` toward `hub`).
4. One branch per task. Before switching machines: `gitsi sync`; on arrival: `gitsi sync`.

## Typical cycle

```powershell
# EDGE - once per repo
gitsi share                                # creates the repo on the hub and pushes everything to the bridge

# HUB - once per repo
gitsi repo get <name>                      # working clone (builds, Docker, agents)
git checkout -b feature/x                  # work on the hub
git commit ... ; gitsi sync                # push to the bridge

# EDGE - bring and publish
gitsi sync                                 # brings feature/x from the bridge
# test whatever depends on resources only the edge reaches
gitsi publish                              # pushes the branch to origin as one commit of yours and prints the PR URL
```

If the PR review asks for changes, the cycle repeats on the same branch: `publish` replaces the PR's commit. The local branch keeps
its own commits, so `git status` may show it as diverged from origin: update the PR with `gitsi publish`, not with a pull and push
(`gitsi repo status` shows what is left to send). Commits added to the PR on origin must be brought first; `publish` stops and says
how. To bring to the hub what was merged on origin: on the
edge `git pull origin main`, then `gitsi sync`.

To go back to the hub after working on the edge:

```powershell
git commit ... ; gitsi sync                # EDGE: sends the commits; after an amend or rebase, replaces the hub's (asks if it has new work)
gitsi sync                                 # HUB: takes the edge's version
```

## Following origin branches the hub needs

When the hub has to stay up to date with origin branches nobody changes from the edge (the team's `development`, `main`):

```powershell
# EDGE - once
gitsi mirror add app https://github.com/acme/app.git -branch development
gitsi config set watch.mirror on           # with gitsi watch open: a cycle every 10 minutes

# HUB - in the working clone
git fetch
git log HEAD..mirror/development           # what the origin has that this branch does not
gitsi mirror ls                            # when the edge last checked each mirror, and its errors
```

## What runs where

| Task | Machine |
|---|---|
| Editing with agents, builds, Docker | hub |
| Tests against resources only the edge reaches | edge |
| `gitsi publish`, PR, review | edge |
| Shared repo (bridge) | hub (`<repos_dir>/<name>.git`) |
| Files, folders, text and images between machines | mailbox (`gitsi send` / `take`) |
