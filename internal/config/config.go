// Package config loads the sluice configuration file, expands ${env:VAR}
// references and loads the optional .env file that sits next to it.
package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultTimeout applies to a whole command (connect + request) when a server
// does not set its own timeout.
const DefaultTimeout = 60 * time.Second

// Server describes how to reach one MCP server and which of its tools may be used.
type Server struct {
	// Transport is "stdio" or "http". If empty it is inferred: command → stdio, url → http.
	Transport   string `json:"transport,omitempty"`
	Description string `json:"description,omitempty"`

	// stdio
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`

	// http (Streamable HTTP)
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// Policy. Nothing is allowed unless it matches Allow (glob, e.g. "list_*").
	// Deny wins over Allow. AllowReadOnly additionally allows tools whose
	// server-declared annotations say readOnlyHint=true.
	Allow         []string `json:"allow"`
	Deny          []string `json:"deny,omitempty"`
	AllowReadOnly bool     `json:"allow_readonly,omitempty"`

	// Timeout such as "60s" or "2m". Empty means DefaultTimeout.
	Timeout string `json:"timeout,omitempty"`

	// Auth configures OAuth for http servers that need it (Vercel, Linear, Notion, ...).
	Auth *Auth `json:"auth,omitempty"`

	// Preset records which built-in preset created this server (informational).
	Preset string `json:"preset,omitempty"`

	// Source is the file this server was loaded from (not part of the file).
	Source string `json:"-"`
}

// Auth is per-server OAuth configuration. With no client_id, sluice uses
// OAuth dynamic client registration, which most hosted MCP servers support.
type Auth struct {
	Type         string   `json:"type"` // "oauth"
	ClientID     string   `json:"client_id,omitempty"`
	ClientSecret string   `json:"client_secret,omitempty"` // use ${env:VAR}
	Scopes       []string `json:"scopes,omitempty"`
	// RedirectPort fixes the loopback callback port (needed for pre-registered
	// clients whose redirect URI must match exactly). 0 = pick a free port.
	RedirectPort int `json:"redirect_port,omitempty"`
}

// IsOAuth reports whether the server uses OAuth.
func (s *Server) IsOAuth() bool { return s.Auth != nil && s.Auth.Type == "oauth" }

// Audit configures the call log.
type Audit struct {
	// Enabled defaults to true.
	Enabled *bool `json:"enabled,omitempty"`
	// Path defaults to <config dir>/audit.jsonl.
	Path string `json:"path,omitempty"`
	// LogArgs records argument values. Default false: only argument names and a hash.
	LogArgs bool `json:"log_args,omitempty"`
}

// DefaultMaxOutput is the default cap on bytes printed by `call` (about 25k tokens).
const DefaultMaxOutput = 100_000

// Config is the whole configuration.
type Config struct {
	Servers map[string]*Server `json:"servers"`
	Audit   *Audit             `json:"audit,omitempty"`
	// MaxOutput caps bytes printed by `call`. Omitted = DefaultMaxOutput, 0 = unlimited.
	MaxOutput *int `json:"max_output,omitempty"`

	// Path is where the config was loaded from (not part of the file).
	Path string `json:"-"`
}

// Dir is the directory holding the config, .env, servers.d, tokens and the audit log.
func (c *Config) Dir() string { return filepath.Dir(c.Path) }

// ServersDir is where `sluice add` writes one file per server.
func (c *Config) ServersDir() string { return filepath.Join(c.Dir(), "servers.d") }

// AuditEnabled reports whether calls are logged.
func (c *Config) AuditEnabled() bool {
	return c.Audit == nil || c.Audit.Enabled == nil || *c.Audit.Enabled
}

// AuditPath is the audit log file.
func (c *Config) AuditPath() string {
	if c.Audit != nil && c.Audit.Path != "" {
		return c.Audit.Path
	}
	return filepath.Join(c.Dir(), "audit.jsonl")
}

// MaxOutputBytes returns the output cap (0 = unlimited).
func (c *Config) MaxOutputBytes() int {
	if c.MaxOutput == nil {
		return DefaultMaxOutput
	}
	return *c.MaxOutput
}

var serverNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// DefaultPath returns the config path: $SLUICE_CONFIG, else <UserConfigDir>/sluice/config.json
// (%AppData%\sluice\config.json on Windows, ~/.config/sluice/config.json on Linux).
func DefaultPath() (string, error) {
	if p := os.Getenv("SLUICE_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine user config dir: %w", err)
	}
	return filepath.Join(dir, "sluice", "config.json"), nil
}

// Load reads and validates the config at p. It also loads <dir of p>/.env
// into the process environment (existing variables are never overridden).
func Load(p string) (*Config, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config not found at %s (run `sluice init` to create one)", p)
		}
		return nil, err
	}
	if err := LoadDotEnv(filepath.Join(filepath.Dir(p), ".env")); err != nil {
		return nil, err
	}
	var c Config
	if err := decodeStrict(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	c.Path = p
	if c.Servers == nil {
		c.Servers = map[string]*Server{}
	}
	for _, s := range c.Servers {
		if s != nil {
			s.Source = p
		}
	}
	if err := c.loadServersDir(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return &c, nil
}

// loadServersDir adds servers from servers.d/<name>.json(c). A name defined
// in two places is an error rather than a silent override.
func (c *Config) loadServersDir() error {
	entries, err := os.ReadDir(c.ServersDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if e.IsDir() || (ext != ".json" && ext != ".jsonc") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ext)
		fp := filepath.Join(c.ServersDir(), e.Name())
		data, err := os.ReadFile(fp)
		if err != nil {
			return err
		}
		var s Server
		if err := decodeStrict(data, &s); err != nil {
			return fmt.Errorf("parse %s: %w", fp, err)
		}
		if prev, dup := c.Servers[name]; dup {
			return fmt.Errorf("server %q is defined twice: %s and %s", name, prev.Source, fp)
		}
		s.Source = fp
		c.Servers[name] = &s
	}
	return nil
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(StripJSONComments(string(data))))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// Validate checks structure and patterns and fills in the inferred transport.
// A config with no servers is valid (commands that need one say so).
func (c *Config) Validate() error {
	if c.MaxOutput != nil && *c.MaxOutput < 0 {
		return errors.New("max_output must be >= 0")
	}
	for name, s := range c.Servers {
		if !serverNameRe.MatchString(name) {
			return fmt.Errorf("server name %q: use letters, digits, '-' or '_'", name)
		}
		if s == nil {
			return fmt.Errorf("server %q: empty definition", name)
		}
		if s.Transport == "" {
			switch {
			case s.Command != "":
				s.Transport = "stdio"
			case s.URL != "":
				s.Transport = "http"
			}
		}
		switch s.Transport {
		case "stdio":
			if s.Command == "" {
				return fmt.Errorf("server %q: stdio transport needs \"command\"", name)
			}
		case "http":
			if s.URL == "" {
				return fmt.Errorf("server %q: http transport needs \"url\"", name)
			}
		default:
			return fmt.Errorf("server %q: transport must be \"stdio\" or \"http\" (got %q)", name, s.Transport)
		}
		for _, pat := range append(append([]string{}, s.Allow...), s.Deny...) {
			if _, err := path.Match(pat, ""); err != nil {
				return fmt.Errorf("server %q: bad pattern %q: %w", name, pat, err)
			}
		}
		if s.Timeout != "" {
			if _, err := time.ParseDuration(s.Timeout); err != nil {
				return fmt.Errorf("server %q: bad timeout %q: %w", name, s.Timeout, err)
			}
		}
		if s.Auth != nil {
			if s.Auth.Type != "oauth" {
				return fmt.Errorf("server %q: auth.type must be \"oauth\" (got %q)", name, s.Auth.Type)
			}
			if s.Transport != "http" {
				return fmt.Errorf("server %q: oauth needs the http transport", name)
			}
			if s.Auth.RedirectPort < 0 || s.Auth.RedirectPort > 65535 {
				return fmt.Errorf("server %q: bad auth.redirect_port", name)
			}
		}
	}
	return nil
}

// ValidName reports whether name can be used as a server name.
func ValidName(name string) bool { return serverNameRe.MatchString(name) }

// Names returns server names sorted.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Servers))
	for n := range c.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// TimeoutOrDefault returns the server timeout.
func (s *Server) TimeoutOrDefault() time.Duration {
	if s.Timeout == "" {
		return DefaultTimeout
	}
	d, _ := time.ParseDuration(s.Timeout) // validated in Load
	return d
}

var envRefRe = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)

// Expand replaces ${env:VAR} with the value of VAR. Missing variables are an
// error naming the variable (never its value).
func Expand(s string) (string, error) {
	var missing []string
	out := envRefRe.ReplaceAllStringFunc(s, func(m string) string {
		name := envRefRe.FindStringSubmatch(m)[1]
		v, ok := os.LookupEnv(name)
		if !ok || v == "" {
			missing = append(missing, name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("environment variable %s is not set", strings.Join(missing, ", "))
	}
	return out, nil
}

// EnvRefs lists the ${env:VAR} names a server needs.
func (s *Server) EnvRefs() []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		for _, m := range envRefRe.FindAllStringSubmatch(v, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	add(s.Command)
	add(s.URL)
	add(s.Cwd)
	for _, a := range s.Args {
		add(a)
	}
	for _, v := range s.Env {
		add(v)
	}
	for _, v := range s.Headers {
		add(v)
	}
	if s.Auth != nil {
		add(s.Auth.ClientID)
		add(s.Auth.ClientSecret)
	}
	sort.Strings(out)
	return out
}

// Resolved returns a copy of s with every ${env:VAR} expanded.
func (s *Server) Resolved() (*Server, error) {
	r := *s
	var err error
	ex := func(v string) string {
		if err != nil {
			return v
		}
		var e string
		e, err = Expand(v)
		return e
	}
	r.Command = ex(s.Command)
	r.URL = ex(s.URL)
	r.Cwd = ex(s.Cwd)
	r.Args = make([]string, len(s.Args))
	for i, a := range s.Args {
		r.Args[i] = ex(a)
	}
	r.Env = make(map[string]string, len(s.Env))
	for k, v := range s.Env {
		r.Env[k] = ex(v)
	}
	r.Headers = make(map[string]string, len(s.Headers))
	for k, v := range s.Headers {
		r.Headers[k] = ex(v)
	}
	if s.Auth != nil {
		a := *s.Auth
		a.ClientID = ex(a.ClientID)
		a.ClientSecret = ex(a.ClientSecret)
		r.Auth = &a
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// LoadDotEnv loads KEY=VALUE lines from p into the environment without
// overriding variables that are already set. A missing file is not an error.
func LoadDotEnv(p string) error {
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", p, n)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if v == "" {
			continue // placeholder written by `sluice add`, not a value
		}
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
	return sc.Err()
}

// StripJSONComments removes // and /* */ comments outside of strings, so the
// config file may be written as JSONC.
func StripJSONComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			b.WriteByte(c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(s) {
			switch s[i+1] {
			case '/':
				for i < len(s) && s[i] != '\n' {
					i++
				}
				if i < len(s) {
					b.WriteByte('\n')
				}
				continue
			case '*':
				i += 2
				for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
					if s[i] == '\n' {
						b.WriteByte('\n')
					}
					i++
				}
				i++ // skip '/'
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}
