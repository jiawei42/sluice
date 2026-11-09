// Package policy decides which tools of a server may be listed and called.
// The rule is deny-by-default: a tool is allowed only when it matches an
// allow pattern (or allow_readonly applies) and matches no deny pattern.
package policy

import (
	"fmt"
	"path"
)

// Policy is the per-server whitelist.
type Policy struct {
	Allow         []string
	Deny          []string
	AllowReadOnly bool
}

// Decision is the outcome of Check.
type Decision struct {
	Allowed bool
	Reason  string
}

// Check decides for tool name. readOnly is the tool's readOnlyHint annotation
// if known; pass nil when the tool's metadata has not been fetched.
func (p Policy) Check(name string, readOnly *bool) Decision {
	for _, pat := range p.Deny {
		if ok, _ := path.Match(pat, name); ok {
			return Decision{false, fmt.Sprintf("matches deny pattern %q", pat)}
		}
	}
	for _, pat := range p.Allow {
		if ok, _ := path.Match(pat, name); ok {
			return Decision{true, fmt.Sprintf("matches allow pattern %q", pat)}
		}
	}
	if p.AllowReadOnly && readOnly != nil && *readOnly {
		return Decision{true, "server marks it read-only (allow_readonly)"}
	}
	return Decision{false, "not in allow list"}
}

// NeedsMetadata reports whether Check for name could change once the tool's
// annotations are known (i.e. only allow_readonly could still allow it).
func (p Policy) NeedsMetadata(name string) bool {
	d := p.Check(name, nil)
	return !d.Allowed && p.AllowReadOnly && d.Reason == "not in allow list"
}
