package config

import (
	"encoding/json"
	"testing"
)

func TestStripJSONComments(t *testing.T) {
	in := `// top
{
  "a": "http://x//y", /* block
  comment */ "b": "say \"//hi\"", // tail
  "c": 1
}`
	var v map[string]any
	if err := json.Unmarshal([]byte(StripJSONComments(in)), &v); err != nil {
		t.Fatal(err)
	}
	if v["a"] != "http://x//y" || v["b"] != `say "//hi"` || v["c"] != 1.0 {
		t.Fatalf("got %#v", v)
	}
}

func TestExpand(t *testing.T) {
	t.Setenv("SLUICE_A", "one")
	got, err := Expand("Bearer ${env:SLUICE_A} $HOME ${notenv}")
	if err != nil || got != "Bearer one $HOME ${notenv}" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := Expand("${env:SLUICE_DEFINITELY_UNSET}"); err == nil {
		t.Fatal("want error for unset var")
	}
}

func TestValidate(t *testing.T) {
	bad := []Config{
		{Servers: map[string]*Server{"x": {Command: "a", Auth: &Auth{Type: "oauth"}}}},
		{Servers: map[string]*Server{"x": {URL: "http://a", Auth: &Auth{Type: "basic"}}}},
		{Servers: map[string]*Server{"x": {}}},
		{Servers: map[string]*Server{"bad name": {Command: "a"}}},
		{Servers: map[string]*Server{"x": {Command: "a", Allow: []string{"["}}}},
		{Servers: map[string]*Server{"x": {URL: "http://a", Timeout: "soon"}}},
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: want error", i)
		}
	}
	if err := (&Config{}).Validate(); err != nil {
		t.Fatalf("empty config should be valid: %v", err)
	}
	ok := Config{Servers: map[string]*Server{"x": {URL: "http://a"}, "y": {Command: "a"}}}
	if err := ok.Validate(); err != nil || ok.Servers["x"].Transport != "http" || ok.Servers["y"].Transport != "stdio" {
		t.Fatalf("inference failed: %v", err)
	}
}

func TestDotEnvValue(t *testing.T) {
	cases := map[string]string{
		`abc`:                       "abc",
		`abc # comment`:             "abc",
		"abc\t# comment":            "abc",
		`a#b`:                       "a#b",
		`"quoted # kept" # comment`: "quoted # kept",
		`'single'`:                  "single",
		`  spaced  `:                "spaced",
		`"unterminated`:             `"unterminated`,
		``:                          "",
		`# only a comment`:          "# only a comment",
	}
	for in, want := range cases {
		if got := dotEnvValue(in); got != want {
			t.Errorf("dotEnvValue(%q) = %q, want %q", in, got, want)
		}
	}
}
