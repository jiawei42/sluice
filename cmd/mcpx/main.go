// Command mcpx calls MCP servers from the shell so any AI agent (Claude Code,
// Codex, Gemini CLI, ...) or script can use them, behind a per-server whitelist.
package main

import (
	"os"

	"github.com/jiawei42/local-mcp/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}))
}
