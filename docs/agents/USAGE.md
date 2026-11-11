# sluice — guide for AI agents

`sluice` lets you use MCP servers (GitHub, Vercel, Linear, ... whatever the user
configured) from the shell. Use it instead of looking for built-in MCP tools. It
works the same from Claude Code, Codex, Gemini CLI, Cursor or a plain script.

The user controls a whitelist per server. You only ever see and call the tools
the user allowed, and every call is written to the user's audit log.

## Workflow

1. `sluice servers` — which servers exist.
2. `sluice tools <server>` — the tools you may use, one per line with a short description.
3. `sluice schema <server> <tool>` — the JSON Schema of a tool's arguments. Read it before the first call of a tool; do not guess argument names.
4. `sluice call <server> <tool> [ARGS]` — run it.

Only fetch what you need: don't run `schema` for every tool up front.

## Passing arguments

Any of these, and they can be combined (flags override keys of the JSON):

| Form | Example |
|---|---|
| JSON object | `sluice call github get_issue '{"owner":"o","repo":"r","issue_number":5}'` |
| `-a key=value` (value parsed as JSON if valid: numbers, true/false, arrays, objects) | `sluice call github get_issue -a owner=o -a repo=r -a issue_number=5` |
| `-s key=value` (always a string, use when a value looks like a number but must be a string) | `-s ref=1234` |
| stdin | `echo '{"owner":"o"}' \| sluice call github list_issues -` |
| file | `sluice call github create_issue @args.json` |

**PowerShell:** quoting JSON on the command line is error-prone. Prefer `-a`/`-s`,
or write the JSON to a file and pass `@file.json`.

## Output

- Success: the tool's result on stdout. Structured results are printed as JSON;
  text results are printed as-is (GitHub returns JSON text). Pipe it into
  whatever JSON tool exists on the machine: `jq` if installed, otherwise
  `python -c "import json,sys; ..."` or PowerShell `ConvertFrom-Json`.
- Output is capped (default 100 000 bytes). When a result is cut, stdout holds the
  first part and stderr says `output truncated`. Then either narrow the query
  (filters, smaller pages) or save everything with `--out result.json` (stdout
  then prints only `{"saved_to":...,"bytes":...}`) and process the file with a script.
- `--raw` prints the complete MCP `CallToolResult` (content blocks, isError, _meta).
- Show the user a summary, not thousands of lines.
- `--json` gives machine-readable output for `servers` and `tools`.

## Exit codes and what to do

| Code | Meaning | What you do |
|---|---|---|
| 0 | OK | — |
| 1 | The tool ran and returned an error (message on stderr) | Read it, fix your arguments (check `schema`), retry once or twice at most. |
| 2 | Usage or config problem (unknown server/tool, bad JSON) | Fix the command. If the config itself is broken, tell the user. |
| 3 | **Blocked by the whitelist** | Stop. Do not try another route. Tell the user which tool you need and why; they decide whether to allow it. |
| 4 | Could not connect, a token is missing, or **a login is needed** | If stderr says `sluice login <server>`, ask the user to run it (it opens their browser). Otherwise run `sluice doctor <server>` and report the result. |

## Rules

- Setup commands are for the user, not for you: `init`, `add`, `remove`, `login`,
  `logout`. Run them only when the user explicitly asks you to.
- Never read, print or edit the sluice config, `servers.d/`, `.env`, `tokens/`
  or any token. The user manages them. `sluice audit` is fine to run if the
  user asks what was called.
- If `sluice doctor` prints `WARN ... looks like a secret written in ...`, tell
  the user which server and field it names. They move the value to `.env` and
  reference it as `${env:VAR}`. Do not open the file yourself.
- Never bypass the whitelist: no calling the underlying server directly
  (`docker run ...github-mcp-server`, `curl` to the MCP endpoint, the vendor's REST API
  with the user's token) to reach a tool sluice blocked.
- Read-only tools (get/list/search) are fine to call when they help the task.
- Tools that create, change or delete things (create_issue, push files, deploy, ...)
  may be allowed, but still confirm with the user first unless they asked for
  exactly that action.
- Each `sluice call` opens a fresh connection (often 0.3–3 s). For many calls,
  write a short script that loops over `sluice call` and prints only the summary.

## Examples

```bash
# Who am I on GitHub?
sluice call github get_me

# Open issues in a repo, titles only
sluice call github list_issues -a owner=octo -a repo=app -a state=OPEN | jq -r '.issues[].title'

# What arguments does search_code take?
sluice schema github search_code

# Big result: save it, then work on the file
sluice call github list_commits -a owner=octo -a repo=app -a perPage=100 --out commits.json
```

```powershell
sluice call github list_issues -a owner=octo -a repo=app -a state=OPEN | ConvertFrom-Json
```
