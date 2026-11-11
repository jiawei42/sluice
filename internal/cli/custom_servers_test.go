package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func emptyConfig(t *testing.T) (dir, cfg string) {
	t.Helper()
	dir = t.TempDir()
	cfg = filepath.Join(dir, "config.json")
	os.WriteFile(cfg, []byte(`{"servers":{}}`), 0o600)
	return dir, cfg
}

func TestAddCustomStdioServer(t *testing.T) {
	dir, cfg := emptyConfig(t)
	r := runCLI(t, cfg, "", "add", "mine", "--env", "MINE_TOKEN", "--env", "DB_URI=MINE_DB_URI", "--", fakeBin, "--flag")
	if r.code != 0 {
		t.Fatalf("add custom: %d %q %q", r.code, r.out, r.err)
	}
	if !strings.Contains(r.out, "nothing is allowed yet") || !strings.Contains(r.out, "next: sluice policy mine") {
		t.Fatalf("add should explain the empty whitelist: %q", r.out)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "servers.d", "mine.jsonc"))
	file := string(data)
	for _, want := range []string{`"transport": "stdio"`, `"--flag"`, `"MINE_TOKEN": "${env:MINE_TOKEN}"`, `"DB_URI": "${env:MINE_DB_URI}"`, `"allow": []`, "custom server"} {
		if !strings.Contains(file, want) {
			t.Errorf("servers.d/mine.jsonc missing %s:\n%s", want, file)
		}
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(env), "\nMINE_TOKEN=\n") || !strings.Contains(string(env), "\nMINE_DB_URI=\n") {
		t.Fatalf(".env placeholders missing: %q", env)
	}

	t.Setenv("MINE_TOKEN", "x")
	t.Setenv("MINE_DB_URI", "x")
	if r := runCLI(t, cfg, "", "call", "mine", "echo", "-a", "text=hi"); r.code != ExitDenied {
		t.Fatalf("a new custom server must allow nothing, got %d %q", r.code, r.err)
	}
	if r := runCLI(t, cfg, "", "add", "mine", "--force", "--allow", "echo", "--deny", "delete_*", "--", fakeBin); r.code != 0 {
		t.Fatalf("re-add with --allow: %d %q", r.code, r.err)
	}
	if r := runCLI(t, cfg, "", "call", "mine", "echo", "-a", "text=hi"); r.code != 0 || strings.TrimSpace(r.out) != "hi" {
		t.Fatalf("allowed call: %d %q %q", r.code, r.out, r.err)
	}
	if r := runCLI(t, cfg, "", "call", "mine", "delete_item", "-a", "id=1"); r.code != ExitDenied {
		t.Fatalf("denied call: %d %q", r.code, r.err)
	}
}

func TestAddCustomHTTPServer(t *testing.T) {
	dir, cfg := emptyConfig(t)
	r := runCLI(t, cfg, "", "add", "o2ai", "--url", "https://mcp.example.com/mcp",
		"--bearer", "O2AI_TOKEN", "--header-env", "X-Jira-Api-Token=JIRA_API_TOKEN", "--allow", "get_*")
	if r.code != 0 {
		t.Fatalf("add http: %d %q %q", r.code, r.out, r.err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "servers.d", "o2ai.jsonc"))
	file := string(data)
	for _, want := range []string{`"url": "https://mcp.example.com/mcp"`, `"Authorization": "Bearer ${env:O2AI_TOKEN}"`, `"X-Jira-Api-Token": "${env:JIRA_API_TOKEN}"`, `"get_*"`} {
		if !strings.Contains(file, want) {
			t.Errorf("servers.d/o2ai.jsonc missing %s:\n%s", want, file)
		}
	}
	if strings.Contains(r.out, "warning:") {
		t.Fatalf("env references must not trigger the literal-secret warning: %q", r.out)
	}
	if r := runCLI(t, cfg, "", "add", "o2ai-login", "--url", "https://mcp.example.com/mcp", "--oauth"); r.code != 0 || !strings.Contains(r.out, "sluice login o2ai-login") {
		t.Fatalf("oauth custom server should point at login: %d %q %q", r.code, r.out, r.err)
	}
}

func TestAddCustomServerRejectsBadInput(t *testing.T) {
	_, cfg := emptyConfig(t)
	for name, args := range map[string][]string{
		"credentials in url":   {"add", "x", "--url", "https://user:pw@example.com/mcp"},
		"query in url":         {"add", "x", "--url", "https://example.com/mcp?token=abc"},
		"not http":             {"add", "x", "--url", "ftp://example.com"},
		"env on http":          {"add", "x", "--url", "https://example.com/mcp", "--env", "A"},
		"oauth on stdio":       {"add", "x", "--oauth", "--", "server"},
		"stdio and url":        {"add", "x", "--url", "https://example.com/mcp", "--", "server"},
		"no command":           {"add", "x", "--"},
		"--name on custom":     {"add", "x", "--name", "y", "--", "server"},
		"bad env name":         {"add", "x", "--env", "A-B", "--", "server"},
		"oauth and bearer":     {"add", "x", "--url", "https://example.com/mcp", "--oauth", "--bearer", "T"},
		"custom flag + preset": {"add", "github", "--allow", "*"},
		"unknown preset":       {"add", "nope"},
	} {
		if r := runCLI(t, cfg, "", args...); r.code != ExitUsage {
			t.Errorf("%s: want exit 2, got %d %q", name, r.code, r.err)
		}
	}
}

func TestDoctorWarnsAboutLiteralSecrets(t *testing.T) {
	const pw = "s3cretPassw0rd"
	cfg := writeConfig(t, map[string]any{"db": map[string]any{
		"command": fakeBin, "allow": []string{"echo"}, "timeout": "20s",
		"env": map[string]string{"MONGO_URI": "mongodb+srv://app:" + pw + "@cluster.example.net/", "MONGO_DB": "app"},
	}})
	r := runCLI(t, cfg, "", "doctor", "db")
	if !strings.Contains(r.out, "WARN  env.MONGO_URI") {
		t.Fatalf("doctor should warn about the literal URI: %q", r.out)
	}
	if strings.Contains(r.out, "MONGO_DB looks") || strings.Contains(r.out+r.err, pw) {
		t.Fatalf("doctor must flag only the secret, and never print it: %q %q", r.out, r.err)
	}
}
