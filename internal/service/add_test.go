package service

import (
	"strings"
	"testing"

	"github.com/itp5b5/blessforge/internal/state"
)

// TestParseWagoRef pins the accepted Wago reference shapes: the addon page
// URL — with query strings, trailing slashes and deep links stripped — and
// the direct "wago:<key>" short form. Anything else stays with the GitHub
// or git-remote paths.
func TestParseWagoRef(t *testing.T) {
	cases := map[string]string{
		"https://addons.wago.io/addons/eqol":                  "eqol",
		"https://addons.wago.io/addons/eqol/":                 "eqol",
		"https://addons.wago.io/addons/eqol?stability=stable": "eqol",
		"https://addons.wago.io/addons/eqol/versions":         "eqol",
		"https://addons.wago.io/addons/eqol/versions#latest":  "eqol",
		"https://www.addons.wago.io/addons/VBNBxKx5":          "VBNBxKx5",
		"HTTPS://ADDONS.WAGO.IO/ADDONS/Eqol":                  "Eqol",
		"wago:eqol":                                           "eqol",
		"wago:VBNBxKx5":                                       "VBNBxKx5",
		"wago:12345":                                          "12345",
		"  wago:eqol  ":                                       "eqol",
	}
	for raw, want := range cases {
		got, ok := ParseWagoRef(raw)
		if !ok || got != want {
			t.Errorf("ParseWagoRef(%q) = %q, %v; want %q, true", raw, got, ok, want)
		}
	}
	rejects := []string{
		"", "wago:", "wago:/", "owner/repo",
		"https://github.com/owner/repo", "https://gitlab.com/group/addon.git",
		"https://example.com/addons/eqol", "https://wago.io/eqol",
		"https://addons.wago.io/", "https://addons.wago.io/addons/",
		"https://addons.wago.io/addon/eqol", "https://addons.wago.io/addons",
	}
	for _, raw := range rejects {
		if got, ok := ParseWagoRef(raw); ok {
			t.Errorf("ParseWagoRef(%q) = %q, true; want a rejection", raw, got)
		}
	}
}

// TestParseCfRef pins the accepted CurseForge reference shapes: the addon
// page URL — with query strings, trailing slashes and deep links stripped —
// and the direct "cfwidget:<key>" short form (slug or numeric project ID).
// Anything else stays with the GitHub or git-remote paths.
func TestParseCfRef(t *testing.T) {
	cases := map[string]string{
		"https://www.curseforge.com/wow/addons/auctionator":               "auctionator",
		"https://www.curseforge.com/wow/addons/auctionator/":              "auctionator",
		"https://www.curseforge.com/wow/addons/auctionator/files":         "auctionator",
		"https://www.curseforge.com/wow/addons/auctionator/files/9202791": "auctionator",
		"https://www.curseforge.com/wow/addons/details?class=warrior":     "details",
		"https://curseforge.com/wow/addons/Details":                       "Details",
		"HTTPS://WWW.CURSEFORGE.COM/WOW/ADDONS/Auctionator":               "Auctionator",
		"cfwidget:61284":       "61284",
		"cfwidget:auctionator": "auctionator",
		"  cfwidget:61284  ":   "61284",
	}
	for raw, want := range cases {
		got, ok := ParseCfRef(raw)
		if !ok || got != want {
			t.Errorf("ParseCfRef(%q) = %q, %v; want %q, true", raw, got, ok, want)
		}
	}
	rejects := []string{
		"", "cfwidget:", "cfwidget:/", "owner/repo",
		"https://github.com/owner/repo", "https://gitlab.com/group/addon.git",
		"https://www.curseforge.com/wow/addons/", "https://www.curseforge.com/wow/addons",
		"https://www.curseforge.com/wow/addon/auctionator",
		"https://www.curseforge.com/minecraft/mc-mods/jei",
		"https://example.com/wow/addons/auctionator",
		"https://addons.wago.io/addons/eqol",
	}
	for _, raw := range rejects {
		if got, ok := ParseCfRef(raw); ok {
			t.Errorf("ParseCfRef(%q) = %q, true; want a rejection", raw, got)
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
		if !IsSourceGitURL(raw) {
			t.Errorf("IsSourceGitURL(%q) = false, want true", raw)
		}
	}
	no := []string{"owner/repo", "not a slug", "ftp://example.com/x"}
	for _, raw := range no {
		if IsSourceGitURL(raw) {
			t.Errorf("IsSourceGitURL(%q) = true, want false", raw)
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
		if got := GitRepoName(raw); got != want {
			t.Errorf("GitRepoName(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestParseAddRef pins the classification the add flow and the TUI's live
// input validation share: every accepted spelling lands in its kind with
// its identity, and anything else is rejected with an error naming the
// accepted shapes.
func TestParseAddRef(t *testing.T) {
	cases := map[string]AddRef{
		"owner/repo":                                    {Kind: state.SourceGitHub},
		"https://github.com/owner/repo.git":             {Kind: state.SourceGitHub},
		"git@github.com:owner/repo.git":                 {Kind: state.SourceGitHub},
		"https://addons.wago.io/addons/eqol":            {Kind: state.SourceWago, Key: "eqol"},
		"wago:eqol":                                     {Kind: state.SourceWago, Key: "eqol"},
		"https://www.curseforge.com/wow/addons/details": {Kind: state.SourceCfwidget, Key: "details"},
		"cfwidget:61284":                                {Kind: state.SourceCfwidget, Key: "61284"},
		"https://gitlab.com/group/addon.git":            {Kind: state.SourceGit, URL: "https://gitlab.com/group/addon.git"},
	}
	for raw, want := range cases {
		got, err := ParseAddRef(raw)
		if err != nil {
			t.Errorf("ParseAddRef(%q) = error %v; want a classification", raw, err)
			continue
		}
		if got.Kind != want.Kind || got.Key != want.Key {
			t.Errorf("ParseAddRef(%q) = %+v; want kind %q key %q", raw, got, want.Kind, want.Key)
		}
	}
	// The GitHub cases carry the parsed repository.
	ref, err := ParseAddRef("owner/repo")
	if err != nil || ref.Repo.Slug() != "owner/repo" {
		t.Errorf("ParseAddRef(\"owner/repo\") repo = %+v, %v; want owner/repo", ref.Repo, err)
	}

	rejects := map[string]string{
		"nope":             "owner/repo",
		"wago:":            "addon key",
		"cfwidget:":        "addon key",
		"ftp://x/y":        "owner/repo",
		"owner/repo/extra": "owner/repo",
	}
	for raw, wantSub := range rejects {
		_, err := ParseAddRef(raw)
		if err == nil {
			t.Errorf("ParseAddRef(%q) accepted; want a rejection", raw)
			continue
		}
		if !strings.Contains(err.Error(), wantSub) {
			t.Errorf("ParseAddRef(%q) error = %v; want it to mention %q", raw, err, wantSub)
		}
	}
}

// TestAddRefIdentity pins the manifest key and the error label one parsed
// reference carries through the add flow.
func TestAddRefIdentity(t *testing.T) {
	cases := map[string][2]string{
		"owner/repo":                         {"repo", "owner/repo"},
		"wago:eqol":                          {"eqol", "wago:eqol"},
		"cfwidget:61284":                     {"61284", "cfwidget:61284"},
		"https://gitlab.com/group/addon.git": {"addon", "https://gitlab.com/group/addon.git"},
	}
	for raw, want := range cases {
		ref, err := ParseAddRef(raw)
		if err != nil {
			t.Fatalf("ParseAddRef(%q) = %v", raw, err)
		}
		if ref.ID() != want[0] || ref.Name() != want[1] {
			t.Errorf("ParseAddRef(%q) identity = (%q, %q); want (%q, %q)", raw, ref.ID(), ref.Name(), want[0], want[1])
		}
	}
}
