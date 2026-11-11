package config

import (
	"reflect"
	"testing"
)

// Values below are made up; none is a real credential.
func TestLiteralSecrets(t *testing.T) {
	cases := []struct {
		name string
		srv  Server
		want []string
	}{
		{"env refs are fine", Server{
			Env:     map[string]string{"GITHUB_TOKEN": "${env:GITHUB_TOKEN}", "MONGO_URI": "${env:MONGO_URI}"},
			Headers: map[string]string{"Authorization": "Bearer ${env:API_TOKEN}"},
		}, nil},
		{"named secrets", Server{
			Env:     map[string]string{"JIRA_API_TOKEN": "abc123", "MONGO_DB": "app", "DB_PASSWORD": "hunter2"},
			Headers: map[string]string{"X-Api-Key": "k-123", "Accept": "application/json"},
		}, []string{"env.DB_PASSWORD", "env.JIRA_API_TOKEN", "headers.X-Api-Key"}},
		{"secret-looking values", Server{
			URL: "https://example.com/mcp?token=abcdef",
			Env: map[string]string{
				"MONGO_URI": "mongodb+srv://app:pa55word@cluster.example.net/",
				"AZURE":     "DefaultEndpointsProtocol=https;AccountName=a;AccountKey=Zm9vYmFy;",
			},
			Headers: map[string]string{"Authorization": "Bearer abcdefghijklmnopqrstuvwxyz"},
		}, []string{"url", "env.AZURE", "env.MONGO_URI", "headers.Authorization"}},
		{"placeholders are not secrets", Server{
			Env: map[string]string{"MONGO_URI": "mongodb+srv://<user>:<password>@cluster.example.net/"},
		}, nil},
		{"args", Server{
			Args: []string{"--verbose", "--token", "abc123", "--api-key=xyz", "--name", "app", "ghp_abcdefghijklmnopqrstuvwxyz0123"},
		}, []string{"args[2]", "args[3]", "args[6]"}},
		{"oauth client secret", Server{Auth: &Auth{Type: "oauth", ClientSecret: "literal"}}, []string{"auth.client_secret"}},
		{"oauth client secret ref", Server{Auth: &Auth{Type: "oauth", ClientSecret: "${env:CS}"}}, nil},
		{"supabase read_only query", Server{URL: "https://mcp.supabase.com/mcp?read_only=true"}, nil},
	}
	for _, c := range cases {
		if got := LiteralSecrets(&c.srv); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
