package audit

import "testing"

func TestDetectAgent(t *testing.T) {
	for _, k := range []string{"SLUICE_AGENT", "CLAUDECODE", "GEMINI_CLI", "CURSOR_AGENT", "CURSOR_TRACE_ID",
		"CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "CODEX_THREAD_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("CODEX_HOME", "/home/me/.codex") // a user setting, not a running agent
	if got := DetectAgent(); got != "" {
		t.Fatalf("CODEX_HOME alone must not mean codex, got %q", got)
	}
	t.Setenv("CODEX_SANDBOX", "seatbelt")
	if got := DetectAgent(); got != "codex" {
		t.Fatalf("want codex, got %q", got)
	}
	t.Setenv("SLUICE_AGENT", "my-bot")
	if got := DetectAgent(); got != "my-bot" {
		t.Fatalf("SLUICE_AGENT must win, got %q", got)
	}
}
