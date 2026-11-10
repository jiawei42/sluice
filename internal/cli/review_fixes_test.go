package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/jiawei42/sluice/internal/fakeserver"
	"github.com/jiawei42/sluice/internal/oauth"
	"github.com/jiawei42/sluice/internal/presets"
)

// loggedIn returns a config whose "svc" server is logged in to f.
func loggedIn(t *testing.T, f *fakeOAuth) string {
	t.Helper()
	t.Setenv("SLUICE_NO_BROWSER", "1")
	cfg := writeConfig(t, map[string]any{"svc": map[string]any{
		"url": f.srv.URL + "/mcp", "auth": map[string]any{"type": "oauth"}, "allow": []string{"echo"},
	}})
	var out bytes.Buffer
	bw := &browserWriter{t: t}
	if code := Run([]string{"--config", cfg, "login", "svc", "--timeout", "20s"}, IO{In: strings.NewReader(""), Out: &out, Err: bw}); code != 0 {
		t.Fatalf("login: %d %s %s", code, out.String(), bw.buf.String())
	}
	return cfg
}

// expireToken marks the stored access token as expired and revokes it server-side.
func expireToken(t *testing.T, f *fakeOAuth, cfg string) {
	t.Helper()
	p := filepath.Join(filepath.Dir(cfg), "tokens", "svc.json")
	var rec map[string]any
	data, _ := os.ReadFile(p)
	json.Unmarshal(data, &rec)
	rec["token"].(map[string]any)["expiry"] = "2000-01-01T00:00:00Z"
	data, _ = json.Marshal(rec)
	os.WriteFile(p, data, 0o600)
	f.revokeAll()
}

func TestOAuthConcurrentRefreshWithRotatingTokens(t *testing.T) {
	f := newFakeOAuth(t)
	cfg := loggedIn(t, f)
	expireToken(t, f, cfg)

	var wg sync.WaitGroup
	results := make([]result, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = runCLI(t, cfg, "", "call", "svc", "echo", "-a", "text=x")
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if r.code != 0 {
			t.Errorf("call %d: %d %q", i, r.code, r.err)
		}
	}
	if n := f.refreshes.Load(); n != 1 {
		t.Fatalf("want exactly one refresh across processes, got %d", n)
	}
}

func TestOAuthTransientRefreshFailureIsNotLogin(t *testing.T) {
	f := newFakeOAuth(t)
	cfg := loggedIn(t, f)
	expireToken(t, f, cfg)
	f.tokenDown.Store(true)
	r := runCLI(t, cfg, "", "call", "svc", "echo", "-a", "text=x")
	if r.code != ExitConnect || strings.Contains(r.err, "sluice login") || !strings.Contains(r.err, "refresh OAuth token") {
		t.Fatalf("outage during refresh: want exit 4 with the real cause, got %d %q", r.code, r.err)
	}
	f.tokenDown.Store(false)
	if r := runCLI(t, cfg, "", "call", "svc", "echo", "-a", "text=ok"); r.code != 0 || strings.TrimSpace(r.out) != "ok" {
		t.Fatalf("after outage: %d %q %q", r.code, r.out, r.err)
	}
}

func TestOAuthPlain403IsNotLogin(t *testing.T) {
	f := newFakeOAuth(t)
	cfg := loggedIn(t, f)
	f.forbid.Store(true)
	r := runCLI(t, cfg, "", "call", "svc", "echo", "-a", "text=x")
	if r.code != ExitConnect || strings.Contains(r.err, "sluice login") {
		t.Fatalf("plain 403: want exit 4 without a login hint, got %d %q", r.code, r.err)
	}
}

// pasteWriter plays a user whose browser is on another machine: it opens the
// authorization URL without following the redirect to 127.0.0.1, then pastes
// the redirect address into sluice's stdin.
type pasteWriter struct {
	t   *testing.T
	in  *io.PipeWriter
	buf bytes.Buffer
}

func (p *pasteWriter) Write(b []byte) (int, error) {
	p.buf.Write(b)
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "http://") || !strings.Contains(line, "/authorize") {
			continue
		}
		go func(u string) {
			c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := c.Get(u)
			if err != nil {
				p.t.Errorf("authorize: %v", err)
				return
			}
			resp.Body.Close()
			io.WriteString(p.in, "some unrelated line\n")
			io.WriteString(p.in, resp.Header.Get("Location")+"\n")
		}(line)
	}
	return len(b), nil
}

func TestOAuthLoginByPastingRedirect(t *testing.T) {
	t.Setenv("SLUICE_NO_BROWSER", "1")
	f := newFakeOAuth(t)
	cfg := writeConfig(t, map[string]any{"svc": map[string]any{
		"url": f.srv.URL + "/mcp", "auth": map[string]any{"type": "oauth"}, "allow": []string{"echo"},
	}})
	pr, pw := io.Pipe()
	defer pw.Close()
	var out bytes.Buffer
	w := &pasteWriter{t: t, in: pw}
	if code := Run([]string{"--config", cfg, "login", "svc", "--no-browser", "--timeout", "20s"}, IO{In: pr, Out: &out, Err: w}); code != 0 {
		t.Fatalf("paste login: %d %s %s", code, out.String(), w.buf.String())
	}
	if !strings.Contains(w.buf.String(), "paste it here") {
		t.Fatalf("login should explain the paste fallback: %q", w.buf.String())
	}
	if r := runCLI(t, cfg, "", "call", "svc", "echo", "-a", "text=pasted"); r.code != 0 || strings.TrimSpace(r.out) != "pasted" {
		t.Fatalf("call after paste login: %d %q %q", r.code, r.out, r.err)
	}
}

func TestAddForceReplacesJSONFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	os.WriteFile(cfg, []byte(`{"servers":{}}`), 0o600)
	os.MkdirAll(filepath.Join(dir, "servers.d"), 0o700)
	os.WriteFile(filepath.Join(dir, "servers.d", "github.json"), []byte(`{"url":"http://x","allow":[]}`), 0o600)
	if r := runCLI(t, cfg, "", "add", "github", "--force"); r.code != 0 {
		t.Fatalf("add --force: %d %q", r.code, r.err)
	}
	if _, err := os.Stat(filepath.Join(dir, "servers.d", "github.json")); !os.IsNotExist(err) {
		t.Fatal("the old github.json should be removed")
	}
	if r := runCLI(t, cfg, "", "servers"); r.code != 0 || !strings.Contains(r.out, "github") {
		t.Fatalf("config must still load: %d %q %q", r.code, r.out, r.err)
	}
}

func TestAddSkipsLoginHintWhenLoggedIn(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	os.WriteFile(cfg, []byte(`{"servers":{}}`), 0o600)
	if r := runCLI(t, cfg, "", "add", "vercel"); r.code != 0 || !strings.Contains(r.out, "sluice login vercel") {
		t.Fatalf("first add should point at login: %d %q %q", r.code, r.out, r.err)
	}
	p, _ := presets.Get("vercel")
	store := oauth.Store{Dir: filepath.Join(dir, "tokens")}
	if err := store.Save("vercel", &oauth.Record{URL: p.Server.URL, Token: &oauth2.Token{AccessToken: "test", RefreshToken: "test"}}); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t, cfg, "", "add", "vercel", "--force")
	if r.code != 0 || strings.Contains(r.out, "sluice login") {
		t.Fatalf("re-add while logged in must not ask for a login: %d %q %q", r.code, r.out, r.err)
	}
}

// countingServer serves the fake MCP server over the 2026-07-28 protocol
// (stateless), which is what sends Mcp-Method headers, and counts tools/list.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var lists atomic.Int32
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fakeserver.New() },
		&mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Method") == "tools/list" {
			lists.Add(1)
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, &lists
}

func TestToolCacheSkipsListOnlyWhenSafe(t *testing.T) {
	ts, lists := countingServer(t)
	cfg := writeConfig(t, map[string]any{"web": map[string]any{"url": ts.URL, "allow": []string{"echo", "lookup"}}})

	for i := 0; i < 2; i++ {
		if r := runCLI(t, cfg, "", "call", "web", "echo", "-a", "text=x"); r.code != 0 {
			t.Fatalf("echo: %d %q", r.code, r.err)
		}
	}
	if n := lists.Load(); n != 1 {
		t.Fatalf("echo has no header params: want 1 tools/list (first call only), got %d", n)
	}

	lists.Store(0)
	for i := 0; i < 2; i++ {
		if r := runCLI(t, cfg, "", "call", "web", "lookup", "-a", "owner=o"); r.code != 0 {
			t.Fatalf("lookup: %d %q", r.code, r.err)
		}
	}
	if n := lists.Load(); n != 2 {
		t.Fatalf("lookup mirrors params into headers: want a tools/list on every call, got %d", n)
	}

	// A stale cache that says lookup has no header params: the call is
	// rejected once, then sluice lists and retries.
	p := filepath.Join(filepath.Dir(cfg), "cache", "web.tools.json")
	data, _ := os.ReadFile(p)
	os.WriteFile(p, bytes.ReplaceAll(data, []byte(`"x-mcp-header"`), []byte(`"x-other"`)), 0o600)
	if r := runCLI(t, cfg, "", "call", "web", "lookup", "-a", "owner=stale"); r.code != 0 || strings.TrimSpace(r.out) != "owner=stale" {
		t.Fatalf("stale cache retry: %d %q %q", r.code, r.out, r.err)
	}
}

func TestStdioCallSkipsToolList(t *testing.T) {
	// With a plain allow match on a stdio server, call must not need the tool
	// list (no cache file is written by call).
	cfg := defaultCfg(t)
	if r := runCLI(t, cfg, "", "call", "fake", "echo", "-a", "text=x"); r.code != 0 {
		t.Fatalf("echo: %d %q", r.code, r.err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg), "cache", "fake.tools.json")); !os.IsNotExist(err) {
		t.Fatal("stdio call with an explicit allow should not list tools")
	}
}
