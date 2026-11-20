# gitsi roadmap

Planned improvements, in suggested order. When one is done it leaves this list, and whatever it decides goes to `DECISIONS.md`.

1. **Other operating systems.** The hub and the edge assume Windows (PowerShell remote scripts, the Windows clipboard, WMI jobs,
   OpenSSH Server setup). The protocol, the mailbox and the Git bridge are portable; the system-specific pieces would sit behind
   per-platform implementations of the existing interfaces.
2. **Desktop notifications for `watch`**, as an opt-in reaction.
3. **Several hubs (profiles)** selected per command.
4. **Folder sync without Git**, started by the edge, based on manifests and without silent conflicts.
