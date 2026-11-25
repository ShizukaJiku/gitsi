# Shared clipboard between hub and edge

## 1. Problem

Moving a text, an image or a small file between the two machines took four steps: copy, `send`, `take` on the other side, paste.
With the shared clipboard, copying on one machine and pasting on the other is **almost immediate** and needs at most one key more
than a local copy and paste, in both directions.

Only the **latest** copy matters: what travels is a single temporary slot that each new copy replaces, not mailbox items.

## 2. How it is used

```powershell
gitsi policy set clip on      # on EACH machine: the clipboard is shared only where it is enabled
gitsi watch -tray             # on the hub and on the edge: the shared clipboard lives in watch (in a terminal or in the tray)
gitsi watch -autostart on     # HUB only: start 'gitsi watch -tray' when you log on
```

Then: Ctrl+C on one machine, **Ctrl+Shift+Alt+C** (the hotkey: "send what I copied"), and Ctrl+V on the other.

| Topic | Behavior |
|---|---|
| Sending | With the **hotkey** by default. `gitsi config set clip.auto on` (or Settings > "Send every copy" in the tray menu) sends every copy on its own. |
| Receiving | Always automatic: what arrives is written to the clipboard; the user only presses Ctrl+V. There is no paste hotkey. |
| Hotkey | `clip.hotkey`, `ctrl+shift+alt+c` by default, with at least two modifiers. Ctrl+Alt+C / Ctrl+Alt+V are taken by Office (copyright sign, paste special) and Ctrl+Alt is AltGr on many layouts. A hotkey another program holds is reported; receiving keeps working. |
| Size | Text, image and files up to `clip.max_size_mb` (20 MB by default, 256 at most); larger content is reported and not sent. Each side applies its own limit to what it receives. |
| Transport | The `watch` session the edge already holds (ssh). No new protocol, port or listener. |
| Hub process | `watch` on the hub, in the tray, may start on logon (`-autostart`). |
| Edge process | `watch` on the edge, in a terminal or in the tray, **always started by hand**; it quits from its terminal (Ctrl+C) or its menu. |

## 3. How it works

```
EDGE: watch                                             HUB
  hotkey / auto --KindClip frames--> serve --stdio ---> <hub home>/clip   (one slot, replaced)
                                     (watch session)          |
                                                              v
                                                       watch (hub) --> hub clipboard

  edge clipboard <--KindClip frames-- serve --stdio <-- <hub home>/clip <-- watch (hub) <-- hotkey / auto
```

- **Why a process on the hub**: `serve --stdio` runs in the sshd session, which has no desktop and cannot touch the user's
  clipboard; a Windows service cannot either (session 0). Only a process in the user's interactive session can. That process is
  `watch` on the hub, which there does only local work (it opens no ssh session).
- **The slot** is the meeting point on the hub: `<hub home>/clip/slot.json` (id, kind, name, size, sha256, origin, time) plus the
  content in `<id>.bin`, written content first and `slot.json` then replaced atomically: whoever reads it sees the old copy or the
  new one, never half. The origin (`hub`, or `<user>@<machine>` of the edge, set by the hub and never taken from what an edge
  claims) avoids handing a copy back to whoever produced it.
- **Hub -> edge**: the edge's watch session checks `slot.json`'s date every 100 ms and, when it changes and the origin is not that
  edge, sends the content in the same session. The edge still pulls: it asked for the clipboard when it opened the session, and the
  hub never connects to it.
- **Edge -> hub**: the edge sends the content in its open watch session; the hub's session stores it in the slot and confirms it
  (`clip.stored`) or says why not (`clip.refused`); the hub's `watch` sees the new slot (same 100 ms check) and writes the hub's
  clipboard.
- **Only what is copied while watch runs**: each side ignores what the slot held before its watch started. The edge says how long
  ago it started (a duration, so the two clocks need not agree) and which item it already received: a reconnection neither repeats
  nor loses the latest copy.
- **Expiry**: a slot older than 10 minutes is not delivered and is deleted (nothing piles up): a copy made an hour ago never lands
  on a clipboard by surprise.

Measured on one machine with the hub in the tray: the hotkey puts a text in the slot in under 0.1 s, and a slot item reaches the
clipboard in under 0.1 s. Over ssh add one round trip in the open session; files and images add their transfer time.

## 4. Copies, the hotkey and the tray (Windows)

Everything with `syscall` from the standard library (`internal/desktop`), in one thread locked with `runtime.LockOSThread` that runs
a message loop on a hidden window:

| Call | For |
|---|---|
| `RegisterHotKey` | the send hotkey |
| `AddClipboardFormatListener` | auto mode: Windows notifies each copy, no polling. A 300 ms wait absorbs programs that write the clipboard in several steps. |
| `GetClipboardSequenceNumber` | echo suppression (below) |
| `OpenClipboard`, `GetClipboardData`, `SetClipboardData` | reading and writing **text** directly, in milliseconds |
| `Shell_NotifyIcon`, `TrackPopupMenu` | the tray icon, its menu and its notifications |

Images and files go through `internal/clipboard` (PowerShell `-STA`), as `send` and `take` do: starting PowerShell takes most of a
second, which only text, the common case, avoids.

**Echo**: writing what arrives changes the clipboard and, in auto mode, would send it back forever. After each own write, watch
saves the change counter; a change with that number is not even read. Besides, the text just received and the files left by the
last item received are not sent back.

**A copy written twice**: some programs write the same copy again a moment later (screenshot tools add formats to the image about
a second after putting it there), past the 300 ms wait. Auto mode does not send again the content it sent less than 5 s before;
later it is a new copy and goes, and the hotkey always sends.

**Privacy in auto mode**: password managers mark what they copy with the formats `ExcludeClipboardContentFromMonitorProcessing`,
`CanIncludeInClipboardHistory = 0` or `CanUploadToCloudClipboard = 0` (the Windows clipboard history convention). Auto mode does
not send content with any of them. The hotkey is an explicit act and sends it anyway.

## 5. Content

| Kind | Sent as | Written on the other side as |
|---|---|---|
| Text | UTF-8 | text |
| Image | PNG | image and file, like `take` |
| Files / folders | a gzip-compressed tar with one top-level entry per item copied in Explorer (`internal/archive`), verified with SHA256 | paths in `%LOCALAPPDATA%\gitsi\clipboard\<id>\` (7-day retention), pasted as files in Explorer |

`clip.max_size_mb` applies to the packed size; copied files are measured before packing, so a folder of several GB is refused
without walking it whole.

## 6. Configuration and policy

```json
"clip": {
  "hotkey": "ctrl+shift+alt+c",
  "auto": false,
  "max_size_mb": 20
}
```

- The `clip` policy (off by default) is required on **each** machine, the hub included: enabling `send` for manual use does not
  start sending on a hotkey. The hub reads its own policy live (at most once a second): turning it on or off applies to the
  sessions already open, without the edges reconnecting; while it is off the hub refuses what edges send and delivers nothing.
- Each item sent or received leaves a line in `audit.log` (kind, size, sha256, id, origin), never the content.
- Configuration changes apply when watch restarts, except Settings > "Send every copy" in the tray menu, which also saves `clip.auto`.

## 7. The process: terminal or tray

- `watch -tray` starts the same binary detached (no console, its own process group) and releases the terminal. Without `-tray`,
  watch stays in the terminal and the clipboard works the same.
- One watch per profile (a named lock `Local\gitsi-watch-<hash of the configuration's path>`), in the tray or in a terminal: a
  second one says so and exits.
- The tray menu has "Send what I copied" (the hotkey's action) and, in Settings, "Send every copy" (`clip.auto`); the rest of
  the menu (mailbox, repos, services, jobs, edges, features) is described in `TRAY.md`. The tooltip shows the state and the last
  item. Problems (a hotkey taken, an item too large or refused) also appear as notifications.
- In the tray, watch's lines go to `%LOCALAPPDATA%\gitsi\watch.log`, which becomes `watch.log.1` past 1 MiB.
- Reconnection, reactions and the restart after `auto_update` stay as in `WATCH.md`; after an update the tray process starts a new
  one in the tray.
- **Hub only**: `watch -autostart on|off` adds or removes the value `gitsi-watch` in `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
  which starts `watch -tray` on logon. Windows opens a console for an instant before the detached start. On the edge the option is
  refused (`PHILOSOPHY.md`: nothing starts on its own there).
- On the hub, `watch` only shares the clipboard; `gitsi edges` still lists the edges.

## 8. Protocol

- `clip` capability in the greeting: every hub from 1.3.0 on announces it, whatever its policy (which is checked live); an older
  hub does not, and watch says to update it.
- The edge asks for the clipboard in its `Watch` request (`clip`, `clip_age_ms`, `clip_seen`, `clip_max`).
- Frames `KindClip` (the item: id, kind, name, count, size, sha256, origin, time) followed by `KindClipData` (the content, in
  blocks of 1 MiB), in both directions inside the watch session. The receiver checks the SHA256 before storing or writing.
- The watch session carries frames both ways: the hub's session reads what the edge sends (through `hub.SessionHandler`, which
  owns the rest of the session) while a single goroutine on each side writes, so notices, heartbeats and clipboard items never
  interleave.
- An invalid or too large item is refused after its content is read and discarded: the session stays in sync and goes on; the
  sender is told why (`clip.refused`).

## 9. Tests

- `internal/clip`: echo suppression (by the counter alone, by content), debounce, size limit, privacy, each kind both ways, the
  slot (replacement, expiry, tampered content) and the wire (blocks, refusals that keep the session in sync).
- `internal/watch`: both directions over in-memory pipes with the real `hub.Serve`, the origin set by the hub, no repeats across a
  reconnection, nothing from before watch started, refusals with their reason.
- `internal/desktop`: hotkey parsing, struct sizes, the lock, the Run key over a temporary key, and the real loop against Windows
  (listener, counter, private formats, direct text, tray, hotkey) with `GITSI_REAL_CLIPBOARD=1`. The menu's own tests are listed
  in `TRAY.md`.
- `internal/modules/watchcmd`: the hub's watch with a fake desktop and clipboard (hotkey to the slot, slot to the clipboard, the
  tray menu, `-autostart`, a second instance, the log).
- `scripts/e2e-ssh.ps1`: a clipboard item from the edge reaches the hub's slot and one from the hub reaches the edge, over a real
  sshd and the real hub binary.
