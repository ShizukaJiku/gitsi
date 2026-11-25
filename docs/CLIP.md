# Shared clipboard between hub and edge (design)

## 1. Problem

Moving a text, an image or a small file between the two machines takes four steps today: copy, `send`, `take` on the other side,
paste. The goal is that copying on one machine and pasting on the other is **almost immediate** (under a second on a LAN) and
needs at most one key more than a local copy and paste, in both directions.

Only the **latest** copy matters: what travels is a single temporary slot that each new copy replaces, not mailbox items.

## 2. Decisions taken

| Topic | Decision |
|---|---|
| Sending | **Hotkey** by default: Ctrl+C as usual, then the hotkey sends what is copied. `clip.auto` in the config sends every copy on its own. |
| Receiving | Always automatic: what arrives is written to the clipboard; the user only presses Ctrl+V. There is no paste hotkey. |
| Hotkey | Configurable, `ctrl+shift+alt+c` by default: Ctrl+Alt+C / Ctrl+Alt+V are taken by Office (copyright sign, paste special) and Ctrl+Alt is AltGr on many layouts. |
| Size | Text, image and files up to `clip.max_size` (20 MB by default); larger content is reported and not sent. |
| Transport | The `watch` session the edge already holds (ssh). No new protocol, port or listener. |
| Hub process | Lives in the tray and may start on logon (the hub has no restriction on background processes). |
| Edge process | Lives in the tray, **always started by hand**, always visible, quits from its menu. It never starts on its own. |

The last row changes a principle (section 9).

## 3. How it works

```
EDGE: watch (tray)                                      HUB
  hotkey / auto --KindClip frames--> serve --stdio ---> <hub home>/clip/slot   (one slot, replaced)
                                     (watch session)          |
                                                              v
                                                       watch (tray, hub) --> hub clipboard

  edge clipboard <--KindClip frames-- serve --stdio <-- <hub home>/clip/slot <-- watch (tray, hub) <-- hotkey / auto
```

- **Why a process on the hub**: `serve --stdio` runs in the sshd session, which has no desktop and cannot touch the user's
  clipboard; a Windows service cannot either (session 0). Only a process in the user's interactive session can. That process is
  `watch` on the hub, which there does only local work (it opens no ssh session).
- **The slot** is the meeting point on the hub: `<hub home>/clip/slot.json` (id, kind, name, size, sha256, origin, time) plus the
  content next to it, written atomically (`fsx.WriteFileAtomic`): whoever reads it sees the old copy or the new one, never half.
  The origin (`hub` or `<user>@<machine>` of the edge) avoids handing a copy back to whoever produced it.
- **Hub -> edge**: the edge's watch session checks the slot's `slot.json` every 200 ms (a `stat`; cheap) and, when it changes and
  the origin is not this edge, sends the content in the same session. The edge still pulls: it subscribed to the clip channel
  when it opened the session, and the hub never connects to it.
- **Edge -> hub**: the edge sends the content in its open watch session; the hub's session writes the slot; the hub's `watch`
  sees it (same 200 ms check) and writes the hub's clipboard.
- **Expiry**: a slot older than 10 minutes is not delivered and is deleted (nothing piles up, D19). An edge that reconnects says
  which slot id it already has, like the rest of watch's known state, and only receives a newer one that has not expired: a
  copy made an hour ago never lands on the clipboard by surprise.

Latency budget on a LAN: hotkey -> clipboard read (~150 ms, PowerShell) -> one round trip in the open session -> slot check
(<= 200 ms) -> clipboard write (~150 ms). Under half a second for text; files and images add their transfer time.

## 4. Detecting copies and the hotkey (Windows)

Everything with `syscall` from the standard library (precedent: `IsUserAnAdmin` in `provision`), in one thread locked with
`runtime.LockOSThread` that runs a message loop on a message-only window:

| Call | For |
|---|---|
| `RegisterHotKey` | the send hotkey. If another program already holds it, watch says so on start and keeps receiving. |
| `AddClipboardFormatListener` | auto mode: Windows notifies each copy, no polling. A 300 ms debounce absorbs programs that write the clipboard in several steps. |
| `GetClipboardSequenceNumber` | echo suppression (below). |
| `Shell_NotifyIcon` | the tray icon and its menu. |

Reading and writing the content stay in `internal/clipboard` (PowerShell `-STA`), which is already tested; only change detection
is native.

**Echo**: writing what arrives changes the clipboard and, in auto mode, would send it back forever. After each own write, watch
saves the sequence number and the content's hash; a change with that number, or with the hash of the last item received, is not
sent.

**Privacy in auto mode**: password managers mark what they copy with the formats `ExcludeClipboardContentFromMonitorProcessing`,
`CanIncludeInClipboardHistory = 0` or `CanUploadToCloudClipboard = 0` (the Windows clipboard history convention). Auto mode does
not send content with any of them. The hotkey is an explicit act and sends it anyway.

## 5. Content

| Kind | Sent as | Written on the other side as |
|---|---|---|
| Text | UTF-8 | text |
| Image | PNG | image and file, like `take` (D10) |
| Files / folders | the same packing as `send` (`internal/archive`), verified with SHA256 | paths in `%LOCALAPPDATA%\gitsi\clipboard\<id>\` (7-day retention, D19), pasted as files in Explorer |

`clip.max_size` applies to the packed size of any kind. Content larger than the limit is reported in the tray (a short
notification) and in `audit.log`, and does not travel.

## 6. Configuration and policy

```json
"clip": {
  "hotkey": "ctrl+shift+alt+c",
  "auto": false,
  "max_size_mb": 20
}
```

- A separate `clip` policy (off by default), on **each** machine: enabling `send` for manual use must not start sending on a
  hotkey. Enabling it explains what it does, like the other policies.
- Each sent or received item leaves a line in `audit.log` (kind, size, sha256, direction), never the content.
- Configuration changes apply when watch restarts.

## 7. The process: tray, start and stop

- `watch -tray` relaunches the same binary detached (`CREATE_NO_WINDOW`, `DETACHED_PROCESS`) and releases the terminal; without
  `-tray`, watch stays in the foreground as today, and the clipboard works the same.
- One instance per user and machine (a named mutex `Local\gitsi-watch`): a second start says so and exits.
- The tray menu: state (connected, hub, last item), "Send clipboard now", "Auto mode" (on/off, saved to the config), "Open log",
  "Quit". The tooltip shows the state.
- In tray mode the lines watch prints go to `%LOCALAPPDATA%\gitsi\watch.log`, rotated by size (nothing piles up).
- The reconnection with growing waits, the reactions and the restart after `auto_update` stay as in `WATCH.md`; after an update
  the tray process relaunches itself in tray mode.
- **Hub only**: `watch -autostart on|off` adds or removes a value in `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` that
  starts `watch -tray` on logon. Known limit: Windows opens a console for an instant before the detached relaunch. On the edge the
  option does not exist (section 9).
- On the hub, `watch` does only the local clip work; `gitsi edges` still lists the edges.

## 8. Protocol

- `clip` capability in the greeting (a minor version: an old hub or edge simply does not offer it, and watch says what to update).
- The edge asks for the clip channel in its `Watch` request and says which slot id it already has.
- New frame `KindClip`, in both directions inside the watch session: a header (id, kind, name, size, sha256) followed by chunks of
  at most `proto.MaxPayload`; the receiver checks the SHA256 before writing the slot or the clipboard and discards it on a
  mismatch.
- The watch session stops being hub -> edge only: the hub's session reads frames while it writes notices and heartbeats. A single
  writer per side serializes events, heartbeats and clip chunks (a big transfer delays the heartbeat, so the 75 s dead-session
  window counts from the last frame of any kind).
- Each item is validated on arrival (`safe`): id, kind, a name with no path, a size within `clip.max_size` on the receiving
  side. The receiver's limit is the one that counts: an edge with a lower limit refuses what is larger.

## 9. Change of principle

`PHILOSOPHY.md` says "Nothing runs in the background on the edge". With this design it changes to:

> **Nothing hidden or unattended on the edge.** Whatever runs for long is started by hand, stays visible (a terminal or a tray
> icon) and quits from there. Nothing starts on its own: no services, scheduled tasks or logon entries.

What still does not change: the edge listens on nothing outside `127.0.0.1`, the hub never connects to the edge, and what moves
content is opt-in and audited. `CONTRIBUTING.md`, `WATCH.md` (sections 2 and 9), `DECISIONS.md` (a new decision) and
`SECURITY-REVIEW.md` (clipboard listener, global hotkey and tray icon, and why) are updated with the implementation.

## 10. Tests

- Echo suppression, expiry, debounce, size limit and privacy formats with a fake clipboard and a fake clock.
- `KindClip` both ways over in-memory pipes: chunks, SHA256 mismatch, a slot replaced during a transfer, heartbeats while a big
  item travels, reconnection with a known slot id (no repeats, no expired items).
- The slot on a temporary hub home: atomic replacement, origin, deletion on expiry.
- Hotkey parsing (`ctrl+shift+alt+c` -> modifiers and virtual key) and a clear error for invalid combinations.
- The message loop and the tray against the real Windows API only with an opt-in variable, like the real clipboard
  (`GITSI_REAL_CLIPBOARD=1`).
- `scripts/e2e-ssh.ps1`: a clip from the edge reaches the hub slot and back; `scripts/race.ps1` for the session's reader and writer.

## 11. Implementation order

1. Slot on the hub and `KindClip` in the watch session (with a test command path, no tray yet): clip works from a terminal.
2. Message loop: hotkey, clipboard listener, echo suppression; `clip.auto`.
3. Tray, detached relaunch, single instance, log file; `watch -autostart` on the hub.
4. Principle change and documents (section 9).
