// Package toolcache keeps each server's last tool list on disk so `call` can
// skip a tools/list round trip when the tool needs nothing from it. The cache
// is an optimization only: entries expire after TTL, are keyed to the
// server's transport definition, and a stale entry just costs one retry.
package toolcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jiawei42/sluice/internal/config"
)

// TTL is how long a cached tool list is trusted.
const TTL = 24 * time.Hour

type entry struct {
	Key   string      `json:"key"`
	Saved time.Time   `json:"saved"`
	Tools []*mcp.Tool `json:"tools"`
}

func path(c *config.Config, server string) string {
	return filepath.Join(c.Dir(), "cache", server+".tools.json")
}

// key identifies the server definition, so editing the URL or command
// invalidates the cache. Secrets (headers, env) are not part of it.
func key(s *config.Server) string {
	b, _ := json.Marshal([]any{s.Transport, s.URL, s.Command, s.Args})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Save stores the tool list. Errors are ignored: the cache is best effort.
func Save(c *config.Config, server string, s *config.Server, tools []*mcp.Tool) {
	data, err := json.Marshal(entry{Key: key(s), Saved: time.Now().UTC(), Tools: tools})
	if err != nil {
		return
	}
	p := path(c, server)
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, p)
	}
}

// Lookup returns the cached definition of tool, if fresh.
func Lookup(c *config.Config, server string, s *config.Server, tool string) (*mcp.Tool, bool) {
	data, err := os.ReadFile(path(c, server))
	if err != nil {
		return nil, false
	}
	var e entry
	if json.Unmarshal(data, &e) != nil || e.Key != key(s) || time.Since(e.Saved) > TTL {
		return nil, false
	}
	for _, t := range e.Tools {
		if t != nil && t.Name == tool {
			return t, true
		}
	}
	return nil, false
}

// HasHeaderParams reports whether the tool mirrors arguments into HTTP
// headers (SEP-2243 "x-mcp-header"). The SDK only sends those headers for
// tools listed in the same session.
func HasHeaderParams(t *mcp.Tool) bool {
	b, err := json.Marshal(t.InputSchema)
	return err != nil || strings.Contains(string(b), `"x-mcp-header"`)
}
