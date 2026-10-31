package service

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/provider"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/state"
)

// AddRef is one parsed add reference: the source kind the reference
// installs through and the kind-specific identity. ParseAddRef classifies
// the spellings "bfctl add" accepts — the same parsers the TUI's add input
// validates against live.
type AddRef struct {
	Kind state.SourceKind // SourceGitHub, SourceWago, SourceCfwidget or SourceGit
	Repo resolver.RepoRef // Kind == SourceGitHub
	Key  string           // Kind == SourceWago / SourceCfwidget: the provider key
	URL  string           // Kind == SourceGit: the git remote
}

// ID renders the manifest key the addon is recorded under: the repository
// name, the provider key or the git repository name.
func (r AddRef) ID() string {
	switch r.Kind {
	case state.SourceGitHub:
		return r.Repo.Repo
	case state.SourceWago, state.SourceCfwidget:
		return r.Key
	default:
		return GitRepoName(r.URL)
	}
}

// Name labels the reference in errors and progress messages the way
// "bfctl add" names its source: the slug, "wago:<key>", "cfwidget:<key>"
// or the git remote itself.
func (r AddRef) Name() string {
	switch r.Kind {
	case state.SourceGitHub:
		return r.Repo.Slug()
	case state.SourceWago:
		return "wago:" + r.Key
	case state.SourceCfwidget:
		return "cfwidget:" + r.Key
	default:
		return r.URL
	}
}

// ParseAddRef classifies one add reference, in the order "bfctl add"
// checks the shapes: Wago Addons references (the addon page URL or
// "wago:<key>"), CurseForge references (the addon page URL or
// "cfwidget:<key>"), GitHub repository references ("owner/repo" or a
// github.com URL) and any other git remote URL (installed git-direct).
// Everything else is rejected with an error naming the accepted shapes —
// the TUI shows it inline as the user types.
func ParseAddRef(raw string) (AddRef, error) {
	raw = strings.TrimSpace(raw)
	if key, ok := ParseWagoRef(raw); ok {
		return AddRef{Kind: state.SourceWago, Key: key}, nil
	}
	if key, ok := ParseCfRef(raw); ok {
		return AddRef{Kind: state.SourceCfwidget, Key: key}, nil
	}
	if repo, err := resolver.ParseRepo(raw); err == nil {
		return AddRef{Kind: state.SourceGitHub, Repo: repo}, nil
	}
	if IsSourceGitURL(raw) {
		return AddRef{Kind: state.SourceGit, URL: raw}, nil
	}
	if _, ok := strings.CutPrefix(raw, "wago:"); ok {
		return AddRef{}, fmt.Errorf("parse reference %q: wago: needs an addon key (the page slug or X-Wago-ID)", raw)
	}
	if _, ok := strings.CutPrefix(raw, "cfwidget:"); ok {
		return AddRef{}, fmt.Errorf("parse reference %q: cfwidget: needs an addon key (the project slug or ID)", raw)
	}
	return AddRef{}, fmt.Errorf("parse reference %q: want owner/repo, a github.com, addons.wago.io or curseforge.com addon URL, wago:<key>, cfwidget:<key> or a git remote URL", raw)
}

// ParseWagoRef recognizes Wago Addons references and returns the provider
// key: the addon page URL "https://addons.wago.io/addons/<key>" (query
// strings, fragments and deep links like "/versions" are ignored) and the
// direct short form "wago:<key>". The key is the page slug, the TOC
// "## X-Wago-ID" short ID or the legacy numeric ID — the Wago provider
// resolves all three.
func ParseWagoRef(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if key, ok := strings.CutPrefix(raw, "wago:"); ok {
		key = strings.Trim(strings.TrimSpace(key), "/")
		return key, key != ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Hostname()) {
	case "addons.wago.io", "www.addons.wago.io":
	default:
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || !strings.EqualFold(parts[0], "addons") {
		return "", false
	}
	return parts[1], parts[1] != ""
}

// ParseCfRef recognizes CurseForge references and returns the cfwidget
// mirror key: the addon page URL "https://www.curseforge.com/wow/addons/<slug>"
// (query strings, fragments and deep links like "/files/123" are ignored)
// and the direct short form "cfwidget:<key>". The key is the project slug
// or the numeric project ID (the TOC "## X-Curse-Project-ID" value) — the
// cfwidget provider resolves both. Non-WoW CurseForge games are rejected.
func ParseCfRef(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if key, ok := strings.CutPrefix(raw, "cfwidget:"); ok {
		key = strings.Trim(strings.TrimSpace(key), "/")
		return key, key != ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Hostname()) {
	case "curseforge.com", "www.curseforge.com":
	default:
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || !strings.EqualFold(parts[0], "wow") || !strings.EqualFold(parts[1], "addons") {
		return "", false
	}
	return parts[2], parts[2] != ""
}

// IsSourceGitURL reports whether raw is a git remote URL for any forge or
// host, as opposed to an "owner/repo" GitHub reference (which the resolver
// handles).
func IsSourceGitURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "http", "https", "git", "ssh":
		return u.Host != ""
	default:
		return false
	}
}

// GitRepoName renders the repository name of a git URL as the addon id,
// e.g. "https://gitlab.com/group/addon.git" -> "addon".
func GitRepoName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	name := filepath.Base(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"))
	if name == "." || name == "/" || name == "" {
		return raw
	}
	return name
}

// AddOptions configures one add run.
type AddOptions struct {
	// Tracking is the GitHub tracking mode: "stable" (the default when
	// empty), the "release" pin or a branch to track. Provider-backed
	// sources always install their stable upstream.
	Tracking string
	// Force takes over addon directories owned by another addon.
	Force bool
	// OnProgress receives the unit's progress events — the same
	// ProgressEvent stream Sync reports through (see ProgressFunc for the
	// delivery contract).
	OnProgress ProgressFunc
	// OnPlan, when set, receives the resolved GitHub plan before the
	// install starts ("bfctl add --explain" prints its reason).
	OnPlan func(resolver.Plan)
}

// AddResult is what one successful add installed.
type AddResult struct {
	ID       string           // manifest key the addon was recorded under
	Kind     state.SourceKind // source kind it installed through
	Version  string           // installed version (or the built ref)
	Strategy state.Strategy   // install strategy recorded
}

// Add installs one parsed add reference for a client flavor — the add flow
// behind both "bfctl add" and the TUI's add input. A GitHub reference
// resolves its install plan and installs it (a release asset, or a source
// build composed through the packager pipeline); provider-backed
// references (wago, cfwidget, git-direct) resolve and fetch through their
// provider. Every step reports through opts.OnProgress with a single-unit
// workload counter, and a failure or cancellation rolls back through the
// atomic installer.
func Add(ctx context.Context, ref AddRef, flavor client.Flavor, opts AddOptions) (AddResult, error) {
	switch ref.Kind {
	case state.SourceGitHub:
		return addGitHub(ctx, ref, flavor, opts)
	case state.SourceWago, state.SourceCfwidget, state.SourceGit:
		return addProvider(ctx, ref, flavor, opts)
	default:
		return AddResult{}, fmt.Errorf("add %s: unsupported source kind %q", ref.Name(), ref.Kind)
	}
}

// addGitHub resolves the install plan for a GitHub repository reference
// and installs it, recording the addon bound to the GitHub source.
func addGitHub(ctx context.Context, ref AddRef, flavor client.Flavor, opts AddOptions) (AddResult, error) {
	res, err := newResolver()
	if err != nil {
		return AddResult{}, fmt.Errorf("create github client: %w", err)
	}
	tracking := opts.Tracking
	if tracking == "" {
		tracking = "stable"
	}
	plan, err := PlanAdd(ctx, res, flavor, ref.Repo, tracking, opts.OnProgress)
	if err != nil {
		return AddResult{}, err
	}
	if opts.OnPlan != nil {
		opts.OnPlan(plan)
	}
	rec := state.AddonState{
		ID:          ref.ID(),
		DisplayName: ref.ID(),
		Source:      state.SourceRef{Kind: state.SourceGitHub, URL: ref.Repo.URL(), Slug: ref.Repo.Slug()},
		Strategy:    plan.Strategy,
		Tracking:    tracking,
	}
	if err := InstallPlan(ctx, res, flavor, rec, plan, opts.Force, opts.OnProgress); err != nil {
		return AddResult{}, err
	}
	ver := plan.Version
	if ver == "" {
		ver = plan.Ref
	}
	return AddResult{ID: rec.ID, Kind: state.SourceGitHub, Version: ver, Strategy: plan.Strategy}, nil
}

// addProvider installs a provider-backed reference (wago, cfwidget or
// git-direct), recording the addon bound to that source so sync keeps
// maintaining it through the same provider.
func addProvider(ctx context.Context, ref AddRef, flavor client.Flavor, opts AddOptions) (AddResult, error) {
	iface, err := clientInterface(flavor)
	if err != nil {
		return AddResult{}, err
	}
	prov, err := provider.For(ref.Kind, provider.Options{Flavor: flavor, Interface: iface})
	if err != nil {
		return AddResult{}, err
	}
	var src state.SourceRef
	switch ref.Kind {
	case state.SourceWago:
		src = state.SourceRef{Kind: state.SourceWago, Slug: ref.Key}
	case state.SourceCfwidget:
		src = state.SourceRef{Kind: state.SourceCfwidget, Slug: ref.Key}
	default:
		src = state.SourceRef{Kind: state.SourceGit, URL: ref.URL}
	}
	up, err := InstallProvider(ctx, prov, src, ref.ID(), ref.Name(), flavor, opts.Force, opts.OnProgress)
	if err != nil {
		return AddResult{}, err
	}
	return AddResult{ID: ref.ID(), Kind: ref.Kind, Version: up.Version, Strategy: up.Strategy}, nil
}
