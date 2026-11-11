package config

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// secretKeyRe matches names (env vars, headers, flags) that usually hold secrets.
var secretKeyRe = regexp.MustCompile(`(?i)(token|secret|passw(or)?d|pwd|api[_-]?key|access[_-]?key|account[_-]?key|private[_-]?key|authorization|credential|connection[_-]?string|cookie)`)

// secretValueRe matches values that look like credentials whatever they are called.
var secretValueRe = []*regexp.Regexp{
	regexp.MustCompile(`://[^/\s:@]+:[^/\s@<>$]+@`), // user:password@ in a URL
	regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bATATT[A-Za-z0-9_=-]{20,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`(?i)\bAccountKey=[^;\s]+`),
	regexp.MustCompile(`(?i)\bSharedAccessSignature=[^;\s]+`),
	regexp.MustCompile(`(?i)[?&](sig|token|access_token|api_?key|secret|password)=[^&\s]+`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{16,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.`), // JWT
}

// literal returns v without its ${env:VAR} references.
func literal(v string) string { return envRefRe.ReplaceAllString(v, "") }

func looksSecret(v string) bool {
	lit := literal(v)
	for _, re := range secretValueRe {
		if re.MatchString(lit) {
			return true
		}
	}
	return false
}

// namedSecret reports a value under a secret-sounding name that is written
// out instead of referencing an environment variable.
func namedSecret(key, v string) bool {
	return secretKeyRe.MatchString(key) && !envRefRe.MatchString(v) && strings.TrimSpace(v) != ""
}

// LiteralSecrets lists the places in s that hold what looks like a secret
// written as a plain value instead of a ${env:VAR} reference, for example
// "env.MONGO_URI" or "args[3]". It returns locations only, never values.
func LiteralSecrets(s *Server) []string {
	var out []string
	if looksSecret(s.Command) {
		out = append(out, "command")
	}
	if looksSecret(s.URL) {
		out = append(out, "url")
	}
	for i, a := range s.Args {
		flagName, flagVal, isKV := strings.Cut(a, "=")
		prevIsSecretFlag := i > 0 && strings.HasPrefix(s.Args[i-1], "-") &&
			!strings.Contains(s.Args[i-1], "=") && secretKeyRe.MatchString(s.Args[i-1])
		switch {
		case looksSecret(a),
			isKV && strings.HasPrefix(flagName, "-") && namedSecret(flagName, flagVal),
			prevIsSecretFlag && !strings.HasPrefix(a, "-") && namedSecret(s.Args[i-1], a):
			out = append(out, "args["+strconv.Itoa(i)+"]")
		}
	}
	for _, m := range []struct {
		prefix string
		kv     map[string]string
	}{{"env.", s.Env}, {"headers.", s.Headers}} {
		var keys []string
		for k, v := range m.kv {
			if looksSecret(v) || namedSecret(k, v) {
				keys = append(keys, m.prefix+k)
			}
		}
		sort.Strings(keys)
		out = append(out, keys...)
	}
	if s.Auth != nil && s.Auth.ClientSecret != "" && !envRefRe.MatchString(s.Auth.ClientSecret) {
		out = append(out, "auth.client_secret")
	}
	return out
}
