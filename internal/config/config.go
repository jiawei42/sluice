// Package config loads the mcpx configuration file, expands ${env:VAR}
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
}

// Config is the whole configuration file.
type Config struct {
	Servers map[string]*Server `json:"servers"`

	// Path is where the config was loaded from (not part of the file).
	Path string `json:"-"`
}

var serverNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// DefaultPath returns the config path: $MCPX_CONFIG, else <UserConfigDir>/mcpx/config.json
// (%AppData%\mcpx\config.json on Windows, ~/.config/mcpx/config.json on Linux).
func DefaultPath() (string, error) {
	if p := os.Getenv("MCPX_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine user config dir: %w", err)
	}
	return filepath.Join(dir, "mcpx", "config.json"), nil
}

// Load reads and validates the config at p. It also loads <dir of p>/.env
// into the process environment (existing variables are never overridden).
func Load(p string) (*Config, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config not found at %s (run `mcpx init` to create one)", p)
		}
		return nil, err
	}
	if err := LoadDotEnv(filepath.Join(filepath.Dir(p), ".env")); err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(StripJSONComments(string(data))))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	c.Path = p
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return &c, nil
}

// Validate checks structure and patterns and fills in the inferred transport.
func (c *Config) Validate() error {
	if len(c.Servers) == 0 {
		return errors.New(`no servers defined under "servers"`)
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
	}
	return nil
}

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
