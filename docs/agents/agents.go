// Package agents embeds the agent-facing documentation and config templates
// so the sluice binary can print them (`sluice guide`, `sluice agents-snippet`, `sluice init`).
package agents

import _ "embed"

// Usage is the full guide for AI agents (`sluice guide`).
//
//go:embed USAGE.md
var Usage string

// Snippet is the short block to paste into a project's AGENTS.md / CLAUDE.md / GEMINI.md.
//
//go:embed SNIPPET.md
var Snippet string

// ConfigTemplate is written by `sluice init`.
//
//go:embed config.template.jsonc
var ConfigTemplate string

// EnvTemplate is written next to the config by `sluice init`.
//
//go:embed env.template
var EnvTemplate string
