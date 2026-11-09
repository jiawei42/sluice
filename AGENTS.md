# AGENTS.md

Instructions for AI agents (Claude Code, Codex, Gemini CLI, ...) working in this repository.

## What this repo is

`mcpx`: a single Go binary that lets any agent or script call MCP servers
(GitHub, Vercel, ...) from the shell, behind a per-server whitelist the user controls.

- **Using mcpx** (in any project): read [docs/agents/USAGE.md](docs/agents/USAGE.md),
  also printed by `mcpx guide`. The short version for other projects' agent files
  is [docs/agents/SNIPPET.md](docs/agents/SNIPPET.md) (`mcpx agents-snippet`).
- **Developing mcpx**: the rest of this file.

## Layout

| Path | Purpose |
|---|---|
| `cmd/mcpx` | entry point |
| `internal/cli` | commands, flag parsing, output, exit codes (+ end-to-end tests) |
| `internal/config` | config file (JSONC), `${env:VAR}` expansion, `.env` loading, validation |
| `internal/policy` | whitelist: deny-by-default, `deny` beats `allow`, optional `allow_readonly` |
| `internal/client` | one MCP session over stdio or Streamable HTTP (official Go SDK) |
| `internal/fakeserver` | test MCP server; `go run ./internal/fakeserver/cmd [-http :8080]` |
| `docs/agents` | agent docs + config templates, embedded into the binary with `go:embed` |

## Commands

```bash
go build -o bin/mcpx.exe ./cmd/mcpx      # build (drop .exe off Windows)
go test ./...                            # all tests; cli tests build and spawn the fake server
go vet ./...
go install ./cmd/mcpx                    # install to $(go env GOPATH)/bin
```

## Rules for changes

- The whitelist is the point of this tool. Any new command that lists or calls tools
  must go through `policy.Check`, and denied tools must be refused **before**
  connecting when the decision does not depend on tool annotations.
- Exit codes (0/1/2/3/4) and the stdout/stderr split are a contract agents depend on.
  Changing them means updating `docs/agents/USAGE.md` and the tests.
- Never print secret values: errors name missing variables, never their contents.
- Keep the binary dependency-light: standard library + the MCP Go SDK.
- When you change behaviour agents see, update `docs/agents/USAGE.md` and `SNIPPET.md`
  (they are compiled into the binary).
- Never put real tokens in tests, templates or examples.

## Roadmap

1. ✅ CLI core, whitelist, stdio + HTTP transports, agent docs.
2. Vercel (remote MCP, OAuth login flow stored in the user config dir).
3. Optional daemon to keep connections warm, tool-list cache, release builds per OS.
