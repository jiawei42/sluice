// Package client opens one MCP session to a configured server.
package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jiawei42/local-mcp/internal/config"
)

// Version is reported to servers as the client version.
var Version = "dev"

// Session is an open connection to one server.
type Session struct {
	Name   string
	cs     *mcp.ClientSession
	stderr *tailBuffer
}

// Connect starts (stdio) or contacts (http) the server and initializes MCP.
// When verbose is false the server's stderr is kept only to explain failures.
func Connect(ctx context.Context, name string, srv *config.Server, verbose bool) (*Session, error) {
	s, err := srv.Resolved()
	if err != nil {
		return nil, fmt.Errorf("server %q: %w", name, err)
	}
	sess := &Session{Name: name, stderr: &tailBuffer{max: 4096}}

	var transport mcp.Transport
	switch s.Transport {
	case "stdio":
		cmd := exec.Command(s.Command, s.Args...)
		cmd.Env = os.Environ()
		for k, v := range s.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		cmd.Dir = s.Cwd
		if verbose {
			cmd.Stderr = io.MultiWriter(os.Stderr, sess.stderr)
		} else {
			cmd.Stderr = sess.stderr
		}
		transport = &mcp.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}
	case "http":
		transport = &mcp.StreamableClientTransport{
			Endpoint:             s.URL,
			HTTPClient:           &http.Client{Transport: &headerTransport{headers: s.Headers, base: http.DefaultTransport}},
			MaxRetries:           1,
			DisableStandaloneSSE: true, // one-shot CLI: only request/response is needed
		}
	default:
		return nil, fmt.Errorf("server %q: unknown transport %q", name, s.Transport)
	}

	c := mcp.NewClient(&mcp.Implementation{Name: "mcpx", Version: Version}, nil)
	cs, err := c.Connect(ctx, transport, nil)
	if err != nil {
		return nil, sess.wrap(fmt.Errorf("connect to %q: %w", name, err))
	}
	sess.cs = cs
	return sess, nil
}

// ListTools returns every tool the server exposes (all pages).
func (s *Session) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	for t, err := range s.cs.Tools(ctx, nil) {
		if err != nil {
			return nil, s.wrap(fmt.Errorf("list tools on %q: %w", s.Name, err))
		}
		tools = append(tools, t)
	}
	return tools, nil
}

// FindTool returns the named tool or nil.
func (s *Session) FindTool(ctx context.Context, name string) (*mcp.Tool, error) {
	tools, err := s.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tools {
		if t.Name == name {
			return t, nil
		}
	}
	return nil, nil
}

// CallTool calls one tool.
func (s *Session) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	res, err := s.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, s.wrap(fmt.Errorf("call %s/%s: %w", s.Name, name, err))
	}
	return res, nil
}

// Close ends the session (and the server process for stdio).
func (s *Session) Close() error {
	if s.cs == nil {
		return nil
	}
	return s.cs.Close()
}

// wrap appends the tail of the server's stderr, which usually explains
// failures such as a missing token or Docker not running.
func (s *Session) wrap(err error) error {
	tail := strings.TrimSpace(s.stderr.String())
	if tail == "" {
		return err
	}
	return fmt.Errorf("%w\n--- server stderr (last lines) ---\n%s", err, tail)
}

type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (h *headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if len(h.headers) > 0 {
		r = r.Clone(r.Context())
		for k, v := range h.headers {
			r.Header.Set(k, v)
		}
	}
	return h.base.RoundTrip(r)
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
