package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jiawei42/local-mcp/internal/fakeserver"
)

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mcpx-test")
	if err != nil {
		panic(err)
	}
	fakeBin = filepath.Join(dir, "fakeserver")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", fakeBin, "github.com/jiawei42/local-mcp/internal/fakeserver/cmd").CombinedOutput()
	if err != nil {
		panic("building fakeserver: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// writeConfig writes servers as a config file and returns its path.
func writeConfig(t *testing.T, servers map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"servers": servers})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func stdioServer(allow, deny []string, readonly bool) map[string]any {
	return map[string]any{"command": fakeBin, "allow": allow, "deny": deny, "allow_readonly": readonly, "timeout": "20s"}
}

type result struct {
	code     int
	out, err string
}

func runCLI(t *testing.T, cfg string, stdin string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(append([]string{"--config", cfg}, args...), IO{In: strings.NewReader(stdin), Out: &out, Err: &errb})
	return result{code, out.String(), errb.String()}
}

func defaultCfg(t *testing.T) string {
	return writeConfig(t, map[string]any{
		"fake": stdioServer([]string{"echo", "list_*", "fail", "delete_*"}, []string{"delete_*"}, false),
	})
}

func TestToolsShowsOnlyAllowed(t *testing.T) {
	r := runCLI(t, defaultCfg(t), "", "tools", "fake")
	if r.code != 0 {
		t.Fatalf("code %d: %s", r.code, r.err)
	}
	for _, want := range []string{"echo", "list_items", "fail"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("tools output missing %q:\n%s", want, r.out)
		}
	}
	for _, hidden := range []string{"delete_item", "peek", "Second line"} {
		if strings.Contains(r.out, hidden) {
			t.Errorf("tools output should not contain %q:\n%s", hidden, r.out)
		}
	}
}

func TestCallWithFlagsAndJSON(t *testing.T) {
	cfg := defaultCfg(t)
	r := runCLI(t, cfg, "", "call", "fake", "echo", "-a", "text=ab", "-a", "times=3")
	if r.code != 0 || strings.TrimSpace(r.out) != "ababab" {
		t.Fatalf("got code %d out %q err %q", r.code, r.out, r.err)
	}
	r = runCLI(t, cfg, "", "call", "fake", "echo", `{"text":"x","times":2}`)
	if strings.TrimSpace(r.out) != "xx" {
		t.Fatalf("json args: %q %q", r.out, r.err)
	}
	r = runCLI(t, cfg, "\xef\xbb\xbf{\"text\":\"in\"}", "call", "fake", "echo", "-")
	if strings.TrimSpace(r.out) != "in" {
		t.Fatalf("stdin args: %q %q", r.out, r.err)
	}
	argFile := filepath.Join(t.TempDir(), "a.json")
	os.WriteFile(argFile, []byte(`{"text":"file"}`), 0o600)
	r = runCLI(t, cfg, "", "call", "fake", "echo", "@"+argFile)
	if strings.TrimSpace(r.out) != "file" {
		t.Fatalf("@file args: %q %q", r.out, r.err)
	}
}

func TestStructuredOutputIsJSON(t *testing.T) {
	r := runCLI(t, defaultCfg(t), "", "call", "fake", "list_items")
	var v struct {
		Items []struct{ Name string } `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.out), &v); err != nil || len(v.Items) != 2 {
		t.Fatalf("want JSON with 2 items, got %q (%v) err %q", r.out, err, r.err)
	}
}

func TestDenyWinsAndBlocksCall(t *testing.T) {
	r := runCLI(t, defaultCfg(t), "", "call", "fake", "delete_item", "-a", "id=1")
	if r.code != ExitDenied {
		t.Fatalf("want exit %d, got %d (out %q)", ExitDenied, r.code, r.out)
	}
	if !strings.Contains(r.err, "deny pattern") || strings.Contains(r.out, "deleted") {
		t.Fatalf("unexpected output: out %q err %q", r.out, r.err)
	}
	if r := runCLI(t, defaultCfg(t), "", "schema", "fake", "peek"); r.code != ExitDenied {
		t.Fatalf("schema of non-allowed tool: want %d got %d", ExitDenied, r.code)
	}
}

func TestToolErrorExitCode(t *testing.T) {
	r := runCLI(t, defaultCfg(t), "", "call", "fake", "fail")
	if r.code != ExitToolError || !strings.Contains(r.err, "always fails") {
		t.Fatalf("want exit 1 with message, got %d out %q err %q", r.code, r.out, r.err)
	}
}

func TestAllowReadOnly(t *testing.T) {
	cfg := writeConfig(t, map[string]any{"fake": stdioServer(nil, nil, true)})
	r := runCLI(t, cfg, "", "tools", "fake")
	if !strings.Contains(r.out, "peek") || strings.Contains(r.out, "delete_item") || strings.Contains(r.out, "fail") {
		t.Fatalf("allow_readonly tools:\n%s", r.out)
	}
	if r := runCLI(t, cfg, "", "call", "fake", "peek"); r.code != 0 || strings.TrimSpace(r.out) != "peeked" {
		t.Fatalf("call peek: %d %q %q", r.code, r.out, r.err)
	}
	if r := runCLI(t, cfg, "", "call", "fake", "delete_item", "-a", "id=1"); r.code != ExitDenied {
		t.Fatalf("delete_item should be denied, got %d", r.code)
	}
}

func TestEmptyAllowDeniesEverything(t *testing.T) {
	cfg := writeConfig(t, map[string]any{"fake": stdioServer(nil, nil, false)})
	if r := runCLI(t, cfg, "", "call", "fake", "echo", "-a", "text=x"); r.code != ExitDenied {
		t.Fatalf("want denied, got %d", r.code)
	}
}

func TestSchema(t *testing.T) {
	r := runCLI(t, defaultCfg(t), "", "schema", "fake", "echo")
	if r.code != 0 || !strings.Contains(r.out, `"inputSchema"`) || !strings.Contains(r.out, `"times"`) {
		t.Fatalf("schema: %d %q %q", r.code, r.out, r.err)
	}
}

func TestHTTPWithHeaderFromEnv(t *testing.T) {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fakeserver.New() }, nil)
	ts := httptest.NewServer(fakeserver.RequireAuth("Bearer s3cret", h))
	defer ts.Close()

	cfg := writeConfig(t, map[string]any{"web": map[string]any{
		"url":     ts.URL,
		"headers": map[string]string{"Authorization": "Bearer ${env:MCPX_TEST_TOKEN}"},
		"allow":   []string{"echo"},
	}})

	os.Unsetenv("MCPX_TEST_TOKEN")
	if r := runCLI(t, cfg, "", "call", "web", "echo", "-a", "text=x"); r.code != ExitConnect || !strings.Contains(r.err, "MCPX_TEST_TOKEN") {
		t.Fatalf("missing env: want exit 4 naming the var, got %d %q", r.code, r.err)
	}

	t.Setenv("MCPX_TEST_TOKEN", "wrong")
	if r := runCLI(t, cfg, "", "call", "web", "echo", "-a", "text=x"); r.code != ExitConnect {
		t.Fatalf("wrong token: want exit 4, got %d %q", r.code, r.err)
	}

	t.Setenv("MCPX_TEST_TOKEN", "s3cret")
	r := runCLI(t, cfg, "", "call", "web", "echo", "-a", "text=hi")
	if r.code != 0 || strings.TrimSpace(r.out) != "hi" {
		t.Fatalf("http call: %d %q %q", r.code, r.out, r.err)
	}
}

func TestDotEnvNextToConfig(t *testing.T) {
	cfg := defaultCfg(t)
	os.Unsetenv("MCPX_DOTENV_TEST")
	os.WriteFile(filepath.Join(filepath.Dir(cfg), ".env"), []byte("# c\nMCPX_DOTENV_TEST=\"hello\"\n"), 0o600)
	if r := runCLI(t, cfg, "", "servers"); r.code != 0 {
		t.Fatalf("servers: %d %s", r.code, r.err)
	}
	if got := os.Getenv("MCPX_DOTENV_TEST"); got != "hello" {
		t.Fatalf(".env not loaded, got %q", got)
	}
	os.Unsetenv("MCPX_DOTENV_TEST")
}

func TestUsageErrors(t *testing.T) {
	cfg := defaultCfg(t)
	for _, args := range [][]string{
		{"call", "nope", "echo"},
		{"call", "fake"},
		{"call", "fake", "echo", "not json"},
		{"frobnicate"},
		{"call", "fake", "echo", "--bogus"},
	} {
		if r := runCLI(t, cfg, "", args...); r.code != ExitUsage {
			t.Errorf("%v: want exit %d, got %d (%s)", args, ExitUsage, r.code, r.err)
		}
	}
}

func TestInitCreatesLoadableConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcpx", "config.json")
	var out bytes.Buffer
	if code := Run([]string{"--config", p, "init"}, IO{Out: &out, Err: &out}); code != 0 {
		t.Fatalf("init: %d %s", code, out.String())
	}
	if r := runCLI(t, p, "", "servers"); r.code != 0 || !strings.Contains(r.out, "github") {
		t.Fatalf("template config should load: %d %q %q", r.code, r.out, r.err)
	}
}
