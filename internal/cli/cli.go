// Package cli implements the sluice command line.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Exit codes. Agents rely on these, keep them stable (documented in USAGE.md).
const (
	ExitOK        = 0
	ExitToolError = 1 // the tool ran and reported an error (isError=true)
	ExitUsage     = 2 // bad arguments or config
	ExitDenied    = 3 // blocked by the whitelist
	ExitConnect   = 4 // could not reach the server / protocol failure
)

// ExitError carries an exit code.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }

func fail(code int, format string, a ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, a...)}
}

// IO bundles the standard streams so tests can capture them.
type IO struct {
	In       io.Reader
	Out, Err io.Writer
}

type opts struct {
	configPath string
	verbose    bool
	json       bool
	raw        bool
	timeout    time.Duration
	args       []kv // -a key=value (value parsed as JSON when possible)
	strs       []kv // -s key=value (value always a string)
	help       bool

	outFile   string // call --out FILE
	maxOutput *int   // call --max-output N (0 = unlimited)

	name      string // add --name
	force     bool   // add --force
	noBrowser bool   // login --no-browser

	// add, custom servers
	dash       bool     // "--" was given
	dashArgs   []string // everything after "--" (command and args)
	url        string   // --url
	oauth      bool     // --oauth
	envs       []string // --env VAR or CHILD=VAR
	bearer     string   // --bearer VAR
	headerEnvs []string // --header-env Name=VAR
	allow      []string // --allow PATTERN
	deny       []string // --deny PATTERN

	since        time.Duration // audit --since
	serverFilter string        // audit --server
	deniedOnly   bool          // audit --denied
	limit        int           // audit --limit (-1 = all)
}

type kv struct{ key, val string }

const usageText = `sluice — governed access to MCP servers for AI agents and scripts.
Every server has a whitelist; anything not allowed is refused before it is sent.

For agents:
  sluice servers                         list configured servers
  sluice tools  <server>                 list the tools you may use
  sluice schema <server> <tool>          show a tool's input schema
  sluice call   <server> <tool> [JSON]   call a tool (JSON | - for stdin | @file.json)
  sluice guide                           full usage guide for AI agents
  sluice agents-snippet                  short text for AGENTS.md / CLAUDE.md / GEMINI.md

Setup (for humans):
  sluice init [preset...]                create the config (optionally adding presets)
  sluice presets                         list ready-made servers (GitHub, Vercel, Linear, ...)
  sluice add <preset> [--name N]         add a server with a safe default whitelist
  sluice add <name> -- <command> [args]  add your own stdio server (nothing allowed until you allow it)
  sluice add <name> --url URL [--oauth]  add your own HTTP server
  sluice remove <server>                 remove a server added with ` + "`add`" + `
  sluice login <server>                  sign in to an OAuth server in your browser
  sluice logout <server>                 delete the stored OAuth token
  sluice doctor [server]                 check env vars, logins and connectivity
  sluice policy <server>                 show every tool and the whitelist decision
  sluice audit                           show recent calls (allowed and denied)
  sluice version

Flags:
  -a, --arg key=value    add an argument (value parsed as JSON if valid, else string)
  -s, --str key=value    add a string argument (never parsed)
      --out FILE         call: save the full result to FILE, print only a summary
      --max-output N     call: cap printed bytes (default from config, 100000; 0 = no cap)
      --json             machine-readable output (servers, tools, presets, audit)
      --raw              call: print the full MCP CallToolResult as JSON
      --timeout 90s      override the server timeout
      --config PATH      config file (default: $SLUICE_CONFIG or the user config dir)
      --env VAR          add (stdio): pass ${env:VAR} to the server; CHILD=VAR renames it
      --bearer VAR       add (http): send "Authorization: Bearer ${env:VAR}"
      --header-env H=VAR add (http): send header H with the value of VAR
      --allow / --deny P add (custom): whitelist glob, repeatable
      --since 24h        audit: only newer entries      --server NAME   audit: one server
      --denied           audit: only denied calls       --limit N       audit: last N (default 50, -1 all)
  -v, --verbose          show server stderr

Exit codes: 0 ok, 1 tool error, 2 usage/config, 3 denied by whitelist, 4 connection or login needed.
`

// Run executes sluice with args (without the program name) and returns the exit code.
func Run(args []string, stdio IO) int {
	err := run(args, stdio)
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		if ee.Err != nil && ee.Err.Error() != "" {
			fmt.Fprintln(stdio.Err, "sluice:", ee.Err)
		}
		return ee.Code
	}
	fmt.Fprintln(stdio.Err, "sluice:", err)
	return ExitUsage
}

func run(args []string, stdio IO) error {
	o, pos, err := parseFlags(args)
	if err != nil {
		return &ExitError{ExitUsage, err}
	}
	if len(pos) == 0 || o.help && len(pos) <= 1 {
		fmt.Fprint(stdio.Out, usageText)
		if len(pos) == 0 && !o.help {
			return &ExitError{Code: ExitUsage, Err: errors.New("")}
		}
		return nil
	}
	cmd, rest := pos[0], pos[1:]
	switch cmd {
	case "help":
		fmt.Fprint(stdio.Out, usageText)
		return nil
	case "version", "--version":
		return cmdVersion(stdio)
	case "guide":
		return cmdGuide(stdio)
	case "agents-snippet":
		return cmdSnippet(stdio)
	case "init":
		return cmdInit(o, rest, stdio)
	case "presets":
		return cmdPresets(o, rest, stdio)
	case "add":
		return cmdAdd(o, rest, stdio)
	case "remove", "rm":
		return cmdRemove(o, rest, stdio)
	case "login":
		return cmdLogin(o, rest, stdio)
	case "logout":
		return cmdLogout(o, rest, stdio)
	case "audit":
		return cmdAudit(o, rest, stdio)
	case "servers":
		return cmdServers(o, rest, stdio)
	case "tools":
		return cmdTools(o, rest, stdio)
	case "schema":
		return cmdSchema(o, rest, stdio)
	case "call":
		return cmdCall(o, rest, stdio)
	case "policy":
		return cmdPolicy(o, rest, stdio)
	case "doctor":
		return cmdDoctor(o, rest, stdio)
	default:
		return fail(ExitUsage, "unknown command %q (run `sluice help`)", cmd)
	}
}

// parseFlags accepts flags anywhere on the line; "--" ends flag parsing.
// A lone "-" is a positional (stdin marker).
func parseFlags(args []string) (*opts, []string, error) {
	o := &opts{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			o.dash, o.dashArgs = true, args[i+1:]
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(a, "=")
		if strings.HasPrefix(name, "-") && !strings.HasPrefix(name, "--") && len(name) > 2 {
			// -akey=value style is not supported; treat "-ab" as unknown.
			hasVal = false
			name, val = a, ""
		}
		needVal := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", name)
			}
			i++
			return args[i], nil
		}
		switch name {
		case "-h", "--help":
			o.help = true
		case "-v", "--verbose":
			o.verbose = true
		case "--json":
			o.json = true
		case "--raw":
			o.raw = true
		case "--config":
			v, err := needVal()
			if err != nil {
				return nil, nil, err
			}
			o.configPath = v
		case "--timeout":
			v, err := needVal()
			if err != nil {
				return nil, nil, err
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, nil, fmt.Errorf("bad --timeout %q: %w", v, err)
			}
			o.timeout = d
		case "-a", "--arg", "-s", "--str":
			v, err := needVal()
			if err != nil {
				return nil, nil, err
			}
			k, vv, ok := strings.Cut(v, "=")
			if !ok || k == "" {
				return nil, nil, fmt.Errorf("flag %s expects key=value, got %q", name, v)
			}
			if name == "-a" || name == "--arg" {
				o.args = append(o.args, kv{k, vv})
			} else {
				o.strs = append(o.strs, kv{k, vv})
			}
		case "--force":
			o.force = true
		case "--no-browser":
			o.noBrowser = true
		case "--denied":
			o.deniedOnly = true
		case "--oauth":
			o.oauth = true
		case "--url", "--env", "--bearer", "--header-env", "--allow", "--deny":
			v, err := needVal()
			if err != nil {
				return nil, nil, err
			}
			switch name {
			case "--url":
				o.url = v
			case "--env":
				o.envs = append(o.envs, v)
			case "--bearer":
				o.bearer = v
			case "--header-env":
				o.headerEnvs = append(o.headerEnvs, v)
			case "--allow":
				o.allow = append(o.allow, v)
			case "--deny":
				o.deny = append(o.deny, v)
			}
		case "--out", "--name", "--server", "--since", "--max-output", "--limit":
			v, err := needVal()
			if err != nil {
				return nil, nil, err
			}
			switch name {
			case "--out":
				o.outFile = v
			case "--name":
				o.name = v
			case "--server":
				o.serverFilter = v
			case "--since":
				d, err := parseSince(v)
				if err != nil {
					return nil, nil, fmt.Errorf("bad --since %q (use e.g. 30m, 24h, 7d)", v)
				}
				o.since = d
			case "--max-output", "--limit":
				n, err := strconv.Atoi(v)
				if err != nil || n < -1 || (name == "--max-output" && n < 0) {
					return nil, nil, fmt.Errorf("bad %s %q", name, v)
				}
				if name == "--limit" {
					o.limit = n
				} else {
					o.maxOutput = &n
				}
			}
		default:
			return nil, nil, fmt.Errorf("unknown flag %s", name)
		}
	}
	return o, pos, nil
}

// parseSince accepts Go durations plus a "d" (days) suffix.
func parseSince(v string) (time.Duration, error) {
	if strings.HasSuffix(v, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(v, "d"))
		if err != nil || n < 0 {
			return 0, errors.New("bad days")
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(v)
}
