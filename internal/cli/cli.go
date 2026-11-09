// Package cli implements the mcpx command line.
package cli

import (
	"errors"
	"fmt"
	"io"
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
}

type kv struct{ key, val string }

const usageText = `mcpx — call MCP servers from the shell, with a per-server whitelist.

Usage:
  mcpx servers                         list configured servers
  mcpx tools  <server>                 list tools you are allowed to use
  mcpx schema <server> <tool>          show a tool's input schema
  mcpx call   <server> <tool> [JSON]   call a tool (JSON | - for stdin | @file.json)
  mcpx guide                           full usage guide for AI agents
  mcpx agents-snippet                  short text to paste into AGENTS.md / CLAUDE.md / GEMINI.md

Setup (for humans):
  mcpx init                            create a starter config
  mcpx doctor [server]                 check config, env vars and connectivity
  mcpx policy <server>                 show every tool and whether the whitelist allows it
  mcpx version

Flags:
  -a, --arg key=value    add an argument (value parsed as JSON if valid, else string)
  -s, --str key=value    add a string argument (never parsed)
      --json             machine-readable output (servers, tools)
      --raw              print the full MCP CallToolResult as JSON (call)
      --timeout 90s      override the server timeout
      --config PATH      config file (default: $MCPX_CONFIG or the user config dir)
  -v, --verbose          show server stderr

Exit codes: 0 ok, 1 tool error, 2 usage/config, 3 denied by whitelist, 4 connection error.
`

// Run executes mcpx with args (without the program name) and returns the exit code.
func Run(args []string, stdio IO) int {
	err := run(args, stdio)
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		if ee.Err != nil && ee.Err.Error() != "" {
			fmt.Fprintln(stdio.Err, "mcpx:", ee.Err)
		}
		return ee.Code
	}
	fmt.Fprintln(stdio.Err, "mcpx:", err)
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
		return cmdInit(o, stdio)
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
		return fail(ExitUsage, "unknown command %q (run `mcpx help`)", cmd)
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
		default:
			return nil, nil, fmt.Errorf("unknown flag %s", name)
		}
	}
	return o, pos, nil
}
