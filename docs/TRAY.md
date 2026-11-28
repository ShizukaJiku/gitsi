# gitsi in the tray

`gitsi watch -tray` keeps watch in the notification area. Besides the shared clipboard (`CLIP.md`) and the hub's notices
(`WATCH.md`), its menu offers what people use every day without typing commands or reading configuration files: the mailbox,
the repos' working folders, the hub's services and jobs, the edges, the settings and the optional features.

## 1. Principles

| Principle | How the tray keeps it |
|---|---|
| One way to do each thing | Each entry runs gitsi itself as a child (`gitsi take -clip <id>`, `gitsi repo sync`...), without interactive input: the same checks, audit and messages as at a terminal. The tray adds no second implementation, and `watchcmd` depends on no other module. |
| Opt-in, conscious and audited | A feature that is off shows why and offers "Turn it on...": the menu explains what it enables (`policy.Effects`), asks with No as the default and runs `gitsi policy set <feature> on`, which records it in `audit.log`. Turning a feature off does not ask. |
| Nothing hidden or unattended | Tunnels opened from the menu are checked in it, close from it and close with Quit (and with the process: they live in a job object). Nothing starts on its own on the edge. |
| The menu never waits | Lists come from caches read in the background (`--json`); the menu shows the last ones and says when they were read, or why they could not be. |
| Text from outside is cleaned | Names, ids and messages from children are cleaned of control characters; ids are validated before they go back as arguments. |

## 2. The menu

```
[mark] Connected to the hub                      (bold: the state)
       Last: 14:37 sent image (102.2 KB)        (grey: the last item; on the hub, who listens)
------------------------------------------
Send what I copied           Ctrl+Shift+Alt+C   (clip policy)
Take the latest to the clipboard
------------------------------------------
Mailbox (hub: Inbox)    (3) >  items: take to the clipboard / to Downloads\gitsi, view a text, discard...;
                               send what I copied; open Downloads\gitsi; refresh
Repos              1 to sync >  each working folder with its state: sync, publish (edge), open the folder, open a terminal;
                               sync all; get a repo from the hub...; refresh
Services (edge)       1 open >  each service: check = tunnel open on localhost; close all; refresh
Jobs (edge)        1 running >  each job with its state: show its output, stop...; refresh
Edges (hub)      1 listening >  who listens with watch; publish this version to the edges...
------------------------------------------
Check the connection (hub: Check this hub)      the report of gitsi check in a dialog
Settings                   >  send every copy; update automatically, keep origin mirrors up to date (edge) /
                               start when I log on (hub); hotkey;
                               Features > each policy with its check mark; open the configuration file
Open the log
------------------------------------------
Quit
```

The icon is gitsi's mark with a dot for the state: green connected, amber retrying, blue on the hub. The menu is a native popup
menu (keyboard, submenus, closing on a click elsewhere, Windows 11's rounded border and shadow) whose entries gitsi draws itself
(owner-draw), because a native entry is one line of one color: the title's two lines (the state in bold, the last item or who
listens in grey), the grey right-hand column, the mailbox's count as a badge, the hover and the glyphs, in the dark or light
theme Windows uses. The mark and the glyphs are drawn in code (`internal/desktop`).
The executable carries the same mark as its icon (`cmd/gitsi/rsrc_windows_<arch>.syso`, made by `go generate ./cmd/gitsi`).

## 3. Where each list comes from

| List | Read with | Read again |
|---|---|---|
| Mailbox | `gitsi --json take -list` | outbox notice, after each action, Refresh, menu opened with a list older than 20 s |
| Repos | the working folders recorded by `repo share`, `repo get` and `repo sync` (`~/.gitsi/workdirs.json`, at most 50; a folder that no longer exists is forgotten), `git fetch` and `rev-list` in each; `gitsi --json repo list` for "Get a repo" | repo notice, after sync/publish/get, Refresh, menu opened after a minute |
| Services | `gitsi --json svc ls` | Refresh, menu opened after 20 s |
| Jobs | `gitsi --json job ls` (exec policy) | finished-job notice, after stop, Refresh, menu opened after 20 s |
| Edges | the hub's own state files (`watch.Edges`), no child | menu opened after 5 s |

Settings and features are read from the configuration on disk each time the menu is built: the menu itself changes them.

## 4. Actions

- Taking, discarding, sending: `take -clip <id>`, `take <id>`, `take -show <id>`, `take -rm <id>` (after a yes), `send`.
- Repos: `repo sync` and `repo publish` (after a yes) run in the folder; "Get a repo" asks for a parent folder in the system's
  dialog and runs `repo get <name> -dest <parent>\<name>`, which records the new folder. "Open a terminal here" opens Windows
  Terminal, or PowerShell in a console of its own; the folder goes as the working directory, never inside a command line.
- Services: `svc up <name>` runs while the entry is checked, in a job object (`procx.StartTree`): closing it ends its ssh.exe
  too. A tunnel that ends on its own is notified; one opened in a terminal shows as "open elsewhere" and is left alone.
- Jobs: `job logs <id> -tail 40` in a dialog; `job stop <id>` after a yes.
- Settings: `config set watch.auto_update`, `config set watch.mirror`, `watch -autostart on|off` (hub), `policy set <feature> on|off`. A change of
  `watch.auto_update` applies to the next new version at once; turning `clip` on or off applies when watch starts again (the
  notification says so).
- Edges: `update -publish` after a yes.

Each action's result appears as a notification (gitsi's last line); a failure, as a warning with its cause.

## 5. Tests

- `internal/desktop`: the icon's and glyphs' pixels, a real icon per state, submenus with each action at its id, glyph bitmaps
  created and freed, the struct layouts, and the executable's resource (`cmd/gitsi` checks the committed `.syso` is current).
- `internal/procx`: a tree stopped ends its grandchildren (cmd.exe and its ping.exe).
- `internal/workdirs`: record, forget missing folders, keep the newest, replace a damaged file; `internal/modules/repo` checks
  that share, get and the hub's clone record their folder, and `repo list --json`.
- `internal/modules/watchcmd`: every submenu with fake gitsi children and a fake desktop: lists, invalid ids left out, actions
  and the folder they run in, every yes/no question, tunnels opened, closed, ended on their own and closed by Quit, features,
  settings and the hub's entries.
