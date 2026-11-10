package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	agentdocs "github.com/jiawei42/sluice/docs/agents"
	"github.com/jiawei42/sluice/internal/audit"
	"github.com/jiawei42/sluice/internal/client"
	"github.com/jiawei42/sluice/internal/config"
	"github.com/jiawei42/sluice/internal/oauth"
	"github.com/jiawei42/sluice/internal/policy"
)

func loadConfig(o *opts) (*config.Config, error) {
	p := o.configPath
	if p == "" {
		var err error
		if p, err = config.DefaultPath(); err != nil {
			return nil, &ExitError{ExitUsage, err}
		}
	}
	c, err := config.Load(p)
	if err != nil {
		return nil, &ExitError{ExitUsage, err}
	}
	return c, nil
}

func serverFor(c *config.Config, name string) (*config.Server, error) {
	s, ok := c.Servers[name]
	if !ok {
		return nil, fail(ExitUsage, "unknown server %q (configured: %s)", name, strings.Join(c.Names(), ", "))
	}
	return s, nil
}

func policyOf(s *config.Server) policy.Policy {
	return policy.Policy{Allow: s.Allow, Deny: s.Deny, AllowReadOnly: s.AllowReadOnly}
}

func readOnlyOf(t *mcp.Tool) *bool {
	ro := t.Annotations != nil && t.Annotations.ReadOnlyHint
	return &ro
}

func timeoutFor(o *opts, s *config.Server) time.Duration {
	if o.timeout > 0 {
		return o.timeout
	}
	return s.TimeoutOrDefault()
}

// clientOptions wires the non-interactive OAuth handler: stored token only,
// never a browser login.
func clientOptions(o *opts, c *config.Config, name string) client.Options {
	store := oauth.NewStore(c)
	return client.Options{
		Verbose: o.verbose,
		OAuth: func(resolved *config.Server) (auth.OAuthHandler, error) {
			return oauth.AgentHandler(store, name, resolved)
		},
	}
}

func connect(ctx context.Context, o *opts, c *config.Config, name string, s *config.Server) (*client.Session, error) {
	sess, err := client.Connect(ctx, name, s, clientOptions(o, c, name))
	if err != nil {
		return nil, &ExitError{ExitConnect, loginHint(err, name, s)}
	}
	return sess, nil
}

// loginHint replaces a noisy transport error with the plain instruction when
// the real cause is a missing or expired OAuth login.
func loginHint(err error, name string, s *config.Server) error {
	if err == nil {
		return nil
	}
	var lr *oauth.LoginRequiredError
	if errors.As(err, &lr) {
		return lr
	}
	if s.IsOAuth() && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Unauthorized")) {
		return &oauth.LoginRequiredError{Server: name}
	}
	return err
}

func asConnectErr(err error) error {
	var ee *ExitError
	if errors.As(err, &ee) {
		return err
	}
	return &ExitError{ExitConnect, err}
}

func deniedErr(s *config.Server, server, tool, reason string) error {
	return fail(ExitDenied,
		"tool %s/%s is not allowed by the whitelist (%s). Do not try to work around this; "+
			"ask the user to add it to \"allow\" for server %q in %s if it is really needed.",
		server, tool, reason, server, s.Source)
}

// ---- servers ---------------------------------------------------------------

func cmdServers(o *opts, rest []string, stdio IO) error {
	if len(rest) != 0 {
		return fail(ExitUsage, "usage: sluice servers [--json]")
	}
	c, err := loadConfig(o)
	if err != nil {
		return err
	}
	if o.json {
		type row struct {
			Name          string   `json:"name"`
			Transport     string   `json:"transport"`
			Description   string   `json:"description,omitempty"`
			Allow         []string `json:"allow"`
			Deny          []string `json:"deny,omitempty"`
			AllowReadOnly bool     `json:"allow_readonly,omitempty"`
		}
		var rows []row
		for _, n := range c.Names() {
			s := c.Servers[n]
			rows = append(rows, row{n, s.Transport, s.Description, s.Allow, s.Deny, s.AllowReadOnly})
		}
		return writeJSON(stdio.Out, rows, true)
	}
	tw := tabwriter.NewWriter(stdio.Out, 0, 4, 2, ' ', 0)
	for _, n := range c.Names() {
		s := c.Servers[n]
		fmt.Fprintf(tw, "%s\t%s\t%s\n", n, s.Transport, s.Description)
	}
	return tw.Flush()
}

// ---- tools -----------------------------------------------------------------

func cmdTools(o *opts, rest []string, stdio IO) error {
	if len(rest) != 1 {
		return fail(ExitUsage, "usage: sluice tools <server> [--json]")
	}
	c, err := loadConfig(o)
	if err != nil {
		return err
	}
	s, err := serverFor(c, rest[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutFor(o, s))
	defer cancel()
	sess, err := connect(ctx, o, c, rest[0], s)
	if err != nil {
		return err
	}
	defer sess.Close()
	all, err := sess.ListTools(ctx)
	if err != nil {
		return asConnectErr(err)
	}
	p := policyOf(s)
	var allowed []*mcp.Tool
	for _, t := range all {
		if p.Check(t.Name, readOnlyOf(t)).Allowed {
			allowed = append(allowed, t)
		}
	}
	sort.Slice(allowed, func(i, j int) bool { return allowed[i].Name < allowed[j].Name })
	if len(allowed) == 0 {
		fmt.Fprintf(stdio.Err, "sluice: no tools of %q are allowed by the whitelist (%d exist)\n", rest[0], len(all))
	}
	if o.json {
		type row struct {
			Name        string `json:"name"`
			Title       string `json:"title,omitempty"`
			Description string `json:"description,omitempty"`
			ReadOnly    bool   `json:"read_only"`
		}
		rows := []row{}
		for _, t := range allowed {
			rows = append(rows, row{t.Name, t.Title, t.Description, *readOnlyOf(t)})
		}
		return writeJSON(stdio.Out, rows, true)
	}
	tw := tabwriter.NewWriter(stdio.Out, 0, 4, 2, ' ', 0)
	for _, t := range allowed {
		fmt.Fprintf(tw, "%s\t%s\n", t.Name, oneLine(t.Description, 110))
	}
	return tw.Flush()
}

// ---- schema ----------------------------------------------------------------

func cmdSchema(o *opts, rest []string, stdio IO) error {
	if len(rest) != 2 {
		return fail(ExitUsage, "usage: sluice schema <server> <tool>")
	}
	c, err := loadConfig(o)
	if err != nil {
		return err
	}
	server, tool := rest[0], rest[1]
	s, err := serverFor(c, server)
	if err != nil {
		return err
	}
	p := policyOf(s)
	if d := p.Check(tool, nil); !d.Allowed && !p.NeedsMetadata(tool) {
		return deniedErr(s, server, tool, d.Reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutFor(o, s))
	defer cancel()
	sess, err := connect(ctx, o, c, server, s)
	if err != nil {
		return err
	}
	defer sess.Close()
	t, err := sess.FindTool(ctx, tool)
	if err != nil {
		return asConnectErr(err)
	}
	if t == nil {
		return fail(ExitUsage, "server %q has no tool %q (run `sluice tools %s`)", server, tool, server)
	}
	if d := p.Check(tool, readOnlyOf(t)); !d.Allowed {
		return deniedErr(s, server, tool, d.Reason)
	}
	out := map[string]any{
		"name":        t.Name,
		"description": t.Description,
		"inputSchema": t.InputSchema,
	}
	if t.Title != "" {
		out["title"] = t.Title
	}
	if t.OutputSchema != nil {
		out["outputSchema"] = t.OutputSchema
	}
	if t.Annotations != nil {
		out["annotations"] = t.Annotations
	}
	return writeJSON(stdio.Out, out, true)
}

// ---- call ------------------------------------------------------------------

func cmdCall(o *opts, rest []string, stdio IO) error {
	if len(rest) < 2 || len(rest) > 3 {
		return fail(ExitUsage, "usage: sluice call <server> <tool> [JSON | - | @file.json] [-a key=value ...]")
	}
	c, err := loadConfig(o)
	if err != nil {
		return err
	}
	server, tool := rest[0], rest[1]
	s, err := serverFor(c, server)
	if err != nil {
		return err
	}
	args, err := buildArgs(o, rest[2:], stdio.In)
	if err != nil {
		return &ExitError{ExitUsage, err}
	}
	entry := audit.Entry{Time: time.Now().UTC(), Server: server, Tool: tool, Agent: audit.DetectAgent()}
	if wd, err := os.Getwd(); err == nil {
		entry.Cwd = wd
	}
	entry.SetArgs(args, c.Audit != nil && c.Audit.LogArgs)
	err = doCall(o, c, s, server, tool, args, stdio, &entry)
	if c.AuditEnabled() {
		entry.DurationMs = time.Since(entry.Time).Milliseconds()
		entry.Exit = exitCodeOf(err)
		var ee *ExitError
		if errors.As(err, &ee) && ee.Code != ExitToolError && ee.Code != ExitDenied {
			entry.SetError(err)
		}
		if aerr := audit.Append(c.AuditPath(), entry); aerr != nil {
			fmt.Fprintln(stdio.Err, "sluice: warning: could not write audit log:", aerr)
		}
	}
	return err
}

func exitCodeOf(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return ExitUsage
}

func doCall(o *opts, c *config.Config, s *config.Server, server, tool string, args map[string]any, stdio IO, entry *audit.Entry) error {
	p := policyOf(s)
	d := p.Check(tool, nil)
	deny := func(reason string) error {
		entry.Decision, entry.Reason = "denied", reason
		return deniedErr(s, server, tool, reason)
	}
	if !d.Allowed && !p.NeedsMetadata(tool) {
		return deny(d.Reason)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutFor(o, s))
	defer cancel()
	sess, err := connect(ctx, o, c, server, s)
	if err != nil {
		return err
	}
	defer sess.Close()
	// Always load the tool list first, in this session: the SDK needs the
	// tool's schema to send SEP-2243 parameter headers (GitHub's hosted server
	// rejects calls without them), allow_readonly needs its annotations, and an
	// unknown tool gets a clear error instead of a server-specific one.
	t, err := sess.FindTool(ctx, tool)
	if err != nil {
		return asConnectErr(loginHint(err, server, s))
	}
	if t == nil {
		return fail(ExitUsage, "server %q has no tool %q (run `sluice tools %s`)", server, tool, server)
	}
	if d = p.Check(tool, readOnlyOf(t)); !d.Allowed {
		return deny(d.Reason)
	}
	entry.Decision, entry.Reason = "allowed", d.Reason
	res, err := sess.CallTool(ctx, tool, args)
	if err != nil {
		return asConnectErr(loginHint(err, server, s))
	}

	var buf bytes.Buffer
	if o.raw {
		err = writeJSON(&buf, res, true)
	} else {
		err = writeResult(&buf, res)
	}
	if err != nil {
		return err
	}
	entry.OutBytes = buf.Len()
	if res.IsError {
		stdio.Err.Write(buf.Bytes())
		return &ExitError{Code: ExitToolError, Err: errors.New("")}
	}
	if o.outFile != "" {
		if err := os.WriteFile(o.outFile, buf.Bytes(), 0o644); err != nil {
			return fail(ExitUsage, "write --out file: %v", err)
		}
		return writeJSON(stdio.Out, map[string]any{"saved_to": o.outFile, "bytes": buf.Len()}, false)
	}
	limit := c.MaxOutputBytes()
	if o.maxOutput != nil {
		limit = *o.maxOutput
	}
	if limit > 0 && buf.Len() > limit {
		entry.Truncated = true
		stdio.Out.Write(cutUTF8(buf.Bytes(), limit))
		fmt.Fprintf(stdio.Out, "\n")
		fmt.Fprintf(stdio.Err, "sluice: output truncated to %d of %d bytes. Narrow the query (filters, pagination), "+
			"or save the full result with --out FILE and process it with a script.\n", limit, buf.Len())
		return nil
	}
	_, err = stdio.Out.Write(buf.Bytes())
	return err
}

// cutUTF8 returns at most n bytes of b without splitting a UTF-8 sequence.
func cutUTF8(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	for n > 0 && !utf8.RuneStart(b[n]) {
		n--
	}
	return b[:n]
}

// buildArgs merges the positional JSON (or stdin / @file) with -a/-s flags.
func buildArgs(o *opts, pos []string, stdin io.Reader) (map[string]any, error) {
	args := map[string]any{}
	if len(pos) == 1 {
		var data []byte
		var err error
		switch src := pos[0]; {
		case src == "-":
			data, err = io.ReadAll(stdin)
		case strings.HasPrefix(src, "@"):
			data, err = os.ReadFile(src[1:])
		default:
			data = []byte(src)
		}
		if err != nil {
			return nil, err
		}
		data = bytes.TrimPrefix(bytes.TrimSpace(data), []byte("\xef\xbb\xbf")) // tolerate a UTF-8 BOM (PowerShell)
		if len(data) > 0 {
			if err := json.Unmarshal(data, &args); err != nil {
				return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
			}
		}
	}
	for _, a := range o.args {
		var v any
		if err := json.Unmarshal([]byte(a.val), &v); err != nil {
			v = a.val
		}
		args[a.key] = v
	}
	for _, a := range o.strs {
		args[a.key] = a.val
	}
	return args, nil
}

// writeResult prints tool output for humans and pipes: structured content as
// JSON when present, else text blocks verbatim; binary data is summarized.
func writeResult(w io.Writer, res *mcp.CallToolResult) error {
	if res.StructuredContent != nil && !res.IsError {
		return writeJSON(w, res.StructuredContent, false)
	}
	for _, ct := range res.Content {
		var line string
		switch v := ct.(type) {
		case *mcp.TextContent:
			line = v.Text
		case *mcp.ImageContent:
			line = fmt.Sprintf(`{"type":"image","mimeType":%q,"bytes":%d}`, v.MIMEType, len(v.Data))
		case *mcp.AudioContent:
			line = fmt.Sprintf(`{"type":"audio","mimeType":%q,"bytes":%d}`, v.MIMEType, len(v.Data))
		case *mcp.EmbeddedResource:
			if v.Resource != nil && v.Resource.Text != "" {
				line = v.Resource.Text
			} else if v.Resource != nil {
				line = fmt.Sprintf(`{"type":"resource","uri":%q,"mimeType":%q,"bytes":%d}`, v.Resource.URI, v.Resource.MIMEType, len(v.Resource.Blob))
			}
		default:
			b, err := json.Marshal(ct)
			if err != nil {
				return err
			}
			line = string(b)
		}
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
		if !strings.HasSuffix(line, "\n") {
			io.WriteString(w, "\n")
		}
	}
	return nil
}

// ---- policy (human) --------------------------------------------------------

func cmdPolicy(o *opts, rest []string, stdio IO) error {
	if len(rest) != 1 {
		return fail(ExitUsage, "usage: sluice policy <server>")
	}
	c, err := loadConfig(o)
	if err != nil {
		return err
	}
	s, err := serverFor(c, rest[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutFor(o, s))
	defer cancel()
	sess, err := connect(ctx, o, c, rest[0], s)
	if err != nil {
		return err
	}
	defer sess.Close()
	all, err := sess.ListTools(ctx)
	if err != nil {
		return asConnectErr(err)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	p := policyOf(s)
	tw := tabwriter.NewWriter(stdio.Out, 0, 4, 2, ' ', 0)
	n := 0
	for _, t := range all {
		d := p.Check(t.Name, readOnlyOf(t))
		mark := "deny "
		if d.Allowed {
			mark = "ALLOW"
			n++
		}
		ro := ""
		if *readOnlyOf(t) {
			ro = "read-only"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, t.Name, ro, d.Reason)
	}
	tw.Flush()
	fmt.Fprintf(stdio.Out, "\n%d of %d tools allowed. Edit %s to change.\n", n, len(all), s.Source)
	return nil
}

// ---- doctor ----------------------------------------------------------------

func cmdDoctor(o *opts, rest []string, stdio IO) error {
	if len(rest) > 1 {
		return fail(ExitUsage, "usage: sluice doctor [server]")
	}
	c, err := loadConfig(o)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdio.Out, "config: %s\n", c.Path)
	if c.AuditEnabled() {
		fmt.Fprintf(stdio.Out, "audit:  %s\n", c.AuditPath())
	} else {
		fmt.Fprintln(stdio.Out, "audit:  disabled")
	}
	names := c.Names()
	if len(names) == 0 {
		fmt.Fprintln(stdio.Out, "\nno servers configured: run `sluice presets`, then `sluice add <preset>`")
		return &ExitError{Code: ExitUsage, Err: errors.New("no servers configured")}
	}
	if len(rest) == 1 {
		if _, err := serverFor(c, rest[0]); err != nil {
			return err
		}
		names = rest
	}
	bad := 0
	for _, n := range names {
		s := c.Servers[n]
		fmt.Fprintf(stdio.Out, "\n[%s] %s\n", n, s.Transport)
		missing := false
		for _, v := range s.EnvRefs() {
			state := "set"
			if os.Getenv(v) == "" {
				state, missing = "MISSING", true
			}
			fmt.Fprintf(stdio.Out, "  env %-28s %s\n", v, state)
		}
		if missing {
			fmt.Fprintf(stdio.Out, "  FAIL  set the missing variables in the environment or in %s\n",
				filepath.Join(filepath.Dir(c.Path), ".env"))
			bad++
			continue
		}
		if s.IsOAuth() {
			st := oauth.NewStore(c).Status(n, mustResolvedURL(s))
			fmt.Fprintf(stdio.Out, "  oauth %s\n", st)
			if st != "logged in" && st != "logged in (refreshable)" {
				fmt.Fprintf(stdio.Out, "  FAIL  run `sluice login %s` (opens a browser; a human must do this)\n", n)
				bad++
				continue
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeoutFor(o, s))
		start := time.Now()
		sess, err := client.Connect(ctx, n, s, clientOptions(o, c, n))
		err = loginHint(err, n, s)
		if err == nil {
			var tools []*mcp.Tool
			tools, err = sess.ListTools(ctx)
			sess.Close()
			if err == nil {
				p := policyOf(s)
				allowed := 0
				for _, t := range tools {
					if p.Check(t.Name, readOnlyOf(t)).Allowed {
						allowed++
					}
				}
				fmt.Fprintf(stdio.Out, "  OK    %d tools, %d allowed (%s)\n", len(tools), allowed, time.Since(start).Round(time.Millisecond))
			}
		}
		cancel()
		if err != nil {
			fmt.Fprintf(stdio.Out, "  FAIL  %s\n", indent(err.Error(), "        "))
			bad++
		}
	}
	if bad > 0 {
		return &ExitError{Code: ExitConnect, Err: fmt.Errorf("%d server(s) failed", bad)}
	}
	return nil
}

// ---- init / docs / version -------------------------------------------------

func cmdInit(o *opts, presetNames []string, stdio IO) error {
	p := o.configPath
	if p == "" {
		var err error
		if p, err = config.DefaultPath(); err != nil {
			return &ExitError{ExitUsage, err}
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil {
		fmt.Fprintf(stdio.Out, "config already exists: %s (left unchanged)\n", p)
	} else {
		if err := os.WriteFile(p, []byte(agentdocs.ConfigTemplate), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(stdio.Out, "created %s\n", p)
	}
	envPath := filepath.Join(filepath.Dir(p), ".env")
	if _, err := os.Stat(envPath); err != nil {
		if err := os.WriteFile(envPath, []byte(agentdocs.EnvTemplate), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(stdio.Out, "created %s (secrets go here)\n", envPath)
	}
	if len(presetNames) == 0 {
		fmt.Fprintln(stdio.Out, "next: `sluice presets` to see ready-made servers, then `sluice add <preset>`")
		return nil
	}
	o.configPath = p
	for _, name := range presetNames {
		if err := cmdAdd(o, []string{name}, stdio); err != nil {
			return err
		}
	}
	return nil
}

func mustResolvedURL(s *config.Server) string {
	if r, err := s.Resolved(); err == nil {
		return r.URL
	}
	return s.URL
}

func cmdGuide(stdio IO) error {
	_, err := io.WriteString(stdio.Out, agentdocs.Usage)
	return err
}

func cmdSnippet(stdio IO) error {
	_, err := io.WriteString(stdio.Out, agentdocs.Snippet)
	return err
}

func cmdVersion(stdio IO) error {
	fmt.Fprintf(stdio.Out, "sluice %s", client.Version)
	if client.Commit != "" {
		fmt.Fprintf(stdio.Out, " (%s, %s)", client.Commit, client.Date)
	}
	fmt.Fprintf(stdio.Out, " %s/%s\n", runtime.GOOS, runtime.GOARCH)
	return nil
}

// ---- helpers ---------------------------------------------------------------

func writeJSON(w io.Writer, v any, pretty bool) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}

func oneLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max-1]) + "…"
	}
	return s
}

func indent(s, pad string) string {
	return strings.ReplaceAll(s, "\n", "\n"+pad)
}
