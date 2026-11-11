# sluice

**Governed MCP access for AI agents and scripts.** One small binary lets Claude Code,
Codex, Gemini CLI, Cursor or a shell script use MCP servers such as GitHub, Vercel,
Linear, Notion, Sentry and Supabase. Every call passes a per-server whitelist and
is written to an audit log.

```
agent / script ──shell──▶ sluice ──whitelist──▶ MCP server (stdio or HTTP, token or OAuth)
                              └──▶ audit.jsonl
```

[繁體中文說明](README.zh-TW.md)

## Why

- **Deny by default.** A tool that is not on the server's `allow` list is
  hidden from the agent and refused before anything is sent. `deny` always wins.
- **Works with every agent.** It is a CLI with stable exit codes, so any agent
  that can run shell commands can use it, and the agent's context is not filled
  with dozens of tool definitions.
- **See what happened.** `sluice audit` shows every call, allowed or denied,
  with the agent, the duration and the exit code. Argument values are excluded
  unless you opt in.
- **Safe defaults.** Presets ship read-only whitelists. Output is capped so one
  huge result cannot flood the agent's context.
- **Real auth.** Token servers read secrets from a `.env` outside your project.
  OAuth servers use a browser login with PKCE and refresh automatically. Agents
  never start a login themselves.

## Install

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/jiawei42/sluice/main/install.sh | sh
```
```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/jiawei42/sluice/main/install.ps1 | iex
```
```bash
# or, with Go 1.26+
go install github.com/jiawei42/sluice/cmd/sluice@latest
```

The scripts download the release archive for your OS and CPU, verify its
SHA-256 against `checksums.txt`, and install one binary. They need no admin
rights and send no telemetry. You can also download an archive from
[Releases](https://github.com/jiawei42/sluice/releases) and check it yourself.

## Quick start

```bash
sluice init github             # create the config and add the GitHub preset
# put a fine-grained token in GITHUB_TOKEN= in the .env file it printed
sluice doctor                  # env vars, logins, connectivity
sluice policy github           # every tool: ALLOW or deny, and why
sluice call github get_me      # try it
```

OAuth services:

```bash
sluice presets                 # github, vercel, linear, notion, sentry, supabase, context7, playwright
sluice add vercel
sluice login vercel            # opens your browser once; tokens refresh automatically
```

Your own MCP servers (an internal tool server, a database reader, ...) start
with an empty whitelist. Secrets can only be passed as environment variable
names, so no value is written into the server file:

```bash
# stdio: everything after -- is the command; --env passes ${env:VAR} (CHILD=VAR renames it)
sluice add internal --env MONGO_URI=INTERNAL_MONGO_URI --env JIRA_API_TOKEN -- /opt/tools/local-mcp
# HTTP: --bearer VAR or --header-env Header=VAR, or --oauth for servers that support it
sluice add team --url https://mcp.example.com/mcp --header-env X-Jira-Api-Token=JIRA_API_TOKEN
sluice policy internal         # see its tools, then list the safe ones in "allow" (or pass --allow)
```

`sluice doctor` warns when a server file holds something that looks like a
secret (a password in a connection string, a token, an `Authorization`
value) instead of a `${env:VAR}` reference. It names the field, never the value.

### Linux, macOS, SSH and WSL

sluice behaves the same on Linux, macOS and Windows: one static binary, and the
config lives in the platform's user config directory. `sluice login` opens the
default browser with `open` (macOS), `xdg-open` (Linux desktop) or `wslview`
(WSL). On a machine without a browser, such as an SSH session, a container or a
headless server, it prints the sign-in URL instead. Open that URL on any
machine and sign in. The browser then lands on a `http://127.0.0.1:…/callback?code=…`
page that fails to load. Copy that address and paste it into the waiting
terminal. Alternatively, set a fixed `"redirect_port"` in the server's `auth`
block and forward it with `ssh -L <port>:127.0.0.1:<port>`.

## For agents

```bash
sluice servers
sluice tools github
sluice schema github list_issues
sluice call github list_issues -a owner=octo -a repo=app -a state=OPEN
sluice call github get_file_contents '{"owner":"octo","repo":"app","path":"README.md"}'
sluice call github list_commits -a owner=octo -a repo=app --out commits.json
```

| Exit | Meaning |
|---|---|
| 0 | ok |
| 1 | the tool returned an error |
| 2 | usage or config error |
| 3 | blocked by the whitelist (the agent must ask you, not work around it) |
| 4 | connection problem or login needed |

`sluice guide` prints the full agent guide ([docs/agents/USAGE.md](docs/agents/USAGE.md)).
`sluice agents-snippet` prints a short block for `AGENTS.md`, `CLAUDE.md` or `GEMINI.md`.

## Configuration

`sluice init` creates the config directory: `%AppData%\sluice` on Windows,
`~/.config/sluice` on Linux, `~/Library/Application Support/sluice` on macOS.
Set `SLUICE_CONFIG` or pass `--config` to use another location.

| Path | Contents |
|---|---|
| `config.json` | global settings (`audit`, `max_output`), optional hand-written servers |
| `servers.d/<name>.jsonc` | one server per file, including its whitelist (`sluice add` writes these) |
| `.env` | secrets referenced as `${env:NAME}` |
| `tokens/` | OAuth tokens from `sluice login` |
| `audit.jsonl` | the call log (`sluice audit`) |

A server entry:

```jsonc
{
  "url": "https://api.githubcopilot.com/mcp/",          // or "command"/"args" for stdio
  "headers": { "Authorization": "Bearer ${env:GITHUB_TOKEN}" },
  "allow": ["get_*", "list_*", "search_*", "issue_read", "pull_request_read"],
  "deny":  ["delete_*", "merge_*"],
  "allow_readonly": false,                               // also admit tools the server marks read-only
  "timeout": "60s"
  // OAuth instead of a header: "auth": { "type": "oauth" }
}
```

## Security model

Read [SECURITY.md](SECURITY.md). In short, the whitelist protects you from an
agent's mistakes. Your token's permissions decide what is possible at all, so
keep tokens minimal.

## Development

```bash
go test ./...
go build -o bin/sluice ./cmd/sluice
go run ./internal/fakeserver/cmd -http :8080      # local MCP server for experiments
```

Layout and contribution rules: [AGENTS.md](AGENTS.md). License: MIT.
