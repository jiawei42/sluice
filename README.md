# mcpx

用一個 Go 執行檔，從 shell 呼叫 MCP server（GitHub、Vercel…），並用每個 server 各自的**白名單**控制 agent 能用哪些 tool。
Claude Code、Codex、Gemini CLI 或一般 script 都用同一套方式接。

```
agent / script ──shell──▶ mcpx ──(白名單檢查)──▶ MCP server（stdio 或 HTTP）
```

## 安裝

```bash
go install github.com/jiawei42/local-mcp/cmd/mcpx@latest
```

在本 repo 內開發時則用 `go install ./cmd/mcpx`。會裝到 `$(go env GOPATH)/bin`（Windows 通常是 `%USERPROFILE%\go\bin`），請確認它在 PATH 裡。

## 設定

```bash
mcpx init
```

會建立：

- 設定檔：Windows `%AppData%\mcpx\config.json`、Linux `~/.config/mcpx/config.json`、macOS `~/Library/Application Support/mcpx/config.json`（可用 `MCPX_CONFIG` 或 `--config` 改）
- 同資料夾的 `.env`：放 token，例如 `GITHUB_TOKEN=...`

設定檔刻意放在專案外，agent 在專案裡工作時不會順手改到白名單。

GitHub token 建議用 [fine-grained PAT](https://github.com/settings/personal-access-tokens)，只開需要的 repo 和權限。

```bash
mcpx doctor          # 檢查環境變數、連線、各 server 有幾個 tool 被允許
mcpx policy github   # 列出 GitHub 所有 tool 和白名單的判斷結果，用來調整 allow/deny
```

### 白名單規則

```jsonc
"allow": ["get_*", "list_*", "search_*", "create_issue"],  // 符合才可用；空的 = 全部擋
"deny":  ["delete_*", "merge_*"],                            // 永遠擋，優先於 allow
"allow_readonly": false                                      // true = 也允許 server 自己標成 readOnlyHint 的 tool
```

- 不在白名單的 tool，`mcpx tools` 不會列出，`mcpx call` 會在**連線前**就拒絕（exit code 3），並請 agent 回頭問你。
- 白名單防的是 agent 誤用，不是惡意繞過：真正的權限邊界是 token 本身的權限，所以 token 要開最小權限。

## 使用

```bash
mcpx servers
mcpx tools github
mcpx schema github list_issues
mcpx call github list_issues -a owner=octo -a repo=app -a state=OPEN
mcpx call github get_issue '{"owner":"octo","repo":"app","issue_number":5}'
```

完整說明（給 agent 看的）：`mcpx guide`，內容同 [docs/agents/USAGE.md](docs/agents/USAGE.md)。

## 讓各家 agent 知道怎麼用

`mcpx agents-snippet` 會印出一小段說明，貼到下列任一處：

| Agent | 全域（所有專案） | 單一專案 |
|---|---|---|
| Claude Code | `~/.claude/CLAUDE.md` | `CLAUDE.md` |
| Codex | `~/.codex/AGENTS.md` | `AGENTS.md` |
| Gemini CLI | `~/.gemini/GEMINI.md` | `GEMINI.md` |

本 repo 自己的 `CLAUDE.md`、`GEMINI.md` 都只是引用 `AGENTS.md`，只需維護一份。

## 開發

```bash
go test ./...
go build -o bin/mcpx.exe ./cmd/mcpx
go run ./internal/fakeserver/cmd -http :8080   # 本地假 server，不需任何 token 就能試
```

架構與修改規則見 [AGENTS.md](AGENTS.md)。
