# Security policy

## Supported versions

Security fixes are released for the latest minor version of gitsi.

## Reporting a vulnerability

Please do not open a public issue for a security problem. Report it privately through
[GitHub private vulnerability reporting](https://github.com/ShizukaJiku/gitsi/security/advisories/new), with:

- the gitsi version (`gitsi version`) and the role of the affected machine (hub or edge);
- what an attacker can do and under which conditions;
- steps to reproduce, or a proof of concept.

You will get an acknowledgement within a few days, and a fix or a mitigation plan as soon as the problem is confirmed.

## Scope

The design limits are part of the security model and are described in [`docs/PHILOSOPHY.md`](docs/PHILOSOPHY.md): the edge only
makes outbound connections and never listens outside `127.0.0.1`, the hub never initiates anything toward the edge, and features
that move content or run commands are opt-in and audited. Anything that breaks one of those limits is a vulnerability. What gitsi
does on a managed machine is described for reviewers in [`docs/SECURITY-REVIEW.md`](docs/SECURITY-REVIEW.md).
