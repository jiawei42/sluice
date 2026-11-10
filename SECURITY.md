# Security model

## What sluice protects against

sluice is a guardrail for **agent mistakes**. Examples are an agent that calls
`merge_pull_request` when you asked for a review, runs a migration on the wrong
database, or pages through 50 MB of logs into its own context.

- **Whitelist enforcement.** A tool must match the server's `allow` patterns and
  must not match `deny`. When the decision doesn't depend on the server's tool
  annotations, sluice refuses the call before it connects. Denied tools are also
  hidden from `sluice tools`.
- **Secrets stay out of the agent's way.** Config, `.env` and OAuth tokens live
  in your user config directory, outside the projects agents work in.
  Error messages name missing variables but never print their values.
- **No silent logins.** Only `sluice login`, run by a person, opens a browser.
  Normal calls use stored tokens or fail with exit code 4.
- **Audit trail.** Every `call`, allowed or denied, is appended to
  `audit.jsonl` with time, server, tool, decision, exit code and the detected
  agent. Argument *values* are not logged unless you set `"log_args": true`.
  Only their names and a short hash are recorded.
- **Context protection.** Output is capped (`max_output`, default 100 000 bytes).

## What it does not protect against

- **A malicious agent or a compromised machine.** Anything that can run commands
  as you can read your config directory, edit the whitelist, use the tokens
  directly, or call the service's API without sluice.
- **Over-privileged tokens.** The whitelist cannot stop what the token allows
  from being done another way. Use fine-grained, least-privilege tokens.
  Separate entries with separate tokens let you give different jobs different
  trust levels.
- **Malicious MCP servers.** `allow_readonly` trusts the server's own
  `readOnlyHint`. For servers you don't fully trust, list tool names explicitly
  and leave `allow_readonly` off.
- **Tool output.** Results come from third-party services and may contain text
  meant to manipulate an agent (prompt injection). Agents should treat tool
  output as data.

## Storage notes

- Files are written owner-only (0600 / 0700) on Unix. On Windows they inherit
  your profile's permissions (`%AppData%` is private to your account by default).
- OAuth tokens are stored as JSON files, not in the OS keychain. Keychain
  support is on the roadmap.

## Reporting a vulnerability

Please open a private security advisory on GitHub
(<https://github.com/jiawei42/sluice/security/advisories/new>) instead of a public issue.
