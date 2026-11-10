// Package presets is the built-in catalog used by `sluice add`. Every preset
// ships a conservative whitelist: read-only tools are allowed, anything that
// writes, deploys or deletes must be added by the user on purpose.
package presets

import (
	"sort"

	"github.com/jiawei42/sluice/internal/config"
)

// EnvVar is a secret the preset needs.
type EnvVar struct {
	Name string
	Help string
}

// Preset is one catalog entry.
type Preset struct {
	Name        string
	Description string
	Docs        string
	Env         []EnvVar
	Notes       []string
	Server      config.Server
}

func oauth() *config.Auth { return &config.Auth{Type: "oauth"} }

var catalog = []Preset{
	{
		Name:        "github",
		Description: "GitHub (hosted): repos, issues, pull requests, code search",
		Docs:        "https://github.com/github/github-mcp-server",
		Env: []EnvVar{{"GITHUB_TOKEN",
			"fine-grained personal access token: https://github.com/settings/personal-access-tokens (grant only the repos and permissions agents need)"}},
		Server: config.Server{
			Description: "GitHub: repos, issues, pull requests, code search",
			URL:         "https://api.githubcopilot.com/mcp/",
			Headers:     map[string]string{"Authorization": "Bearer ${env:GITHUB_TOKEN}"},
			Allow:       []string{"get_*", "list_*", "search_*", "issue_read", "pull_request_read"},
			Deny:        []string{"delete_*", "merge_*"},
		},
	},
	{
		Name:        "github-docker",
		Description: "GitHub via the local Docker image (works offline from GitHub's hosted MCP; slower start-up)",
		Docs:        "https://github.com/github/github-mcp-server",
		Env:         []EnvVar{{"GITHUB_TOKEN", "fine-grained personal access token"}},
		Notes:       []string{"Requires Docker running. Each call starts a container (~1-3 s)."},
		Server: config.Server{
			Description: "GitHub (Docker): repos, issues, pull requests, code search",
			Command:     "docker",
			Args:        []string{"run", "-i", "--rm", "-e", "GITHUB_PERSONAL_ACCESS_TOKEN", "ghcr.io/github/github-mcp-server"},
			Env:         map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "${env:GITHUB_TOKEN}"},
			Allow:       []string{"get_*", "list_*", "search_*", "issue_read", "pull_request_read"},
			Deny:        []string{"delete_*", "merge_*"},
		},
	},
	{
		Name:        "vercel",
		Description: "Vercel: projects, deployments, build logs, docs (OAuth)",
		Docs:        "https://vercel.com/docs/mcp/vercel-mcp",
		Notes: []string{
			"Read-only by default. Deploys, domain purchases and project changes are not allowed.",
			"Tools that can return secrets (env var values, Edge Config tokens) are denied; allow them by exact name only if needed.",
		},
		Server: config.Server{
			Description:   "Vercel: projects, deployments, build logs, docs",
			URL:           "https://mcp.vercel.com/",
			Auth:          oauth(),
			Allow:         []string{"get_*", "list_*", "search_*"},
			AllowReadOnly: true,
			// Verified against Vercel's live tool list (246 tools):
			// get_or_create_drive and get_access_to_vercel_url write despite
			// their names; the env/token readers can return secret values.
			Deny: []string{
				"*delete*", "deploy*",
				"get_or_create_*", "get_access_to_vercel_url",
				"get_project_env", "filter_project_envs", "get_shared_env_var",
				"*token*",
			},
		},
	},
	{
		Name:        "linear",
		Description: "Linear: issues, projects, cycles, comments (OAuth)",
		Docs:        "https://linear.app/docs/mcp",
		Server: config.Server{
			Description:   "Linear: issues, projects, cycles, comments",
			URL:           "https://mcp.linear.app/mcp",
			Auth:          oauth(),
			Allow:         []string{"get_*", "list_*", "search_*"},
			AllowReadOnly: true,
			Deny:          []string{"delete_*"},
		},
	},
	{
		Name:        "notion",
		Description: "Notion: search and read pages and databases (OAuth)",
		Docs:        "https://developers.notion.com/docs/mcp",
		Server: config.Server{
			Description:   "Notion: search and read pages and databases",
			URL:           "https://mcp.notion.com/mcp",
			Auth:          oauth(),
			Allow:         []string{"notion-search", "notion-fetch", "notion-get-*", "notion-list-*", "notion-query-*"},
			AllowReadOnly: true,
			Deny:          []string{"notion-delete-*", "notion-move-*"},
		},
	},
	{
		Name:        "sentry",
		Description: "Sentry: issues, events, releases, traces (OAuth)",
		Docs:        "https://docs.sentry.io/product/sentry-mcp/",
		Server: config.Server{
			Description:   "Sentry: issues, events, releases, traces",
			URL:           "https://mcp.sentry.dev/mcp",
			Auth:          oauth(),
			Allow:         []string{"find_*", "get_*", "search_*"},
			AllowReadOnly: true,
			Deny:          []string{"update_*", "*delete*"},
		},
	},
	{
		Name:        "supabase",
		Description: "Supabase in read-only mode: tables, logs, advisors, docs (OAuth)",
		Docs:        "https://supabase.com/docs/guides/getting-started/mcp",
		Notes: []string{
			"The URL sets read_only=true so SQL runs read-only on Supabase's side.",
			"Add &project_ref=<ref> to the URL to scope it to one project.",
		},
		Server: config.Server{
			Description:   "Supabase (read-only): tables, logs, advisors, docs",
			URL:           "https://mcp.supabase.com/mcp?read_only=true",
			Auth:          oauth(),
			Allow:         []string{"list_*", "get_*", "search_docs", "generate_typescript_types"},
			AllowReadOnly: true,
			Deny:          []string{"apply_migration", "deploy_*", "delete_*", "create_*", "pause_*", "restore_*", "merge_*", "reset_*", "rebase_*"},
		},
	},
	{
		Name:        "context7",
		Description: "Context7: up-to-date library documentation (no login needed)",
		Docs:        "https://github.com/upstash/context7",
		Server: config.Server{
			Description: "Context7: up-to-date library documentation",
			URL:         "https://mcp.context7.com/mcp",
			Allow:       []string{"*"},
		},
	},
	{
		Name:        "playwright",
		Description: "Playwright: drive a local browser (needs Node.js)",
		Docs:        "https://github.com/microsoft/playwright-mcp",
		Notes:       []string{"Requires Node.js (npx). Script execution and file upload are denied by default."},
		Server: config.Server{
			Description: "Playwright: drive a local browser",
			Command:     "npx",
			Args:        []string{"-y", "@playwright/mcp@latest"},
			Allow:       []string{"browser_*"},
			Deny:        []string{"browser_evaluate", "browser_run_code", "browser_file_upload"},
			Timeout:     "120s",
		},
	},
}

// All returns the catalog sorted by name.
func All() []Preset {
	out := append([]Preset(nil), catalog...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns the named preset.
func Get(name string) (Preset, bool) {
	for _, p := range catalog {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}
