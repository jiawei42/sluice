# Changelog

All notable changes to sluice. Versions follow [Semantic Versioning](https://semver.org/);
the CLI contract agents rely on (commands, flags, exit codes, stdout/stderr split) only
breaks in a major version.

## v1.0.0 — unreleased

First public release (the project was called `mcpx` during development).

### Added
- `sluice` CLI: `servers`, `tools`, `schema`, `call` for agents; `guide` and
  `agents-snippet` print the agent instructions.
- Per-server whitelist: deny-by-default `allow` globs, `deny` always wins,
  optional `allow_readonly`. Denied calls are refused before anything is sent.
- Transports: stdio and Streamable HTTP, with `${env:VAR}` secrets from the
  environment or a `.env` next to the config.
- OAuth for hosted MCP servers: `sluice login` (browser, PKCE, dynamic client
  registration or a pre-registered client), tokens stored in the user config
  dir, silent refresh. Agents never start a login; they get exit code 4 with
  the command a human must run.
- Presets with conservative whitelists: GitHub (hosted and Docker), Vercel,
  Linear, Notion, Sentry, Supabase (read-only), Context7, Playwright.
  `sluice presets`, `sluice add`, `sluice remove`, `sluice init <preset...>`.
- Audit log of every call (allowed and denied) in `audit.jsonl`, argument
  values excluded unless `log_args` is on; `sluice audit` to review.
- Output cap (`max_output`, default 100 000 bytes) to protect agent context,
  and `--out FILE` to save full results.
- `sluice doctor` (env vars, logins, connectivity) and `sluice policy`
  (every tool and the whitelist decision).
- Release builds for Linux, macOS and Windows (amd64, arm64) with SHA-256
  checksums, and install scripts that verify them.
- Headless logins (SSH, containers): paste the final redirect URL into the
  terminal. WSL opens the Windows browser.
- Tool-list cache (`cache/`, 24 h): `call` skips the extra `tools/list` round
  trip when the tool doesn't mirror arguments into HTTP headers, and never
  lists for stdio servers unless `allow_readonly` needs annotations.

### Fixed during review
- Token refresh is serialized across processes with a lock file, so parallel
  calls against providers that rotate refresh tokens no longer force a
  re-login. Network errors during refresh keep their real cause and are no
  longer reported as "login required".
- A plain HTTP 403 is reported as the server's error, not as a missing login.
- `add --force` over `servers.d/<name>.json` no longer leaves a duplicate.
- `.env` values ignore inline ` # comments`.
- `install.ps1` runs in a child scope, so `irm | iex` leaves the caller's
  PowerShell preferences untouched.
- Agent detection no longer reads a user's `CODEX_HOME` as "codex".
- Vercel preset, checked against the live tool list (246 tools): denies
  `get_or_create_*` and `get_access_to_vercel_url` (writes with read-like
  names) and the tools that return env var values or tokens.
