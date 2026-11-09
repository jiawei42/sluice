# mcpx — guide for AI agents

`mcpx` lets you use MCP servers (GitHub, and whatever else the user configured)
from the shell. Use it instead of looking for built-in MCP tools. It works the
same from Claude Code, Codex, Gemini CLI or a plain script.

The user controls a whitelist per server. You only ever see and call the tools
the user allowed.

## Workflow

1. `mcpx servers` — which servers exist.
2. `mcpx tools <server>` — the tools you may use, one per line with a short description.
3. `mcpx schema <server> <tool>` — the JSON Schema of a tool's arguments. Read it before the first call of a tool; do not guess argument names.
4. `mcpx call <server> <tool> [ARGS]` — run it.

Only fetch what you need: don't run `schema` for every tool up front.

## Passing arguments

Any of these, and they can be combined (flags override keys of the JSON):

| Form | Example |
|---|---|
| JSON object | `mcpx call github get_issue '{"owner":"o","repo":"r","issue_number":5}'` |
| `-a key=value` (value parsed as JSON if valid: numbers, true/false, arrays, objects) | `mcpx call github get_issue -a owner=o -a repo=r -a issue_number=5` |
| `-s key=value` (always a string, use when a value looks like a number but must be a string) | `-s ref=1234` |
| stdin | `echo '{"owner":"o"}' \| mcpx call github list_issues -` |
| file | `mcpx call github create_issue @args.json` |

**PowerShell:** quoting JSON on the command line is error-prone. Prefer `-a`/`-s`,
or write the JSON to a file and pass `@file.json`.

## Output

- Success: the tool's result on stdout. Structured results are printed as JSON;
  text results are printed as-is (GitHub returns JSON text). Pipe it into
  whatever JSON tool exists on the machine: `jq` if installed, otherwise
  `python -c "import json,sys; ..."` or PowerShell `ConvertFrom-Json`.
- `--raw` prints the complete MCP `CallToolResult` (content blocks, isError, _meta).
- Results can be large. Filter them (`jq`, a script) and show the user a summary,
  not thousands of lines.
- `--json` gives machine-readable output for `servers` and `tools`.

## Exit codes and what to do

| Code | Meaning | What you do |
|---|---|---|
| 0 | OK | — |
| 1 | The tool ran and returned an error (message on stderr) | Read it, fix your arguments (check `schema`), retry once or twice at most. |
| 2 | Usage or config problem (unknown server/tool, bad JSON) | Fix the command. If the config itself is broken, tell the user. |
| 3 | **Blocked by the whitelist** | Stop. Do not try another route. Tell the user which tool you need and why; they decide whether to allow it. |
| 4 | Could not connect / auth failed | Run `mcpx doctor <server>` and report the result to the user. |

## Rules

- Never read, print or edit the mcpx config, its `.env`, or any token. The user manages them.
- Never bypass the whitelist: no calling the underlying server directly
  (`docker run ...github-mcp-server`, `curl` to the MCP endpoint, the vendor's REST API
  with the user's token) to reach a tool mcpx blocked.
- Read-only tools (get/list/search) are fine to call when they help the task.
- Tools that create, change or delete things (create_issue, push files, deploy, ...)
  may be allowed, but still confirm with the user first unless they asked for
  exactly that action.
- Each `mcpx call` opens a fresh connection (often 0.3–3 s). For many calls,
  write a short script that loops over `mcpx call` and prints only the summary.

## Examples

```bash
# Who am I on GitHub?
mcpx call github get_me

# Open bugs in a repo, titles only
mcpx call github list_issues -a owner=octo -a repo=app -a state=OPEN | jq -r '.issues[].title'

# What arguments does search_code take?
mcpx schema github search_code
```

```powershell
mcpx call github list_issues -a owner=octo -a repo=app -a state=OPEN | ConvertFrom-Json
```
