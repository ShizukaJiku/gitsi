# gitsi - notes for AI assistants

Follow [`CONTRIBUTING.md`](CONTRIBUTING.md): it holds the principles, architecture rules, code style, tests and release process,
and they apply to every change. Before changing something, read the documents it lists that apply.

## Working in this repository
- Before each commit: `go vet ./...` and `go test ./... -count=1`; when touching ssh, the protocol, `serve`, jobs or `watch`, also
  `scripts/e2e-ssh.ps1`; when touching concurrency, `scripts/race.ps1`.
- Do not push, tag or publish releases unless the user asks.
- Confirm before deleting user data (mailbox items, repos, configuration).
- In a Bash tool on Windows, heredocs and `sed` may alter backslashes and `\n` inside strings: write such content with file tools
  or a script file, and check the result.
- Windows PowerShell 5.1: run native commands with `2>&1` only when the error preference allows it (with `Stop`, stderr output
  becomes an error), use `-LiteralPath` for paths with brackets, and write files with an explicit encoding.
