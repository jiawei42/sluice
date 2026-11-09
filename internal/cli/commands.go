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
	"sort"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	agentdocs "github.com/jiawei42/local-mcp/docs/agents"
	"github.com/jiawei42/local-mcp/internal/client"
	"github.com/jiawei42/local-mcp/internal/config"
	"github.com/jiawei42/local-mcp/internal/policy"
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

func connect(ctx context.Context, o *opts, name string, s *config.Server) (*client.Session, error) {
	sess, err := client.Connect(ctx, name, s, o.verbose)
	if err != nil {
		return nil, &ExitError{ExitConnect, err}
	}
	return sess, nil
}

func asConnectErr(err error) error {
	var ee *ExitError
	if errors.As(err, &ee) {
		return err
	}
	return &ExitError{ExitConnect, err}
}

func deniedErr(c *config.Config, server, tool, reason string) error {
	return fail(ExitDenied,
		"tool %s/%s is not allowed by the whitelist (%s). Do not try to work around this; "+
			"ask the user to add it to \"allow\" for server %q in %s if it is really needed.",
		server, tool, reason, server, c.Path)
}

// ---- servers ---------------------------------------------------------------

func cmdServers(o *opts, rest []string, stdio IO) error {
	if len(rest) != 0 {
		return fail(ExitUsage, "usage: mcpx servers [--json]")
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
		return fail(ExitUsage, "usage: mcpx tools <server> [--json]")
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
	sess, err := connect(ctx, o, rest[0], s)
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
		fmt.Fprintf(stdio.Err, "mcpx: no tools of %q are allowed by the whitelist (%d exist)\n", rest[0], len(all))
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
		return fail(ExitUsage, "usage: mcpx schema <server> <tool>")
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
		return deniedErr(c, server, tool, d.Reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutFor(o, s))
	defer cancel()
	sess, err := connect(ctx, o, server, s)
	if err != nil {
		return err
	}
	defer sess.Close()
	t, err := sess.FindTool(ctx, tool)
	if err != nil {
		return asConnectErr(err)
	}
	if t == nil {
		return fail(ExitUsage, "server %q has no tool %q (run `mcpx tools %s`)", server, tool, server)
	}
	if d := p.Check(tool, readOnlyOf(t)); !d.Allowed {
		return deniedErr(c, server, tool, d.Reason)
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
		return fail(ExitUsage, "usage: mcpx call <server> <tool> [JSON | - | @file.json] [-a key=value ...]")
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
	p := policyOf(s)
	d := p.Check(tool, nil)
	if !d.Allowed && !p.NeedsMetadata(tool) {
		return deniedErr(c, server, tool, d.Reason)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutFor(o, s))
	defer cancel()
	sess, err := connect(ctx, o, server, s)
	if err != nil {
		return err
	}
	defer sess.Close()
	if !d.Allowed { // only allow_readonly can still allow it: look at annotations
		t, err := sess.FindTool(ctx, tool)
		if err != nil {
			return asConnectErr(err)
		}
		if t == nil {
			return fail(ExitUsage, "server %q has no tool %q (run `mcpx tools %s`)", server, tool, server)
		}
		if d = p.Check(tool, readOnlyOf(t)); !d.Allowed {
			return deniedErr(c, server, tool, d.Reason)
		}
	}
	res, err := sess.CallTool(ctx, tool, args)
	if err != nil {
		return asConnectErr(err)
	}
	if o.raw {
		if err := writeJSON(stdio.Out, res, true); err != nil {
			return err
		}
	} else {
		w := stdio.Out
		if res.IsError {
			w = stdio.Err
		}
		if err := writeResult(w, res); err != nil {
			return err
		}
	}
	if res.IsError {
		return &ExitError{Code: ExitToolError, Err: errors.New("")}
	}
	return nil
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
		return fail(ExitUsage, "usage: mcpx policy <server>")
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
	sess, err := connect(ctx, o, rest[0], s)
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
	fmt.Fprintf(stdio.Out, "\n%d of %d tools allowed. Edit %s to change.\n", n, len(all), c.Path)
	return nil
}

// ---- doctor ----------------------------------------------------------------

func cmdDoctor(o *opts, rest []string, stdio IO) error {
	if len(rest) > 1 {
		return fail(ExitUsage, "usage: mcpx doctor [server]")
	}
	c, err := loadConfig(o)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdio.Out, "config: %s\n", c.Path)
	names := c.Names()
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
		ctx, cancel := context.WithTimeout(context.Background(), timeoutFor(o, s))
		start := time.Now()
		sess, err := client.Connect(ctx, n, s, o.verbose)
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

func cmdInit(o *opts, stdio IO) error {
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
		fmt.Fprintf(stdio.Out, "created %s (put your tokens here)\n", envPath)
	}
	fmt.Fprintln(stdio.Out, "next: fill in the tokens, then run `mcpx doctor` and `mcpx policy <server>`")
	return nil
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
	fmt.Fprintf(stdio.Out, "mcpx %s\n", client.Version)
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
