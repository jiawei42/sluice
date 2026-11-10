# AGENTS.md

Instructions for AI agents (Claude Code, Codex, Gemini CLI, Cursor, ...) working in this repository.

## What this repo is

`sluice`: a single Go binary that lets any agent or script call MCP servers
(GitHub, Vercel, Linear, ...) from the shell. Every call passes a per-server
whitelist the user controls and is written to an audit log.

- **Using sluice** (in any project): read [docs/agents/USAGE.md](docs/agents/USAGE.md),
  also printed by `sluice guide`. The short version for other projects' agent files
  is [docs/agents/SNIPPET.md](docs/agents/SNIPPET.md) (`sluice agents-snippet`).
- **Developing sluice**: the rest of this file.

## Layout

| Path | Purpose |
|---|---|
| `cmd/sluice` | entry point |
| `internal/cli` | commands, flag parsing, output cap, exit codes (+ end-to-end tests, incl. a fake OAuth server) |
| `internal/config` | config (JSONC) + `servers.d/*.jsonc`, `${env:VAR}` expansion, `.env` loading, validation |
| `internal/policy` | whitelist: deny-by-default, `deny` beats `allow`, optional `allow_readonly` |
| `internal/client` | one MCP session over stdio or Streamable HTTP (official Go SDK) |
| `internal/oauth` | `sluice login` browser flow, token store, silent refresh, agent-safe handler |
| `internal/audit` | JSONL call log, rotation, agent detection |
| `internal/presets` | built-in server catalog with conservative whitelists |
| `internal/toolcache` | per-server tool list on disk (24 h) so `call` can skip `tools/list` when safe |
| `internal/fakeserver` | test MCP server; `go run ./internal/fakeserver/cmd [-http :8080]` |
| `docs/agents` | agent docs + config templates, embedded into the binary with `go:embed` |
| `install.sh`, `install.ps1`, `.goreleaser.yaml`, `.github/workflows` | distribution |
| `private/` | git-ignored: the paid skill package and listing notes. Never commit it. |

## Commands

```bash
go build -o bin/sluice.exe ./cmd/sluice   # build (drop .exe off Windows)
go test ./...                              # all tests; cli tests build and spawn the fake server
go vet ./... && gofmt -l .
go install ./cmd/sluice                    # install to $(go env GOPATH)/bin
```

Release: tag `vX.Y.Z` and push the tag. `.github/workflows/release.yml` then
runs GoReleaser. Update `CHANGELOG.md` first.

## Rules for changes

- The whitelist is the point of this tool. Any new command that lists or calls tools
  must go through `policy.Check`, and denied tools must be refused **before**
  connecting when the decision does not depend on tool annotations.
- Every `call` path must write one audit entry (allowed or denied). Never log
  argument values unless `audit.log_args` is set.
- Normal commands must never start an interactive OAuth login. Only `sluice login`
  may. Agents get `LoginRequiredError` → exit 4.
- Exit codes (0/1/2/3/4) and the stdout/stderr split are a contract agents depend on.
  Changing them means a major version, plus updates to `docs/agents/USAGE.md`,
  `SNIPPET.md`, the skill package and the tests.
- Never print secret values: errors name missing variables, never their contents.
- Keep the binary dependency-light: standard library + the MCP Go SDK (+ x/oauth2 it brings).
- When you change behaviour agents see, update `docs/agents/USAGE.md` and `SNIPPET.md`
  (they are compiled into the binary).
- New presets: verify the endpoint (an unauthenticated request should return 401
  with `WWW-Authenticate` for OAuth servers), and ship a read-only whitelist.
- Never put real tokens in tests, templates or examples.

## Roadmap

1. ✅ CLI core, whitelist, stdio + HTTP transports, agent docs.
2. ✅ OAuth login, presets, audit log, output cap, release builds and installers.
3. Optional daemon to keep connections warm, a tool-list cache, OS keychain token
   storage, and Homebrew/Scoop packages.
