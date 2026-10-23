package cli

import (
	"testing"

	"github.com/itp5b5/blessforge/internal/adopt"
	"github.com/itp5b5/blessforge/internal/state"
)

// TestBindFlags checks the repeatable --bind folder=owner/repo override.
func TestBindFlags(t *testing.T) {
	b := bindFlags{}
	if err := b.Set("MysteryAddon=acme/mystery"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := b.Set("Other=foo/bar"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if b["MysteryAddon"] != "acme/mystery" || b["Other"] != "foo/bar" {
		t.Errorf("binds = %v", b)
	}
	if got := b.String(); got != "MysteryAddon=acme/mystery,Other=foo/bar" {
		t.Errorf("String = %q", got)
	}
	for _, bad := range []string{"", "folder=", "=repo", "no-separator"} {
		if err := b.Set(bad); err == nil {
			t.Errorf("Set(%q) accepted, want an error", bad)
		}
	}
}

// TestIsSourceGitURL checks source-git detection: git remotes route to the
// git-direct provider, "owner/repo" references stay with the resolver.
func TestIsSourceGitURL(t *testing.T) {
	yes := []string{
		"https://gitlab.com/group/addon.git",
		"https://codeberg.org/group/addon",
		"git://example.com/addon.git",
	}
	for _, raw := range yes {
		if !isSourceGitURL(raw) {
			t.Errorf("isSourceGitURL(%q) = false, want true", raw)
		}
	}
	no := []string{"owner/repo", "not a slug", "ftp://example.com/x"}
	for _, raw := range no {
		if isSourceGitURL(raw) {
			t.Errorf("isSourceGitURL(%q) = true, want false", raw)
		}
	}
}

// TestGitRepoName checks addon-id derivation for git URLs.
func TestGitRepoName(t *testing.T) {
	cases := map[string]string{
		"https://gitlab.com/group/addon.git": "addon",
		"https://codeberg.org/group/addon":   "addon",
		"git://example.com/deep/path/repo":   "repo",
	}
	for raw, want := range cases {
		if got := gitRepoName(raw); got != want {
			t.Errorf("gitRepoName(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestSourceLabel checks the table rendering of proposed sources.
func TestSourceLabel(t *testing.T) {
	cases := map[state.SourceRef]string{
		{Kind: state.SourceManual}:                    "-",
		{Kind: state.SourceGitHub, Slug: "a/b"}:       "github:a/b",
		{Kind: state.SourceGit, Slug: "gitlab.com/a"}: "git:gitlab.com/a",
		{Kind: state.SourceWoWI, Slug: "24910"}:       "wowi:24910",
	}
	for src, want := range cases {
		if got := sourceLabel(src); got != want {
			t.Errorf("sourceLabel(%+v) = %q, want %q", src, got, want)
		}
	}
}

// TestAdoptCount checks that only bind and manual proposals are counted for
// the persistence summary.
func TestAdoptCount(t *testing.T) {
	rep := &adopt.Report{Groups: []*adopt.Group{
		{Action: adopt.ActionBind},
		{Action: adopt.ActionManual},
		{Action: adopt.ActionSkip},
	}}
	if got := adoptCount(rep); got != 2 {
		t.Errorf("adoptCount = %d, want 2", got)
	}
	if plural(1, "y", "ies") != "y" || plural(2, "y", "ies") != "ies" {
		t.Error("plural() picked the wrong form")
	}
}
