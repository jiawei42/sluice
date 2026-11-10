package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/jiawei42/sluice/internal/audit"
	"github.com/jiawei42/sluice/internal/client"
	"github.com/jiawei42/sluice/internal/config"
	"github.com/jiawei42/sluice/internal/oauth"
	"github.com/jiawei42/sluice/internal/presets"
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
	if len(rest) != 1 {
		return fail(ExitUsage, "usage: sluice add <preset> [--name NAME] [--force]  (see `sluice presets`)")
	}
	p, ok := presets.Get(rest[0])
	if !ok {
		return fail(ExitUsage, "unknown preset %q (see `sluice presets`)", rest[0])
	}
	name := o.name
	if name == "" {
		name = p.Name
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

	srv := p.Server
	srv.Preset = p.Name
	body, err := json.MarshalIndent(&srv, "", "  ")
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// %s — added by `sluice add %s`\n", name, p.Name)
	fmt.Fprintf(&b, "// %s\n// Docs: %s\n", p.Description, p.Docs)
	for _, e := range p.Env {
		fmt.Fprintf(&b, "// Needs %s: %s\n", e.Name, e.Help)
	}
	for _, n := range p.Notes {
		fmt.Fprintf(&b, "// Note: %s\n", n)
	}
	b.WriteString("//\n// Whitelist: \"allow\" globs may be called, \"deny\" always wins, \"allow_readonly\" also\n")
	fmt.Fprintf(&b, "// admits tools the server marks read-only. Review with: sluice policy %s\n", name)
	b.Write(body)
	b.WriteByte('\n')

	if err := os.MkdirAll(c.ServersDir(), 0o700); err != nil {
		return err
	}
	target := filepath.Join(c.ServersDir(), name+".jsonc")
	if err := os.WriteFile(target, []byte(b.String()), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stdio.Out, "added %q from preset %q → %s\n", name, p.Name, target)

	envPath := filepath.Join(c.Dir(), ".env")
	for _, e := range p.Env {
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
	for _, n := range p.Notes {
		fmt.Fprintf(stdio.Out, "  note: %s\n", n)
	}
	if srv.IsOAuth() {
		fmt.Fprintf(stdio.Out, "next: sluice login %s   (opens your browser)\n", name)
	} else {
		fmt.Fprintf(stdio.Out, "next: sluice doctor %s, then review the whitelist with sluice policy %s\n", name, name)
	}
	return nil
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
		Out: stdio.Err, OpenBrowser: !o.noBrowser, Version: client.Version,
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
	p := policyOf(s)
	allowed := 0
	for _, t := range tools {
		if p.Check(t.Name, readOnlyOf(t)).Allowed {
			allowed++
		}
	}
	fmt.Fprintf(stdio.Out, "logged in to %q: %d tools, %d allowed by the whitelist. Token stored in %s\n",
		name, len(tools), allowed, store.Dir)
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
