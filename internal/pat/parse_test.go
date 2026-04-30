package pat

import (
	"reflect"
	"testing"
	"time"
)

// today is fixed so "Created today." resolves deterministically.
var today = time.Date(2026, time.April, 29, 0, 0, 0, 0, time.UTC)

func TestParse_RealisticPaste(t *testing.T) {
	body := `Skip to content
Test PAT

Test description

Created today.
Expires on Tue, Jul 28 2026
Access on @example-user example-user
Repository access
example-user/agentic-worktrees-mcp
example-user/agentic-sandbox
User permissions
This token does not have any user permissions.
Repository permissions
 Read access to issues and metadata
 Read and Write access to code and workflows
Footer
© 2026 GitHub, Inc.
Footer navigation
Terms
Privacy
Security
Status
Community
Docs
Contact
Manage cookies
Do not share my personal information
`

	got := Parse(body, today)
	want := ParsedPaste{
		Name:        "Test PAT",
		Description: "Test description",
		Owner:       "example-user",
		ExpiresAt:   "2026-07-28",
		CreatedAt:   "2026-04-29",
		Repos: []string{
			"example-user/agentic-worktrees-mcp",
			"example-user/agentic-sandbox",
		},
		Perms: []string{
			"issues: read",
			"metadata: read",
			"code: write",
			"workflows: write",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse mismatch.\n got: %+v\nwant: %+v", got, want)
	}
}

func TestParse_NoDescriptionLiteral(t *testing.T) {
	body := `My Token

No description

Created on Mon, Apr 27 2026.
Expires on Wed, May 27 2026
Access on @owner owner
Repository access
owner/repo
Repository permissions
 Read access to contents
`
	got := Parse(body, today)
	if got.Description != "" {
		t.Fatalf("expected empty description for literal \"No description\", got %q", got.Description)
	}
	if got.CreatedAt != "2026-04-27" {
		t.Fatalf("CreatedAt: got %q want 2026-04-27", got.CreatedAt)
	}
	if got.ExpiresAt != "2026-05-27" {
		t.Fatalf("ExpiresAt: got %q want 2026-05-27", got.ExpiresAt)
	}
	if !reflect.DeepEqual(got.Perms, []string{"contents: read"}) {
		t.Fatalf("Perms: got %v want [contents: read]", got.Perms)
	}
}

func TestParse_MissingFieldsAreEmpty(t *testing.T) {
	// Pathological paste: no anchors anywhere. We capture the title and
	// nothing else — no panic, no error, just empty fields.
	body := `Skip to content
Stripped Token Page

Random words about nothing.
`
	got := Parse(body, today)
	if got.Name != "Stripped Token Page" {
		t.Fatalf("Name: got %q", got.Name)
	}
	if got.Owner != "" || got.ExpiresAt != "" || got.CreatedAt != "" {
		t.Fatalf("expected empty anchors, got %+v", got)
	}
	if len(got.Repos) != 0 || len(got.Perms) != 0 {
		t.Fatalf("expected empty scopes, got %+v", got)
	}
}

func TestParse_PermsFallbackOnUnknownShape(t *testing.T) {
	body := `Token

No description

Repository permissions
 Some weird future GitHub copy that doesn't match the template
`
	got := Parse(body, today)
	want := []string{"Some weird future GitHub copy that doesn't match the template"}
	if !reflect.DeepEqual(got.Perms, want) {
		t.Fatalf("Perms: got %v want %v", got.Perms, want)
	}
}

func TestExplodePerm(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Read access to issues and metadata", []string{"issues: read", "metadata: read"}},
		{"Read and Write access to code and workflows", []string{"code: write", "workflows: write"}},
		{"Read and Write access to actions, code, and workflows", []string{"actions: write", "code: write", "workflows: write"}},
		{"Read access to contents", []string{"contents: read"}},
		{"Admin access to administration", []string{"administration: admin"}},
		{"weird input", []string{"weird input"}},
	}
	for _, tc := range cases {
		got := explodePerm(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("explodePerm(%q): got %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestParse_AllReposAndUserPerms(t *testing.T) {
	body := `Skip to content
My Awesome PAT

This is a very important description

Created today.
Expires on Fri, May 29 2026
Access on @example-user example-user
Repository access
This token has access to all repositories owned by you.
User permissions
 Read access to user copilot requests
Repository permissions
 Read access to actions and metadata
Footer
© 2026 GitHub, Inc.
Footer navigation
Terms
Privacy
`
	got := Parse(body, today)
	if !got.ReposAll {
		t.Errorf("ReposAll should be true for the all-repos sentinel")
	}
	if len(got.Repos) != 0 {
		t.Errorf("Repos should be empty when the sentinel matched: %v", got.Repos)
	}
	if got.Owner != "example-user" {
		t.Errorf("Owner = %q", got.Owner)
	}
	wantUserPerms := []string{"user copilot requests: read"}
	if !reflect.DeepEqual(got.UserPerms, wantUserPerms) {
		t.Errorf("UserPerms = %v, want %v", got.UserPerms, wantUserPerms)
	}
	wantPerms := []string{"actions: read", "metadata: read"}
	if !reflect.DeepEqual(got.Perms, wantPerms) {
		t.Errorf("Perms = %v, want %v", got.Perms, wantPerms)
	}
	if got.Description != "This is a very important description" {
		t.Errorf("Description = %q", got.Description)
	}
}

func TestParse_OrgOwnedAccessLine(t *testing.T) {
	// Org-owned PATs render the access line as
	// "Access on the @<org> <org> organization" instead of the user-owned
	// "Access on @<login> <login>". Both must populate Owner.
	body := `Skip to content
example-token

No description

Created on Sat, Mar 21 2026.
Expires on Thu, Jun 4 2026
Access on the @example-org example-org organization
Repository access
example-org/foo
Repository permissions
 Read access to metadata
 Read and Write access to actions, code, and workflows
`
	got := Parse(body, today)
	if got.Owner != "example-org" {
		t.Errorf("Owner = %q, want example-org", got.Owner)
	}
	wantPerms := []string{
		"metadata: read",
		"actions: write",
		"code: write",
		"workflows: write",
	}
	if !reflect.DeepEqual(got.Perms, wantPerms) {
		t.Errorf("Perms = %v, want %v", got.Perms, wantPerms)
	}
}

func TestRepoLineGate(t *testing.T) {
	// The "All repositories" summary line must NOT be captured as a repo;
	// only owner/repo-shaped lines count.
	body := `Token

No description

Repository access
All repositories
example/foo
example/bar.dot
User permissions
`
	got := Parse(body, today)
	want := []string{"example/foo", "example/bar.dot"}
	if !reflect.DeepEqual(got.Repos, want) {
		t.Fatalf("Repos: got %v want %v", got.Repos, want)
	}
}
