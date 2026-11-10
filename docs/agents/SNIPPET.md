## MCP servers via `sluice`

External services (GitHub, Vercel, Linear, ... whatever the user configured) are available through the `sluice` CLI, not as built-in tools. Run `sluice guide` once before first use for the full rules.

- Discover: `sluice servers` → `sluice tools <server>` → `sluice schema <server> <tool>`
- Call: `sluice call <server> <tool> '{"key":"value"}'` or `sluice call <server> <tool> -a key=value` (prefer `-a`/`-s` in PowerShell)
- Output goes to stdout and is capped; for big results use `--out FILE` and process the file. Show the user a summary.
- Exit code 3 = blocked by the user's whitelist: do not work around it, tell the user which tool you need and why.
- Exit code 4 = connection or login problem: if stderr names `sluice login <server>`, ask the user to run it; else run `sluice doctor <server>` and report.
- `init`, `add`, `remove`, `login`, `logout` are the user's setup commands; never read the sluice config, `.env` or tokens.
- Confirm with the user before calling tools that create, change or delete things, unless they already asked for exactly that.
