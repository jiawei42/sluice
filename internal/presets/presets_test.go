package presets

import (
	"testing"

	"github.com/jiawei42/sluice/internal/policy"
)

func pol(t *testing.T, name string) policy.Policy {
	t.Helper()
	p, ok := Get(name)
	if !ok {
		t.Fatalf("preset %q missing", name)
	}
	return policy.Policy{Allow: p.Server.Allow, Deny: p.Server.Deny, AllowReadOnly: p.Server.AllowReadOnly}
}

// Tool names and readOnlyHint values observed on Vercel's hosted MCP server
// (246 tools, November 2026).
func TestVercelPresetAgainstLiveToolList(t *testing.T) {
	p := pol(t, "vercel")
	cases := []struct {
		tool     string
		readOnly bool
		want     bool
	}{
		// read-only tools the playbooks need
		{"list_projects", true, true},
		{"list_deployments", true, true},
		{"get_deployment", true, true},
		{"list_deployment_events", true, true},
		{"get_runtime_logs", true, true},
		{"get_runtime_errors", true, true},
		{"search_vercel_documentation", true, true},
		{"list_feature_flag_sdk_keys", true, true}, // key values are masked
		{"aggregate_pageviews", true, true},        // via allow_readonly
		// write tools whose names look like reads
		{"get_or_create_drive", false, false},
		{"get_access_to_vercel_url", false, false},
		// read-only, but can return secret values
		{"get_project_env", true, false},
		{"filter_project_envs", true, false},
		{"get_shared_env_var", true, false},
		{"get_edge_config_token", true, false},
		{"get_edge_config_tokens", true, false},
		// writes
		{"create_deployment", false, false},
		{"buy_domain", false, false},
		{"delete_sandbox", false, false},
		{"create_edge_config_token", false, false},
		{"update_project", false, false},
	}
	for _, c := range cases {
		ro := c.readOnly
		if got := p.Check(c.tool, &ro).Allowed; got != c.want {
			t.Errorf("vercel %s: allowed=%v, want %v (%s)", c.tool, got, c.want, p.Check(c.tool, &ro).Reason)
		}
	}
}

// Tool names observed on GitHub's hosted MCP server (46 tools).
func TestGitHubPresetAgainstLiveToolList(t *testing.T) {
	for _, name := range []string{"github", "github-docker"} {
		p := pol(t, name)
		for tool, want := range map[string]bool{
			"get_me": true, "list_issues": true, "search_code": true,
			"issue_read": true, "pull_request_read": true,
			"issue_write": false, "merge_pull_request": false, "delete_file": false,
			"push_files": false, "create_pull_request": false, "run_secret_scanning": false,
		} {
			if got := p.Check(tool, nil).Allowed; got != want {
				t.Errorf("%s %s: allowed=%v, want %v", name, tool, got, want)
			}
		}
	}
}

func TestEveryPresetIsValid(t *testing.T) {
	for _, p := range All() {
		if p.Name == "" || p.Description == "" || p.Docs == "" {
			t.Errorf("preset %q: name, description and docs are required", p.Name)
		}
		if len(p.Server.Allow) == 0 && !p.Server.AllowReadOnly {
			t.Errorf("preset %q allows nothing", p.Name)
		}
	}
}
