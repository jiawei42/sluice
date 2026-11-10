// Package audit appends one JSON line per tool call (allowed or denied) to a
// local log, so the user can see what agents did. Argument values are only
// recorded when the user opts in; by default only their names and a hash.
package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaxSize is the size at which the log is rotated to <path>.1.
const MaxSize = 10 << 20

// Entry is one logged call.
type Entry struct {
	Time       time.Time      `json:"time"`
	Server     string         `json:"server"`
	Tool       string         `json:"tool"`
	Decision   string         `json:"decision"` // "allowed" or "denied"
	Reason     string         `json:"reason,omitempty"`
	Exit       int            `json:"exit"`
	DurationMs int64          `json:"duration_ms"`
	ArgKeys    []string       `json:"arg_keys,omitempty"`
	ArgsHash   string         `json:"args_sha256,omitempty"`
	Args       map[string]any `json:"args,omitempty"`
	OutBytes   int            `json:"out_bytes,omitempty"`
	Truncated  bool           `json:"truncated,omitempty"`
	Error      string         `json:"error,omitempty"`
	Agent      string         `json:"agent,omitempty"`
	Cwd        string         `json:"cwd,omitempty"`
}

// SetArgs fills ArgKeys and ArgsHash, plus Args when logValues is true.
func (e *Entry) SetArgs(args map[string]any, logValues bool) {
	if len(args) == 0 {
		return
	}
	for k := range args {
		e.ArgKeys = append(e.ArgKeys, k)
	}
	sort.Strings(e.ArgKeys)
	if b, err := json.Marshal(args); err == nil { // map keys marshal sorted: stable hash
		sum := sha256.Sum256(b)
		e.ArgsHash = hex.EncodeToString(sum[:8])
	}
	if logValues {
		e.Args = args
	}
}

// SetError records the first line of err, shortened.
func (e *Entry) SetError(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	e.Error = msg
}

// Append writes e to the log at path, rotating when it grows past MaxSize.
func Append(path string, e Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > MaxSize {
		_ = os.Rename(path, path+".1")
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// Read returns all entries (oldest first), including the rotated file.
// Malformed lines are skipped.
func Read(path string) ([]Entry, error) {
	var out []Entry
	for _, p := range []string{path + ".1", path} {
		f, err := os.Open(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 4<<20)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				out = append(out, e)
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DetectAgent guesses which agent runs sluice from its environment.
// SLUICE_AGENT overrides the guess.
func DetectAgent() string {
	if v := os.Getenv("SLUICE_AGENT"); v != "" {
		return v
	}
	switch {
	case os.Getenv("CLAUDECODE") != "":
		return "claude-code"
	case os.Getenv("GEMINI_CLI") != "":
		return "gemini-cli"
	case os.Getenv("CURSOR_AGENT") != "" || os.Getenv("CURSOR_TRACE_ID") != "":
		return "cursor"
	// Variables Codex sets for the commands it runs (not CODEX_HOME, which
	// users export in their shell profile).
	case os.Getenv("CODEX_SANDBOX") != "" || os.Getenv("CODEX_SANDBOX_NETWORK_DISABLED") != "" ||
		os.Getenv("CODEX_THREAD_ID") != "":
		return "codex"
	}
	return ""
}
