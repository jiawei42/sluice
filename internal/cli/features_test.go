package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jiawei42/sluice/internal/audit"
	"github.com/jiawei42/sluice/internal/fakeserver"
)

func TestAddPresetAndRemove(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	os.WriteFile(cfg, []byte(`{"servers":{}}`), 0o600)
	os.Unsetenv("GITHUB_TOKEN")

	r := runCLI(t, cfg, "", "add", "github")
	if r.code != 0 {
		t.Fatalf("add: %d %s", r.code, r.err)
	}
	if _, err := os.Stat(filepath.Join(dir, "servers.d", "github.jsonc")); err != nil {
		t.Fatal("servers.d/github.jsonc not written")
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(env), "\nGITHUB_TOKEN=\n") {
		t.Fatalf(".env should get an empty GITHUB_TOKEN placeholder, got %q", env)
	}
	if r := runCLI(t, cfg, "", "servers"); !strings.Contains(r.out, "github") {
		t.Fatalf("servers should list github: %q", r.out)
	}
	if r := runCLI(t, cfg, "", "add", "github"); r.code != ExitUsage {
		t.Fatalf("second add should fail, got %d", r.code)
	}
	if r := runCLI(t, cfg, "", "add", "github", "--name", "gh-work"); r.code != 0 {
		t.Fatalf("add --name: %d %s", r.code, r.err)
	}
	env2, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if strings.Count(string(env2), "GITHUB_TOKEN=") != 1 {
		t.Fatalf("env key must not be duplicated: %q", env2)
	}
	// Missing token: call fails as a connection problem naming the variable, never sent.
	if r := runCLI(t, cfg, "", "call", "github", "get_me"); r.code != ExitConnect || !strings.Contains(r.err, "GITHUB_TOKEN") {
		t.Fatalf("want exit 4 naming GITHUB_TOKEN, got %d %q", r.code, r.err)
	}
	if r := runCLI(t, cfg, "", "remove", "gh-work"); r.code != 0 {
		t.Fatalf("remove: %d %s", r.code, r.err)
	}
	if r := runCLI(t, cfg, "", "servers"); strings.Contains(r.out, "gh-work") {
		t.Fatalf("gh-work should be gone: %q", r.out)
	}
	if r := runCLI(t, cfg, "", "presets"); !strings.Contains(r.out, "vercel") || !strings.Contains(r.out, "oauth") {
		t.Fatalf("presets output: %q", r.out)
	}
}

func TestServersDirConflict(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(map[string]any{"servers": map[string]any{"fake": stdioServer([]string{"echo"}, nil, false)}})
	os.WriteFile(cfg, b, 0o600)
	os.MkdirAll(filepath.Join(dir, "servers.d"), 0o700)
	os.WriteFile(filepath.Join(dir, "servers.d", "fake.json"), []byte(`{"command":"x","allow":[]}`), 0o600)
	if r := runCLI(t, cfg, "", "servers"); r.code != ExitUsage || !strings.Contains(r.err, "defined twice") {
		t.Fatalf("want duplicate error, got %d %q", r.code, r.err)
	}
}

func TestAuditLog(t *testing.T) {
	cfg := defaultCfg(t)
	runCLI(t, cfg, "", "call", "fake", "echo", "-a", "text=secret-value")
	runCLI(t, cfg, "", "call", "fake", "delete_item", "-a", "id=7")
	runCLI(t, cfg, "", "call", "fake", "fail")

	entries, err := audit.Read(filepath.Join(filepath.Dir(cfg), "audit.jsonl"))
	if err != nil || len(entries) != 3 {
		t.Fatalf("want 3 audit entries, got %d (%v)", len(entries), err)
	}
	if e := entries[0]; e.Decision != "allowed" || e.Exit != 0 || e.Args != nil || len(e.ArgKeys) != 1 || e.ArgsHash == "" {
		t.Fatalf("allowed entry wrong: %+v", e)
	}
	raw, _ := os.ReadFile(filepath.Join(filepath.Dir(cfg), "audit.jsonl"))
	if strings.Contains(string(raw), "secret-value") {
		t.Fatal("argument values must not be logged by default")
	}
	if e := entries[1]; e.Decision != "denied" || e.Exit != ExitDenied || !strings.Contains(e.Reason, "deny") {
		t.Fatalf("denied entry wrong: %+v", e)
	}
	if e := entries[2]; e.Exit != ExitToolError {
		t.Fatalf("tool error entry wrong: %+v", e)
	}
	r := runCLI(t, cfg, "", "audit", "--denied")
	if r.code != 0 || !strings.Contains(r.out, "fake/delete_item") || strings.Contains(r.out, "fake/echo") {
		t.Fatalf("audit --denied: %d %q", r.code, r.out)
	}
}

// Regression: over HTTP, servers using SEP-2243 parameter headers reject a
// tools/call unless the client knows the tool's schema (GitHub does this).
func TestHTTPHeaderMirroredParams(t *testing.T) {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fakeserver.New() },
		&mcp.StreamableHTTPOptions{Stateless: true})
	var mu sync.Mutex
	var callHeaders http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Method") == "tools/call" {
			mu.Lock()
			callHeaders = r.Header.Clone()
			mu.Unlock()
		}
		h.ServeHTTP(w, r)
	}))
	defer ts.Close()
	cfg := writeConfig(t, map[string]any{"web": map[string]any{"url": ts.URL, "allow": []string{"lookup"}}})
	r := runCLI(t, cfg, "", "call", "web", "lookup", "-a", "owner=octo")
	if r.code != 0 || strings.TrimSpace(r.out) != "owner=octo" {
		t.Fatalf("header-mirrored call: %d %q %q", r.code, r.out, r.err)
	}
	mu.Lock()
	defer mu.Unlock()
	if callHeaders == nil {
		t.Fatal("no tools/call request with an Mcp-Method header was seen")
	}
	if got := callHeaders.Get("Mcp-Param-Owner"); got != "octo" {
		t.Fatalf("tools/call must carry Mcp-Param-Owner: octo (protocol %q), got %q",
			callHeaders.Get("Mcp-Protocol-Version"), got)
	}
}

func TestOutputCapAndOutFile(t *testing.T) {
	cfg := defaultCfg(t)
	r := runCLI(t, cfg, "", "call", "fake", "echo", "-a", "text=abcdef", "--max-output", "3")
	if strings.TrimSpace(r.out) != "abc" || !strings.Contains(r.err, "truncated") {
		t.Fatalf("cap: out %q err %q", r.out, r.err)
	}
	f := filepath.Join(t.TempDir(), "out.txt")
	r = runCLI(t, cfg, "", "call", "fake", "echo", "-a", "text=abcdef", "--out", f, "--max-output", "3")
	got, _ := os.ReadFile(f)
	if r.code != 0 || strings.TrimSpace(string(got)) != "abcdef" || !strings.Contains(r.out, "saved_to") {
		t.Fatalf("--out: %d %q file %q", r.code, r.out, got)
	}
	if string(cutUTF8([]byte("héllo"), 2)) != "h" {
		t.Fatal("cutUTF8 must not split runes")
	}
}

// fakeOAuth is a minimal OAuth 2.1 authorization server + protected MCP endpoint.
type fakeOAuth struct {
	srv       *httptest.Server
	mu        sync.Mutex
	valid     map[string]bool
	n         atomic.Int32
	refreshes atomic.Int32
	expiresIn int
	// refresh tokens rotate: each is valid once, like Linear or Notion.
	refreshOK map[string]bool
	tokenDown atomic.Bool // token endpoint answers 500
	forbid    atomic.Bool // MCP endpoint answers a plain 403 to valid tokens
}

func newFakeOAuth(t *testing.T) *fakeOAuth {
	f := &fakeOAuth{valid: map[string]bool{}, refreshOK: map[string]bool{}, expiresIn: 3600}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	base := func() string { return f.srv.URL }

	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"resource": base() + "/mcp", "authorization_servers": []string{base()}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"issuer": base(), "authorization_endpoint": base() + "/authorize", "token_endpoint": base() + "/token",
			"registration_endpoint": base() + "/register", "response_types_supported": []string{"code"},
			"grant_types_supported":            []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		m["client_id"] = "test-client"
		m["token_endpoint_auth_method"] = "none"
		writeJSON(w, 201, m)
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		u, _ := url.Parse(q.Get("redirect_uri"))
		v := u.Query()
		v.Set("code", "the-code")
		v.Set("state", q.Get("state"))
		u.RawQuery = v.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if f.tokenDown.Load() {
			http.Error(w, "upstream outage", http.StatusInternalServerError)
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "the-code" || r.Form.Get("code_verifier") == "" {
				writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
		case "refresh_token":
			f.mu.Lock()
			ok := f.refreshOK[r.Form.Get("refresh_token")]
			delete(f.refreshOK, r.Form.Get("refresh_token")) // single use
			f.mu.Unlock()
			if !ok {
				writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			f.refreshes.Add(1)
		default:
			writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
			return
		}
		n := f.n.Add(1)
		tok, refresh := fmt.Sprintf("tok-%d", n), fmt.Sprintf("refresh-%d", n)
		f.mu.Lock()
		f.valid[tok] = true
		f.refreshOK[refresh] = true
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": f.expiresIn, "refresh_token": refresh})
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fakeserver.New() }, nil)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		ok := f.valid[tok]
		f.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, base()))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if f.forbid.Load() {
			http.Error(w, "no access to this workspace", http.StatusForbidden)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	return f
}

func (f *fakeOAuth) revokeAll() {
	f.mu.Lock()
	f.valid = map[string]bool{}
	f.mu.Unlock()
}

// browserWriter plays the human: it follows the authorization URL that
// `sluice login` prints, which redirects to sluice's loopback callback.
type browserWriter struct {
	t   *testing.T
	buf bytes.Buffer
}

func (b *browserWriter) Write(p []byte) (int, error) {
	b.buf.Write(p)
	sc := bufio.NewScanner(bytes.NewReader(p))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "http://") {
			go func(u string) {
				resp, err := http.Get(u)
				if err != nil {
					b.t.Errorf("browser: %v", err)
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}(line)
		}
	}
	return len(p), nil
}

func TestOAuthLoginCallRefreshLogout(t *testing.T) {
	t.Setenv("SLUICE_NO_BROWSER", "1")
	f := newFakeOAuth(t)
	cfg := writeConfig(t, map[string]any{"svc": map[string]any{
		"url": f.srv.URL + "/mcp", "auth": map[string]any{"type": "oauth"}, "allow": []string{"echo"},
	}})

	// Before login: agents get a clear "a human must log in" error, exit 4.
	r := runCLI(t, cfg, "", "call", "svc", "echo", "-a", "text=x")
	if r.code != ExitConnect || !strings.Contains(r.err, "sluice login svc") {
		t.Fatalf("before login: want exit 4 with login hint, got %d %q", r.code, r.err)
	}

	var out bytes.Buffer
	bw := &browserWriter{t: t}
	if code := Run([]string{"--config", cfg, "login", "svc", "--timeout", "20s"}, IO{In: strings.NewReader(""), Out: &out, Err: bw}); code != 0 {
		t.Fatalf("login failed: %d\nout: %s\nerr: %s", code, out.String(), bw.buf.String())
	}
	if !strings.Contains(out.String(), "logged in") {
		t.Fatalf("login output: %q", out.String())
	}
	tokFile := filepath.Join(filepath.Dir(cfg), "tokens", "svc.json")
	if _, err := os.Stat(tokFile); err != nil {
		t.Fatal("token not stored")
	}

	r = runCLI(t, cfg, "", "call", "svc", "echo", "-a", "text=hi")
	if r.code != 0 || strings.TrimSpace(r.out) != "hi" {
		t.Fatalf("call after login: %d %q %q", r.code, r.out, r.err)
	}

	// Expire the stored access token: sluice must refresh silently and persist the new one.
	var rec map[string]any
	data, _ := os.ReadFile(tokFile)
	json.Unmarshal(data, &rec)
	tok := rec["token"].(map[string]any)
	tok["expiry"] = "2000-01-01T00:00:00Z"
	data, _ = json.Marshal(rec)
	os.WriteFile(tokFile, data, 0o600)
	f.revokeAll()
	r = runCLI(t, cfg, "", "call", "svc", "echo", "-a", "text=again")
	if r.code != 0 || strings.TrimSpace(r.out) != "again" || f.refreshes.Load() != 1 {
		t.Fatalf("refresh: %d %q %q (refreshes=%d)", r.code, r.out, r.err, f.refreshes.Load())
	}
	data, _ = os.ReadFile(tokFile)
	if strings.Contains(string(data), "2000-01-01") {
		t.Fatal("refreshed token was not saved")
	}

	if r := runCLI(t, cfg, "", "doctor", "svc"); r.code != 0 || !strings.Contains(r.out, "logged in") {
		t.Fatalf("doctor: %d %q", r.code, r.out)
	}
	if r := runCLI(t, cfg, "", "logout", "svc"); r.code != 0 {
		t.Fatalf("logout: %d %q", r.code, r.err)
	}
	if r := runCLI(t, cfg, "", "call", "svc", "echo", "-a", "text=x"); r.code != ExitConnect || !strings.Contains(r.err, "sluice login svc") {
		t.Fatalf("after logout: %d %q", r.code, r.err)
	}
}
