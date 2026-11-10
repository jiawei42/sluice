// Package oauth adds OAuth to http servers: an interactive browser login for
// humans (`sluice login`), token storage in the user config dir, and silent
// token refresh for agents. Agents never trigger a browser login: when no
// usable token exists they get a LoginRequiredError telling a human what to run.
package oauth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/jiawei42/sluice/internal/config"
)

// LoginRequiredError means a human has to run `sluice login <server>`.
type LoginRequiredError struct {
	Server string
	Cause  error
}

func (e *LoginRequiredError) Error() string {
	msg := fmt.Sprintf("server %q needs an OAuth login: a human must run `sluice login %s`", e.Server, e.Server)
	if e.Cause != nil {
		msg += " (" + e.Cause.Error() + ")"
	}
	return msg
}

func (e *LoginRequiredError) Unwrap() error { return e.Cause }

// Record is what is stored per server. It contains secrets: the file is 0600
// and agents are told never to read it.
type Record struct {
	URL          string        `json:"url"`
	ClientID     string        `json:"client_id"`
	ClientSecret string        `json:"client_secret,omitempty"`
	AuthURL      string        `json:"auth_url"`
	TokenURL     string        `json:"token_url"`
	AuthStyle    int           `json:"auth_style"`
	Scopes       []string      `json:"scopes,omitempty"`
	Token        *oauth2.Token `json:"token"`
	SavedAt      time.Time     `json:"saved_at"`
}

func (r *Record) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     r.ClientID,
		ClientSecret: r.ClientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: r.AuthURL, TokenURL: r.TokenURL, AuthStyle: oauth2.AuthStyle(r.AuthStyle)},
		Scopes:       r.Scopes,
	}
}

// Store keeps one token file per server.
type Store struct{ Dir string }

// NewStore returns the store under <config dir>/tokens.
func NewStore(c *config.Config) Store { return Store{Dir: filepath.Join(c.Dir(), "tokens")} }

func (s Store) path(server string) string { return filepath.Join(s.Dir, server+".json") }

// Load returns the record or nil if there is none.
func (s Store) Load(server string) (*Record, error) {
	data, err := os.ReadFile(s.path(server))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("corrupt token file for %q (run `sluice logout %s` then login again): %w", server, server, err)
	}
	return &r, nil
}

// Save writes the record atomically with owner-only permissions.
func (s Store) Save(server string, r *Record) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	r.SavedAt = time.Now().UTC()
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(server) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(server))
}

// Delete removes the record. Missing is not an error.
func (s Store) Delete(server string) error {
	err := os.Remove(s.path(server))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// lock takes an exclusive per-server lock file so that only one sluice
// process refreshes a token at a time (providers that rotate refresh tokens
// revoke the old one on first use). A lock older than 30 s is treated as left
// behind by a crashed process.
func (s Store) lock(server string) (func(), error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	p := s.path(server) + ".lock"
	deadline := time.Now().Add(15 * time.Second)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) > 30*time.Second {
			os.Remove(p)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for another sluice process to refresh the token (%s)", p)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// LoginState is the stored login for doctor output (never the token itself).
type LoginState int

const (
	NotLoggedIn LoginState = iota
	LoggedIn
	LoggedInRefreshable
	Expired
	OtherURL
	Unreadable
)

// Usable reports whether calls can proceed without a new login.
func (s LoginState) Usable() bool { return s == LoggedIn || s == LoggedInRefreshable }

func (s LoginState) String() string {
	switch s {
	case LoggedIn:
		return "logged in"
	case LoggedInRefreshable:
		return "logged in (refreshable)"
	case Expired:
		return "login expired"
	case OtherURL:
		return "logged in to a different URL; login again"
	case Unreadable:
		return "token file unreadable"
	default:
		return "not logged in"
	}
}

// Status describes the stored login for a server at url.
func (s Store) Status(server, url string) LoginState {
	r, err := s.Load(server)
	switch {
	case err != nil:
		return Unreadable
	case r == nil || r.Token == nil:
		return NotLoggedIn
	case r.URL != url:
		return OtherURL
	case r.Token.RefreshToken != "":
		return LoggedInRefreshable
	case !r.Token.Valid():
		return Expired
	default:
		return LoggedIn
	}
}

// persistingSource serves the stored token, refreshes it under the store
// lock when it expires, and saves the result.
type persistingSource struct {
	mu      sync.Mutex
	store   Store
	server  string
	rec     *Record
	onLogin func(*LoginRequiredError)
}

func (p *persistingSource) loginRequired(cause error) error {
	e := &LoginRequiredError{Server: p.server, Cause: cause}
	if p.onLogin != nil {
		p.onLogin(e)
	}
	return e
}

func (p *persistingSource) Token() (*oauth2.Token, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rec.Token != nil && p.rec.Token.Valid() {
		return p.rec.Token, nil
	}
	unlock, err := p.store.lock(p.server)
	if err != nil {
		return nil, fmt.Errorf("oauth token for %q: %w", p.server, err)
	}
	defer unlock()

	// Another sluice process may have refreshed while we waited for the lock.
	if fresh, err := p.store.Load(p.server); err == nil && fresh != nil && fresh.Token != nil && fresh.URL == p.rec.URL {
		p.rec = fresh
		if fresh.Token.Valid() {
			return fresh.Token, nil
		}
	}
	if p.rec.Token == nil || p.rec.Token.RefreshToken == "" {
		return nil, p.loginRequired(errors.New("access token expired and there is no refresh token"))
	}
	t, err := p.rec.oauthConfig().TokenSource(context.Background(), p.rec.Token).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.Response != nil && re.Response.StatusCode >= 400 && re.Response.StatusCode < 500 {
			code := re.ErrorCode
			if code == "" {
				code = re.Response.Status
			}
			return nil, p.loginRequired(fmt.Errorf("the provider rejected the refresh: %s", code))
		}
		// Network trouble or a provider outage: not fixable by logging in.
		return nil, fmt.Errorf("refresh OAuth token for %q: %w", p.server, err)
	}
	p.rec.Token = t
	if err := p.store.Save(p.server, p.rec); err != nil {
		return nil, fmt.Errorf("save refreshed token for %q: %w", p.server, err)
	}
	return t, nil
}

// AgentHandler is used for normal (non-interactive) calls: it only uses the
// stored token and never starts a login. It remembers whether it decided a
// login is needed, so callers can report that instead of a transport error.
type AgentHandler struct {
	server string
	ts     oauth2.TokenSource

	mu    sync.Mutex
	login *LoginRequiredError
}

func (h *AgentHandler) record(e *LoginRequiredError) {
	h.mu.Lock()
	h.login = e
	h.mu.Unlock()
}

// LoginRequired returns the login error this handler raised, if any.
func (h *AgentHandler) LoginRequired() *LoginRequiredError {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.login
}

func (h *AgentHandler) TokenSource(context.Context) (oauth2.TokenSource, error) { return h.ts, nil }

func (h *AgentHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	challenge := strings.Join(resp.Header.Values("WWW-Authenticate"), " ")
	if resp.StatusCode == http.StatusForbidden && h.ts != nil && !strings.Contains(challenge, "insufficient_scope") {
		// A plain 403 (no access to that resource) is not fixed by logging in:
		// let the transport retry and surface the server's own answer.
		return nil
	}
	e := &LoginRequiredError{Server: h.server, Cause: fmt.Errorf("server answered HTTP %d", resp.StatusCode)}
	h.record(e)
	return e
}

// NewAgentHandler returns the handler for normal calls of an OAuth server.
func NewAgentHandler(store Store, server string, srv *config.Server) (*AgentHandler, error) {
	rec, err := store.Load(server)
	if err != nil {
		return nil, err
	}
	h := &AgentHandler{server: server}
	if rec != nil && rec.Token != nil && rec.URL == srv.URL {
		h.ts = &persistingSource{store: store, server: server, rec: rec, onLogin: h.record}
	}
	return h, nil
}

// Login is an interactive login session for one server.
type Login struct {
	Handler auth.OAuthHandler
	close   func()
}

// Close releases the callback listener.
func (l *Login) Close() { l.close() }

// LoginOptions control the interactive flow.
type LoginOptions struct {
	Out         io.Writer // where the authorization URL and progress go
	In          io.Reader // optional: accepts the pasted redirect URL (SSH, containers)
	OpenBrowser bool
	Version     string
}

// NewLogin prepares an interactive login: it listens on a loopback port for
// the OAuth redirect, registers a client (or uses the configured one) and
// stores the resulting token. The flow starts when the transport gets a 401.
//
// When the browser runs on another machine (SSH session, container, WSL
// without a shared network), the redirect to 127.0.0.1 cannot reach sluice;
// the user can then paste the address the browser ended on into o.In.
func NewLogin(store Store, server string, srv *config.Server, o LoginOptions) (*Login, error) {
	port := 0
	if srv.Auth != nil {
		port = srv.Auth.RedirectPort
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen for OAuth callback: %w", err)
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	results := make(chan callbackResult, 1)
	var once sync.Once
	deliver := func(r callbackResult) { once.Do(func() { results <- r }) }
	httpSrv := &http.Server{Handler: callbackHandler(deliver), ReadHeaderTimeout: 10 * time.Second}
	go httpSrv.Serve(ln)

	cfg := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL:           redirect,
		RequestRefreshToken:   true,
		AcceptUnadvertisedIss: true,
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			fmt.Fprintf(o.Out, "Open this URL to sign in to %q:\n\n  %s\n\n", server, args.URL)
			if o.OpenBrowser {
				if err := openBrowser(args.URL); err == nil {
					fmt.Fprintln(o.Out, "(opened in your browser; waiting for you to finish...)")
				}
			}
			if o.In != nil {
				fmt.Fprintln(o.Out, "If your browser runs on another machine (SSH, container), finish signing in, then copy")
				fmt.Fprintf(o.Out, "the address of the page it ends on (%s?code=...) and paste it here.\n", redirect)
				go readPastedRedirect(o.In, deliver)
			}
			select {
			case r := <-results:
				if r.err != nil {
					return nil, r.err
				}
				return r.res, nil
			case <-ctx.Done():
				return nil, fmt.Errorf("login timed out: %w", ctx.Err())
			}
		},
		NewTokenSource: func(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			rec := &Record{
				URL:          srv.URL,
				ClientID:     oc.ClientID,
				ClientSecret: oc.ClientSecret,
				AuthURL:      oc.Endpoint.AuthURL,
				TokenURL:     oc.Endpoint.TokenURL,
				AuthStyle:    int(oc.Endpoint.AuthStyle),
				Scopes:       oc.Scopes,
				Token:        tok,
			}
			if err := store.Save(server, rec); err != nil {
				return nil, fmt.Errorf("save token: %w", err)
			}
			return &persistingSource{store: store, server: server, rec: rec}, nil
		},
	}
	if srv.Auth != nil && len(srv.Auth.Scopes) > 0 {
		scopes := srv.Auth.Scopes
		cfg.ScopeFilter = func([]string) []string { return scopes }
	}
	if srv.Auth != nil && srv.Auth.ClientID != "" {
		cred := &oauthex.ClientCredentials{ClientID: srv.Auth.ClientID}
		if srv.Auth.ClientSecret != "" {
			cred.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: srv.Auth.ClientSecret}
		}
		cfg.PreregisteredClient = cred
	} else {
		cfg.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				RedirectURIs:            []string{redirect},
				ClientName:              "sluice",
				ClientURI:               "https://github.com/jiawei42/sluice",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
				TokenEndpointAuthMethod: "none",
				SoftwareID:              "sluice",
				SoftwareVersion:         o.Version,
			},
		}
	}
	h, err := auth.NewAuthorizationCodeHandler(cfg)
	if err != nil {
		httpSrv.Close()
		return nil, err
	}
	return &Login{Handler: h, close: func() { httpSrv.Close() }}, nil
}

type callbackResult struct {
	res *auth.AuthorizationResult
	err error
}

func resultFromQuery(q url.Values) callbackResult {
	switch {
	case q.Get("error") != "":
		return callbackResult{err: fmt.Errorf("authorization denied: %s %s", q.Get("error"), q.Get("error_description"))}
	case q.Get("code") == "":
		return callbackResult{err: errors.New("authorization callback without a code")}
	default:
		return callbackResult{res: &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}}
	}
}

// readPastedRedirect accepts a pasted callback URL (or just its query string).
// Lines that carry neither a code nor an error are ignored.
func readPastedRedirect(in io.Reader, deliver func(callbackResult)) {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "?"); i >= 0 {
			line = line[i+1:]
		}
		q, err := url.ParseQuery(line)
		if err != nil || (q.Get("code") == "" && q.Get("error") == "") {
			continue
		}
		deliver(resultFromQuery(q))
		return
	}
}

func callbackHandler(deliver func(callbackResult)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		res := resultFromQuery(r.URL.Query())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		msg := "Signed in. You can close this tab and return to the terminal."
		if res.err != nil {
			msg = "Sign-in failed: " + res.err.Error()
		}
		fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>sluice</title><body style=\"font-family:sans-serif;padding:3em\"><h2>sluice</h2><p>%s</p></body>", html.EscapeString(msg))
		deliver(res)
	})
}

func openBrowser(url string) error {
	if os.Getenv("SLUICE_NO_BROWSER") != "" {
		return errors.New("browser disabled")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		if os.Getenv("WSL_DISTRO_NAME") != "" { // WSL: open the Windows browser
			if _, err := exec.LookPath("wslview"); err == nil {
				return exec.Command("wslview", url).Start()
			}
			return exec.Command("cmd.exe", "/c", "start", "", strings.ReplaceAll(url, "&", "^&")).Start()
		}
		// Headless Linux (SSH, servers) usually has no browser: skip quietly
		// and rely on the printed URL and the paste fallback.
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no graphical session")
		}
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
