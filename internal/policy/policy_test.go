package policy

import "testing"

func TestCheck(t *testing.T) {
	yes, no := true, false
	p := Policy{Allow: []string{"get_*", "list_*", "create_issue"}, Deny: []string{"list_secrets", "delete_*"}}
	cases := []struct {
		name string
		ro   *bool
		want bool
	}{
		{"get_me", nil, true},
		{"list_issues", nil, true},
		{"create_issue", nil, true},
		{"create_issue_comment", nil, false}, // exact name only
		{"list_secrets", nil, false},         // deny wins
		{"delete_file", &yes, false},
		{"push_files", nil, false},
		{"peek", &yes, false}, // allow_readonly off
	}
	for _, c := range cases {
		if got := p.Check(c.name, c.ro).Allowed; got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}

	p.AllowReadOnly = true
	if !p.Check("peek", &yes).Allowed {
		t.Error("allow_readonly should allow read-only tool")
	}
	if p.Check("peek", &no).Allowed || p.Check("delete_file", &yes).Allowed {
		t.Error("allow_readonly must not allow writable or denied tools")
	}
	if !p.NeedsMetadata("peek") || p.NeedsMetadata("get_me") || p.NeedsMetadata("delete_file") {
		t.Error("NeedsMetadata wrong")
	}
}
