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
gitsi publish                              # pushes the branch to origin and prints the PR URL
```

If the PR review asks for changes, the cycle repeats on the same branch. To bring to the hub what was merged on origin: on the
edge `git pull origin main`, then `gitsi sync`.

## What runs where

| Task | Machine |
|---|---|
| Editing with agents, builds, Docker | hub |
| Tests against resources only the edge reaches | edge |
| `gitsi publish`, PR, review | edge |
| Shared repo (bridge) | hub (`<repos_dir>/<name>.git`) |
| Files, folders, text and images between machines | mailbox (`gitsi send` / `take`) |
