package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/gitops"
	"github.com/itp5b5/blessforge/internal/packager"
	"github.com/itp5b5/blessforge/internal/state"
)

// GitDirect builds addons straight from an arbitrary git remote — GitHub,
// GitLab, Codeberg or anything else git can clone. Check lists the remote's
// refs and reports the newest version-looking tag (or the tracking branch
// HEAD when the repository is untagged); Fetch runs the full packager
// pipeline — shallow clone into the shared working-copy cache, .pkgmeta
// externals, keyword substitution, debug stripping and the TOC flavor split
// — so the installed tree matches what a release build would produce.
type GitDirect struct {
	// Flavor is the client flavor the packager's TOC split selects for.
	Flavor client.Flavor

	// Interface is the client interface version written into the staged
	// .toc files (required by the packager).
	Interface int
}

// Check resolves the remote's newest version: the highest stable version
// tag when the repository has any, otherwise the tracking branch ("main",
// "master", then any) at its HEAD commit.
func (g *GitDirect) Check(ctx context.Context, ref state.SourceRef) (Upstream, error) {
	if ref.URL == "" {
		return Upstream{}, errors.New("git: source carries no repository URL")
	}
	tags, heads, err := gitops.LsRemote(ctx, ref.URL)
	if err != nil {
		return Upstream{}, err
	}
	up := Upstream{
		Changelog: strings.TrimSuffix(ref.URL, ".git"),
		Strategy:  state.StrategyDirect,
		URL:       ref.URL,
	}
	if tag, ok := newestTag(tags); ok {
		up.Version, up.Ref = tag, tag
		return up, nil
	}
	branch, sha := pickHead(heads)
	if branch == "" {
		return Upstream{}, fmt.Errorf("git %s: remote advertises no tags and no branches", ref.URL)
	}
	up.Version, up.Ref = shortSHA(sha), branch
	return up, nil
}

// Fetch builds ref's current version through the packager pipeline into
// dest as top-level addon folders.
func (g *GitDirect) Fetch(ctx context.Context, ref state.SourceRef, dest string) error {
	if ref.URL == "" {
		return errors.New("git: source carries no repository URL")
	}
	up, err := g.Check(ctx, ref)
	if err != nil {
		return err
	}
	if g.Interface == 0 {
		return errors.New("git: the detected client's interface version is required to build from source")
	}
	owner, repo := repoIdentity(ref.URL)
	_, err = packager.Build(ctx, packager.Spec{
		Owner: owner,
		Repo:  repo,
		URL:   ref.URL,
		Ref:   up.Ref,
	}, packager.Options{
		OutDir:    dest,
		Flavor:    g.Flavor,
		Interface: g.Interface,
	})
	return err
}

// newestTag picks the highest stable version tag ("1.2.3", "v1.2.3",
// "1.2.3.4"): prerelease tags (anything carrying a "-") are only considered
// when no stable tag exists. ok is false when no tag parses as a version.
func newestTag(tags map[string]string) (string, bool) {
	type scored struct {
		name string
		seg  []int
	}
	var stable, pre []scored
	for name := range tags {
		seg, ok := parseTag(name)
		if !ok {
			continue
		}
		if strings.Contains(name, "-") {
			pre = append(pre, scored{name, seg})
		} else {
			stable = append(stable, scored{name, seg})
		}
	}
	pool := stable
	if len(pool) == 0 {
		pool = pre
	}
	if len(pool) == 0 {
		return "", false
	}
	sort.Slice(pool, func(i, j int) bool { return compareVersions(pool[i].seg, pool[j].seg) > 0 })
	return pool[0].name, true
}

// parseTag parses a version-ish tag name into its numeric segments. Tags
// that do not start with a number (or "v" plus a number) are not versions.
func parseTag(name string) ([]int, bool) {
	s := strings.TrimPrefix(name, "v")
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
	if len(parts) == 0 {
		return nil, false
	}
	var seg []int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			// A trailing non-numeric suffix ("1.2.3-beta" already split,
			// "1.2.3final") is tolerated only after at least one number.
			if i == 0 {
				return nil, false
			}
			break
		}
		seg = append(seg, n)
	}
	return seg, len(seg) > 0
}

// compareVersions compares two numeric version segment lists, treating a
// missing trailing segment as zero.
func compareVersions(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		av, bv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			return av - bv
		}
	}
	return 0
}

// pickHead chooses the branch an untagged repository tracks: "main", then
// "master", then any other head in alphabetical order.
func pickHead(heads map[string]string) (branch, sha string) {
	for _, name := range []string{"main", "master"} {
		if sha, ok := heads[name]; ok {
			return name, sha
		}
	}
	names := make([]string, 0, len(heads))
	for name := range heads {
		names = append(names, name)
	}
	if len(names) == 0 {
		return "", ""
	}
	sort.Strings(names)
	return names[0], heads[names[0]]
}

// shortSHA renders a commit SHA's short form for version comparisons.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// repoIdentity derives a cache identity (owner, repo) from a git remote URL:
// the last two path segments, e.g. "https://gitlab.com/group/addons/foo.git"
// -> ("addons", "foo"). Nested group names only affect cache directory
// naming, never resolution.
func repoIdentity(rawURL string) (owner, repo string) {
	trimmed := strings.TrimSuffix(strings.TrimRight(rawURL, "/"), ".git")
	segs := strings.FieldsFunc(trimmed, func(r rune) bool { return r == '/' || r == ':' })
	if len(segs) == 0 {
		return "local", "repo"
	}
	repo = segs[len(segs)-1]
	if len(segs) >= 2 {
		owner = segs[len(segs)-2]
	} else {
		owner = "local"
	}
	return owner, repo
}
