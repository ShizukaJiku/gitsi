# gitsi philosophy

gitsi connects two machines with a **deliberate asymmetry**: one **serves** (the *hub*) and the other **only consumes** (the
*edge*). Everything else follows from that idea.

## Principles

### 1. Only consume, never expose
The edge **starts every connection** and **opens nothing to the outside**: no reverse shell, no reverse proxy, no remote tunnels
(`ssh -R`), no server or listener on the edge. At most it listens on `127.0.0.1` (local `-L` tunnels to consume the hub's
services). What gitsi does not want to do **cannot be asked for**: the transport interface has no reverse forwarding API and no
way to listen outside loopback.

This is a **design** decision, not a limitation: even if the edge could accept incoming connections, gitsi would not use them.
Minimal surface, no port to open or inbound rule to maintain, and a simple mental model: *the edge asks, the hub answers*.

### 2. The hub never initiates toward the edge
Every hub -> edge flow happens because the edge **pulls** it (the *mailbox* model): the hub leaves the item in its outbox and the
edge picks it up when it wants.

### 3. Each machine decides what is its own
The hub owns what belongs to it (where it keeps repos, mailbox, jobs and its binary) and **announces it**; the edge discovers it
and does not configure it. Nobody has to set or remember the other machine's paths, and moving a folder on the hub does not
require touching any edge.

### 4. Explicit opt-in, least privilege by default
Whatever moves content or runs something (mailbox, services, remote execution) comes **disabled**. It is enabled with a conscious
act (`gitsi policy set <feature> on`, which explains what it enables) and leaves a local record (`~/.gitsi/audit.log`). Git,
diagnostics and configuration need nothing enabled.

### 5. Standard, transparent pieces
gitsi uses what the system already has (`ssh`, `git`, `scp`, `powershell`): no resident agents, no daemons on the edge, no native
dependencies. Users can see and reproduce by hand everything gitsi does. Nothing runs in the background on the edge: tunnels
live in the foreground and close with Ctrl+C.

### 6. Content-agnostic
gitsi neither inspects nor judges what is shared; that is the sharer's responsibility. It guarantees **integrity** (end-to-end
SHA256, resumable transfers, never overwriting without verifying) and keeps a **record**, nothing more.

### 7. Verifiable
The diagnosis (`gitsi check`) exists so users can confirm, in their own environment, that each piece works and that the edge
listens on nothing outside loopback. What gitsi claims, it can prove.

### 8. It composes, it does not pile up
Each capability is a module with a small contract (role, preconditions, policy). The command line is organized in flows (`repo`,
`svc`, `job`) plus a few daily commands, not as a flat list of loose commands.

## What gitsi is not
- It is not a remote access tool **into the edge**: nothing enters the edge.
- It is not a reverse tunnel or reverse proxy, not even an optional or "off by default" one.
- It is not a server on the edge, nor a synchronizer with listeners on both sides.

Lifting these limits would be a change of philosophy, not one more option.

## Where it fits
Any pair of machines where one should limit itself to consuming: a workstation that may only make outbound connections and a
machine that can serve builds, containers and long jobs; a thin client and a build box.
