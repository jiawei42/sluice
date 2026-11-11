package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/jiawei42/sluice/internal/audit"
	"github.com/jiawei42/sluice/internal/client"
	"github.com/jiawei42/sluice/internal/config"
	"github.com/jiawei42/sluice/internal/oauth"
	"github.com/jiawei42/sluice/internal/presets"
	"github.com/jiawei42/sluice/internal/toolcache"
)

// ---- presets / add / remove ------------------------------------------------

func cmdPresets(o *opts, rest []string, stdio IO) error {
	if len(rest) != 0 {
		return fail(ExitUsage, "usage: sluice presets [--json]")
	}
	all := presets.All()
	if o.json {
		type row struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Auth        string   `json:"auth"`
			Env         []string `json:"env,omitempty"`
			Docs        string   `json:"docs"`
		}
		var rows []row
		for _, p := range all {
			r := row{Name: p.Name, Description: p.Description, Auth: authKind(p), Docs: p.Docs}
			for _, e := range p.Env {
				r.Env = append(r.Env, e.Name)
			}
			rows = append(rows, r)
		}
		return writeJSON(stdio.Out, rows, true)
	}
	tw := tabwriter.NewWriter(stdio.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PRESET\tAUTH\tDESCRIPTION")
	for _, p := range all {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, authKind(p), p.Description)
	}
	tw.Flush()
	fmt.Fprintln(stdio.Out, "\nAdd one with: sluice add <preset> [--name NAME]")
	return nil
}

func authKind(p presets.Preset) string {
	switch {
	case p.Server.IsOAuth():
		return "oauth"
	case len(p.Env) > 0:
		return "token"
	default:
		return "none"
	}
}

func cmdAdd(o *opts, rest []string, stdio IO) error {
	var (
		name   string
		origin string // what the file header says created it
		srv    config.Server
		about  []string // extra header comment lines
		envs   []presets.EnvVar
		notes  []string
	)
	if o.dash || o.url != "" {
		var err error
		if name, srv, envs, err = customServer(o, rest); err != nil {
			return err
		}
		origin = "custom server"
		about = []string{`Your own MCP server. Nothing is allowed until tool names or globs are in "allow".`}
		if len(srv.Allow) == 0 {
			notes = append(notes, "nothing is allowed yet: run `sluice policy "+name+"` to see its tools, then add the ones agents may use to \"allow\"")
		}
	} else {
		if len(rest) != 1 {
			return fail(ExitUsage, "usage: sluice add <preset> [--name NAME] [--force]  (see `sluice presets`)\n"+
				"   or: sluice add <name> -- <command> [args...]   (your own stdio server)\n"+
				"   or: sluice add <name> --url URL [--oauth]     (your own HTTP server)")
		}
		if o.oauth || o.bearer != "" || len(o.envs) > 0 || len(o.headerEnvs) > 0 || len(o.allow) > 0 || len(o.deny) > 0 {
			return fail(ExitUsage, "--env, --bearer, --header-env, --oauth, --allow and --deny are for custom servers (with -- <command> or --url); edit a preset's file after adding it")
		}
		p, ok := presets.Get(rest[0])
		if !ok {
			return fail(ExitUsage, "unknown preset %q (see `sluice presets`; for your own server use `sluice add <name> -- <command>` or `--url URL`)", rest[0])
		}
		name = o.name
		if name == "" {
			name = p.Name
		}
		origin = "preset " + p.Name
		srv = p.Server
		srv.Preset = p.Name
		about = append(about, p.Description, "Docs: "+p.Docs)
		for _, e := range p.Env {
			about = append(about, fmt.Sprintf("Needs %s: %s", e.Name, e.Help))
		}
		for _, n := range p.Notes {
			about = append(about, "Note: "+n)
		}
		envs, notes = p.Env, p.Notes
	}
	if !config.ValidName(name) {
		return fail(ExitUsage, "bad server name %q: use letters, digits, '-' or '_'", name)
	}
	c, err := loadConfig(o)
	if err != nil {
		return err
	}
	if prev, exists := c.Servers[name]; exists && !(o.force && isInServersDir(c, prev)) {
		if o.force {
			return fail(ExitUsage, "server %q is defined in %s; edit or remove it there", name, prev.Source)
		}
		return fail(ExitUsage, "server %q already exists (%s); use --name to add another, or --force to replace it", name, prev.Source)
	}

	body, err := json.MarshalIndent(&srv, "", "  ")
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// %s — added by `sluice add` (%s)\n", name, origin)
	for _, line := range about {
		fmt.Fprintf(&b, "// %s\n", line)
	}
	b.WriteString("//\n// Whitelist: \"allow\" globs may be called, \"deny\" always wins, \"allow_readonly\" also\n")
	fmt.Fprintf(&b, "// admits tools the server marks read-only. Review with: sluice policy %s\n", name)
	b.WriteString("// Secrets: never write them here. Use ${env:VAR} and put the value in .env next to config.json.\n")
	b.Write(body)
	b.WriteByte('\n')

	if err := os.MkdirAll(c.ServersDir(), 0o700); err != nil {
		return err
	}
	target := filepath.Join(c.ServersDir(), name+".jsonc")
	if err := os.WriteFile(target, []byte(b.String()), 0o600); err != nil {
		return err
	}
	// --force replacing servers.d/<name>.json: drop the old file, or the
	// server would be defined twice.
	if prev, exists := c.Servers[name]; exists && filepath.Clean(prev.Source) != filepath.Clean(target) {
		if err := os.Remove(prev.Source); err != nil {
			return fmt.Errorf("replace %s: %w", prev.Source, err)
		}
	}
	if srv.Preset != "" {
		fmt.Fprintf(stdio.Out, "added %q from preset %q → %s\n", name, srv.Preset, target)
	} else {
		fmt.Fprintf(stdio.Out, "added %q (%s) → %s\n", name, origin, target)
	}

	envPath := filepath.Join(c.Dir(), ".env")
	for _, e := range envs {
		if os.Getenv(e.Name) != "" {
			continue
		}
		if added, err := ensureEnvKey(envPath, e); err != nil {
			return err
		} else if added {
			fmt.Fprintf(stdio.Out, "  set %s in %s\n", e.Name, envPath)
		} else {
			fmt.Fprintf(stdio.Out, "  %s is still empty in %s\n", e.Name, envPath)
		}
	}
	for _, n := range notes {
		fmt.Fprintf(stdio.Out, "  note: %s\n", n)
	}
	for _, loc := range config.LiteralSecrets(&srv) {
		fmt.Fprintf(stdio.Out, "  warning: %s looks like a secret in plain text; move the value to %s and use ${env:VAR}\n", loc, envPath)
	}
	switch {
	case srv.IsOAuth() && !oauth.NewStore(c).Status(name, mustResolvedURL(&srv)).Usable():
		fmt.Fprintf(stdio.Out, "next: sluice login %s   (opens your browser)\n", name)
	case len(srv.Allow) == 0 && !srv.AllowReadOnly:
		fmt.Fprintf(stdio.Out, "next: sluice policy %s, then edit \"allow\" in %s\n", name, target)
	default:
		fmt.Fprintf(stdio.Out, "next: sluice doctor %s, then review the whitelist with sluice policy %s\n", name, name)
	}
	return nil
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// customServer builds a server from `sluice add <name> -- <command> [args]`
// (stdio) or `sluice add <name> --url URL` (http). Credentials can only be
// given as environment variable names, so no secret value is written.
func customServer(o *opts, rest []string) (string, config.Server, []presets.EnvVar, error) {
	srv := config.Server{Description: "Custom server added with `sluice add`"}
	bad := func(format string, a ...any) (string, config.Server, []presets.EnvVar, error) {
		return "", srv, nil, fail(ExitUsage, format, a...)
	}
	if o.name != "" {
		return bad("for a custom server the name is the first argument: sluice add <name> -- <command>")
	}
	if o.dash && o.url != "" {
		return bad("use either -- <command> (stdio) or --url URL (http), not both")
	}
	if len(rest) == 0 || len(rest) != 1+len(o.dashArgs) {
		return bad("usage: sluice add <name> -- <command> [args...]   or   sluice add <name> --url URL [--oauth]")
	}
	name := rest[0]

	var envs []presets.EnvVar
	needEnv := func(v string) error {
		if !envNameRe.MatchString(v) {
			return fail(ExitUsage, "bad environment variable name %q", v)
		}
		for _, e := range envs {
			if e.Name == v {
				return nil
			}
		}
		envs = append(envs, presets.EnvVar{Name: v, Help: "used by the " + name + " server; put the value here, never in servers.d"})
		return nil
	}
	ref := func(v string) string { return "${env:" + v + "}" }

	if o.dash {
		if len(o.dashArgs) == 0 {
			return bad("missing the command after --")
		}
		if o.oauth || o.bearer != "" || len(o.headerEnvs) > 0 {
			return bad("--oauth, --bearer and --header-env are for --url servers; use --env for a stdio server")
		}
		srv.Transport = "stdio"
		srv.Command = o.dashArgs[0]
		srv.Args = o.dashArgs[1:]
		for _, e := range o.envs {
			child, src, ok := strings.Cut(e, "=")
			if !ok {
				src = child
			}
			if !envNameRe.MatchString(child) {
				return bad("--env expects VAR or CHILD=VAR, got %q", e)
			}
			if err := needEnv(src); err != nil {
				return "", srv, nil, err
			}
			if srv.Env == nil {
				srv.Env = map[string]string{}
			}
			srv.Env[child] = ref(src)
		}
	} else {
		if len(o.envs) > 0 {
			return bad("--env is for stdio servers; for --url servers use --bearer VAR or --header-env Name=VAR")
		}
		u, err := url.Parse(o.url)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return bad("--url must be an http(s) URL")
		}
		if u.User != nil || u.RawQuery != "" {
			return bad("--url must not contain credentials or a query string; pass secrets with --bearer or --header-env")
		}
		if o.oauth && o.bearer != "" {
			return bad("use either --oauth or --bearer, not both")
		}
		srv.Transport = "http"
		srv.URL = o.url
		if o.oauth {
			srv.Auth = &config.Auth{Type: "oauth"}
		}
		if o.bearer != "" {
			if err := needEnv(o.bearer); err != nil {
				return "", srv, nil, err
			}
			srv.Headers = map[string]string{"Authorization": "Bearer " + ref(o.bearer)}
		}
		for _, h := range o.headerEnvs {
			k, v, ok := strings.Cut(h, "=")
			k = strings.TrimSpace(k)
			if !ok || k == "" {
				return bad("--header-env expects Name=VAR, got %q", h)
			}
			if err := needEnv(v); err != nil {
				return "", srv, nil, err
			}
			if srv.Headers == nil {
				srv.Headers = map[string]string{}
			}
			srv.Headers[k] = ref(v)
		}
	}
	srv.Allow = append([]string{}, o.allow...)
	srv.Deny = o.deny
	return name, srv, envs, nil
}

func isInServersDir(c *config.Config, s *config.Server) bool {
	rel, err := filepath.Rel(c.ServersDir(), s.Source)
	return err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// ensureEnvKey appends "NAME=" (with a help comment) to the .env file unless
// the key is already present. It never writes a value.
func ensureEnvKey(envPath string, e presets.EnvVar) (bool, error) {
	data, err := os.ReadFile(envPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "export "), "=")
		if ok && strings.TrimSpace(k) == e.Name {
			return false, nil
		}
	}
	f, err := os.OpenFile(envPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n# %s\n%s=\n", e.Help, e.Name)
	return err == nil, err
}

func cmdRemove(o *opts, rest []string, stdio IO) error {
	if len(rest) != 1 {
		return fail(ExitUsage, "usage: sluice remove <server>")
	}
	c, err := loadConfig(o)
	if err != nil {
		return err
	}
	s, err := serverFor(c, rest[0])
	if err != nil {
		return err
	}
	if !isInServersDir(c, s) {
		return fail(ExitUsage, "server %q is defined in %s; remove it there by hand", rest[0], s.Source)
	}
	if err := os.Remove(s.Source); err != nil {
		return err
	}
	if err := oauth.NewStore(c).Delete(rest[0]); err != nil {
		return err
	}
	fmt.Fprintf(stdio.Out, "removed %q (%s)\n", rest[0], s.Source)
	return nil
}

// ---- login / logout --------------------------------------------------------

func cmdLogin(o *opts, rest []string, stdio IO) error {
	if len(rest) != 1 {
		return fail(ExitUsage, "usage: sluice login <server> [--no-browser]")
	}
	c, err := loadConfig(o)
	if err != nil {
		return err
	}
	name := rest[0]
	s, err := serverFor(c, name)
	if err != nil {
		return err
	}
	if !s.IsOAuth() {
		return fail(ExitUsage, "server %q does not use OAuth (it uses tokens from %s)", name, filepath.Join(c.Dir(), ".env"))
	}
	resolved, err := s.Resolved()
	if err != nil {
		return &ExitError{ExitUsage, err}
	}
	store := oauth.NewStore(c)
	login, err := oauth.NewLogin(store, name, resolved, oauth.LoginOptions{
		Out: stdio.Err, In: stdio.In, OpenBrowser: !o.noBrowser, Version: client.Version,
	})
	if err != nil {
		return &ExitError{ExitConnect, err}
	}
	defer login.Close()

	timeout := 5 * time.Minute
	if o.timeout > 0 {
		timeout = o.timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	sess, err := client.Connect(ctx, name, s, client.Options{
		Verbose: o.verbose,
		OAuth:   func(*config.Server) (auth.OAuthHandler, error) { return login.Handler, nil },
	})
	if err != nil {
		if ctx.Err() != nil {
			return fail(ExitConnect, "login to %q timed out after %s: run it again and finish signing in in the browser", name, timeout)
		}
		return &ExitError{ExitConnect, fmt.Errorf("login to %q failed: %w", name, err)}
	}
	defer sess.Close()
	tools, err := sess.ListTools(ctx)
	if err != nil {
		return asConnectErr(err)
	}
	toolcache.Save(c, name, s, tools)
	fmt.Fprintf(stdio.Out, "logged in to %q: %d tools, %d allowed by the whitelist. Token stored in %s\n",
		name, len(tools), countAllowed(policyOf(s), tools), store.Dir)
	return nil
}

func cmdLogout(o *opts, rest []string, stdio IO) error {
	if len(rest) != 1 {
		return fail(ExitUsage, "usage: sluice logout <server>")
	}
	c, err := loadConfig(o)
	if err != nil {
		return err
	}
	if _, err := serverFor(c, rest[0]); err != nil {
		return err
	}
	if err := oauth.NewStore(c).Delete(rest[0]); err != nil {
		return err
	}
	fmt.Fprintf(stdio.Out, "logged out of %q (local token deleted; revoke access in the provider's settings if needed)\n", rest[0])
	return nil
}

// ---- audit -----------------------------------------------------------------

func cmdAudit(o *opts, rest []string, stdio IO) error {
	if len(rest) != 0 {
		return fail(ExitUsage, "usage: sluice audit [--since 24h] [--server NAME] [--denied] [--limit N] [--json]")
	}
	c, err := loadConfig(o)
	if err != nil {
		return err
	}
	entries, err := audit.Read(c.AuditPath())
	if err != nil {
		return err
	}
	var cutoff time.Time
	if o.since > 0 {
		cutoff = time.Now().Add(-o.since)
	}
	var out []audit.Entry
	for _, e := range entries {
		if !cutoff.IsZero() && e.Time.Before(cutoff) {
			continue
		}
		if o.serverFilter != "" && e.Server != o.serverFilter {
			continue
		}
		if o.deniedOnly && e.Decision != "denied" {
			continue
		}
		out = append(out, e)
	}
	limit := o.limit
	if limit == 0 {
		limit = 50
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	if o.json {
		if out == nil {
			out = []audit.Entry{}
		}
		return writeJSON(stdio.Out, out, true)
	}
	if len(out) == 0 {
		fmt.Fprintf(stdio.Out, "no matching calls in %s\n", c.AuditPath())
		return nil
	}
	tw := tabwriter.NewWriter(stdio.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tAGENT\tDECISION\tTOOL\tEXIT\tMS\tNOTE")
	for _, e := range out {
		note := e.Error
		if e.Decision == "denied" {
			note = e.Reason
		} else if e.Truncated {
			note = "output truncated"
		}
		agent := e.Agent
		if agent == "" {
			agent = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s/%s\t%d\t%d\t%s\n", e.Time.Local().Format("01-02 15:04:05"),
			agent, e.Decision, e.Server, e.Tool, e.Exit, e.DurationMs, oneLine(note, 70))
	}
	return tw.Flush()
}
