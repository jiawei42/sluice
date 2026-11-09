## MCP servers via `mcpx`

External services (GitHub, and others the user configured) are available through the `mcpx` CLI, not as built-in tools. Run `mcpx guide` once before first use for the full rules.

- Discover: `mcpx servers` → `mcpx tools <server>` → `mcpx schema <server> <tool>`
- Call: `mcpx call <server> <tool> '{"key":"value"}'` or `mcpx call <server> <tool> -a key=value` (prefer `-a`/`-s` in PowerShell)
- Output goes to stdout; filter large results (`jq`, `python`, `ConvertFrom-Json`) instead of printing them whole.
- Exit code 3 = blocked by the user's whitelist: do not work around it, tell the user which tool you need and why.
- Exit code 4 = connection/auth problem: run `mcpx doctor <server>` and report it; never read or print tokens or the mcpx `.env`.
- Confirm with the user before calling tools that create, change or delete things, unless they already asked for exactly that.
