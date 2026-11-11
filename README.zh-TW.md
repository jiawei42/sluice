# sluice（繁體中文）

**給 AI agent 與 script 用、受管控的 MCP 存取工具。**只要一個小小的執行檔，
Claude Code、Codex、Gemini CLI、Cursor 或 shell script 就能使用 GitHub、Vercel、
Linear、Notion、Sentry、Supabase 等 MCP server。每次呼叫都會先經過該 server 的
白名單檢查，並寫入稽核紀錄。

[English README](README.md)

## 特色

- **預設全部拒絕**：不在 `allow` 清單裡的 tool，agent 看不到，呼叫時也會在送出前被擋下；`deny` 永遠優先。
- **各家 agent 通用**：它是一個 CLI，exit code 固定，任何能執行 shell 指令的 agent 都能用，也不會把幾十個 tool 定義塞進 agent 的 context。
- **用過什麼都查得到**：`sluice audit` 列出每次呼叫（放行和被擋的都有），包含 agent、耗時和 exit code。參數內容預設不記錄。
- **預設就安全**：內建 preset 只開唯讀的白名單；輸出有上限，單一超大結果不會灌爆 agent 的 context。
- **完整的認證支援**：用 token 的 server，從專案外的 `.env` 讀取金鑰；用 OAuth 的 server，透過瀏覽器登入（PKCE），token 會自動更新，agent 不會自己發起登入。

## 安裝

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/jiawei42/sluice/main/install.sh | sh
```
```powershell
# Windows（PowerShell）
irm https://raw.githubusercontent.com/jiawei42/sluice/main/install.ps1 | iex
```

安裝腳本會下載對應作業系統和 CPU 的壓縮檔，比對 `checksums.txt` 裡的 SHA-256，
然後只安裝一個執行檔。不需要管理員權限，也不會回傳任何使用資料。

## 快速開始

```bash
sluice init github             # 建立設定檔並加入 GitHub preset
# 把 fine-grained token 填進畫面上提示的 .env 檔，寫在 GITHUB_TOKEN= 後面
sluice doctor                  # 檢查環境變數、登入狀態、連線
sluice policy github           # 列出每個 tool 是 ALLOW 還是 deny，以及原因
sluice call github get_me

sluice add vercel              # 加入 OAuth 服務
sluice login vercel            # 開瀏覽器登入一次，之後 token 自動更新
```

自己的 MCP server（公司內部的工具 server、資料庫查詢等）一加入時白名單是空的。
機密只能用環境變數名稱傳入，所以設定檔裡不會寫進任何密碼或 token：

```bash
# stdio：-- 後面是啟動指令；--env 會傳入 ${env:VAR}，CHILD=VAR 可以改名
sluice add internal --env MONGO_URI=INTERNAL_MONGO_URI --env JIRA_API_TOKEN -- /opt/tools/local-mcp
# HTTP：用 --bearer VAR 或 --header-env Header=VAR，支援 OAuth 的 server 用 --oauth
sluice add team --url https://mcp.example.com/mcp --header-env X-Jira-Api-Token=JIRA_API_TOKEN
sluice policy internal         # 先看有哪些 tool，再把安全的寫進 "allow"（或加 --allow）
```

如果 server 設定檔裡出現像是機密的值（連線字串裡的密碼、token、`Authorization`），
而不是 `${env:VAR}`，`sluice doctor` 會發出警告。警告只會指出欄位，不會印出值。

### Linux、macOS、SSH 與 WSL

三個平台的用法完全一樣，都是一個靜態執行檔，設定檔放在各平台的使用者設定目錄。
`sluice login` 會自動開啟預設瀏覽器：macOS 用 `open`，Linux 桌面用 `xdg-open`，WSL 用 `wslview`。
如果在沒有瀏覽器的機器上，例如 SSH 連線、container 或無介面的伺服器，sluice 會改成印出登入網址。
你在任何一台電腦打開這個網址並完成登入後，瀏覽器會停在一個打不開的
`http://127.0.0.1:…/callback?code=…` 頁面，把網址列上的這串網址整個複製，
貼回正在等待的終端機即可。另一種做法是在 server 的 `auth` 區塊設定固定的
`"redirect_port"`，再用 `ssh -L <port>:127.0.0.1:<port>` 把這個 port 轉發過去。

## 給 agent 的說明

`sluice guide` 會印出完整的 agent 指南；`sluice agents-snippet` 會印出一小段說明，
可以貼進 `AGENTS.md`、`CLAUDE.md` 或 `GEMINI.md`。

| Exit | 意義 |
|---|---|
| 0 | 成功 |
| 1 | tool 回報錯誤 |
| 2 | 用法或設定錯誤 |
| 3 | 被白名單擋下（agent 必須回頭問你，不能繞路） |
| 4 | 連線問題，或需要登入 |

## 設定位置

Windows 是 `%AppData%\sluice`，Linux 是 `~/.config/sluice`，macOS 是 `~/Library/Application Support/sluice`。

| 路徑 | 內容 |
|---|---|
| `config.json` | 全域設定（稽核、輸出上限） |
| `servers.d/<name>.jsonc` | 每個 server 一個檔案，含白名單 |
| `.env` | token 等機密 |
| `tokens/` | OAuth token |
| `audit.jsonl` | 呼叫紀錄 |

## 安全模型

詳見 [SECURITY.md](SECURITY.md)。簡單說：白名單防的是 agent 犯錯，真正決定能做什麼的
是 token 本身的權限，所以 token 要開到最小。
