// Package oauth adds OAuth to http servers: an interactive browser login for
// humans (`sluice login`), token storage in the user config dir, and silent
// token refresh for agents. Agents never trigger a browser login: when no
// usable token exists they get a LoginRequiredError telling a human what to run.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// Status describes the stored login for doctor output (never the token itself).
func (s Store) Status(server, url string) string {
	r, err := s.Load(server)
	switch {
	case err != nil:
		return "token file unreadable: " + err.Error()
	case r == nil || r.Token == nil:
		return "not logged in"
	case r.URL != url:
		return "logged in to a different URL; login again"
	case r.Token.RefreshToken != "":
		return "logged in (refreshable)"
	case !r.Token.Expiry.IsZero() && time.Now().After(r.Token.Expiry):
		return "login expired"
	default:
		return "logged in"
	}
}

// persistingSource saves refreshed tokens and turns refresh failures into
// LoginRequiredError.
type persistingSource struct {
	mu     sync.Mutex
	base   oauth2.TokenSource
	store  Store
	server string
	rec    *Record
}

func (p *persistingSource) Token() (*oauth2.Token, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, err := p.base.Token()
	if err != nil {
		return nil, &LoginRequiredError{Server: p.server, Cause: errors.New("token refresh failed")}
	}
	if p.rec.Token == nil || t.AccessToken != p.rec.Token.AccessToken {
		p.rec.Token = t
		_ = p.store.Save(p.server, p.rec) // best effort; the call can still proceed
	}
	return t, nil
}

func newPersistingSource(store Store, server string, rec *Record) *persistingSource {
	refreshCtx := context.Background()
	return &persistingSource{
		base:   rec.oauthConfig().TokenSource(refreshCtx, rec.Token),
		store:  store,
		server: server,
		rec:    rec,
	}
}

// agentHandler is used for normal (non-interactive) calls: it only uses the
// stored token and never starts a login.
type agentHandler struct {
	server string
	ts     oauth2.TokenSource
}

func (h *agentHandler) TokenSource(context.Context) (oauth2.TokenSource, error) { return h.ts, nil }

func (h *agentHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return &LoginRequiredError{Server: h.server, Cause: fmt.Errorf("server answered HTTP %d", resp.StatusCode)}
}

// AgentHandler returns the handler for normal calls of an OAuth server.
func AgentHandler(store Store, server string, srv *config.Server) (auth.OAuthHandler, error) {
	rec, err := store.Load(server)
	if err != nil {
		return nil, err
	}
	h := &agentHandler{server: server}
	if rec != nil && rec.Token != nil && rec.URL == srv.URL {
		h.ts = newPersistingSource(store, server, rec)
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
	OpenBrowser bool
	Version     string
}

// NewLogin prepares an interactive login: it listens on a loopback port for
// the OAuth redirect, registers a client (or uses the configured one) and
// stores the resulting token. The flow starts when the transport gets a 401.
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
	httpSrv := &http.Server{Handler: callbackHandler(results), ReadHeaderTimeout: 10 * time.Second}
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
			return newPersistingSource(store, server, rec), nil
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

func callbackHandler(results chan<- callbackResult) http.Handler {
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		var res callbackResult
		if e := q.Get("error"); e != "" {
			res.err = fmt.Errorf("authorization denied: %s %s", e, q.Get("error_description"))
		} else if q.Get("code") == "" {
			res.err = errors.New("authorization callback without a code")
		} else {
			res.res = &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		msg := "Signed in. You can close this tab and return to the terminal."
		if res.err != nil {
			msg = "Sign-in failed: " + res.err.Error()
		}
		fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>sluice</title><body style=\"font-family:sans-serif;padding:3em\"><h2>sluice</h2><p>%s</p></body>", html.EscapeString(msg))
		once.Do(func() { results <- res })
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
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
