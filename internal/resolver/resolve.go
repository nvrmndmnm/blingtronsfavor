// Package resolver turns a GitHub repository reference into an install Plan:
// which release asset to install for a client flavor, and why. The release
// strategy (M3) selects a packaged release asset — via the release.json
// metadata BigWigsMods/packager attaches first, zip-name heuristics second.
// The source-pkgmeta strategy (M4) builds the repository at the planned ref
// through the packager pipeline (internal/packager) — it is planned here and
// executed by the service layer when no release asset can serve the flavor or
// the staleness detector finds the releases stale.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/github"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
)

// downloadTimeout bounds a single release.json or release asset download.
const downloadTimeout = 5 * time.Minute

// Plan is one resolution result: what to install for an addon source and why.
// Strategy "release" downloads URL; Strategy "source-pkgmeta" builds URL at
// Ref through the packager pipeline. Version is the version to record for
// either — the release tag, or a source build's tag/commit stamp — so callers
// can detect whether anything changed before installing.
type Plan struct {
	Strategy state.Strategy // "release" | "source-pkgmeta"
	Version  string         // tag or version
	URL      string         // asset download URL or git URL to build
	Ref      string         // git ref to build (source strategy), release tag otherwise
	Reason   string         // human-readable, shown by --explain
}

// Request is one resolution request: a GitHub repository and the client
// flavor to select an asset for.
type Request struct {
	Owner, Repo   string
	Flavor        client.Flavor
	Tracking      string // "stable" (default), "release" (strategy pin), a pinned tag, or the branch a source build would track
	DefaultBranch string // repository default branch, used as the source Ref fallback

	// Staleness is the staleness window of the M4 detector; zero selects
	// state.DefaultStalenessDays.
	Staleness time.Duration

	// PinnedStrategy is the config strategy override for the addon, if
	// any. A pin wins over every staleness heuristic: the detector never
	// fires for a pinned addon.
	PinnedStrategy state.Strategy
}

// RepoRef is a parsed GitHub repository reference.
type RepoRef struct {
	Owner string
	Repo  string
}

// Slug renders the canonical "owner/repo" form.
func (r RepoRef) Slug() string { return r.Owner + "/" + r.Repo }

// URL renders the canonical repository web URL.
func (r RepoRef) URL() string { return "https://github.com/" + r.Slug() }

// ParseRepo accepts the repository spellings "bfctl add" takes: "owner/repo",
// "https://github.com/owner/repo(.git)", "git@github.com:owner/repo.git" and
// "ssh://git@github.com/owner/repo.git". A github.com deep link (…/releases)
// keeps its owner/repo head.
func ParseRepo(raw string) (RepoRef, error) {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "/")
	var owner, repo string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return RepoRef{}, fmt.Errorf("parse repository %q: %w", raw, err)
		}
		switch strings.ToLower(u.Hostname()) {
		case "github.com", "www.github.com":
		default:
			return RepoRef{}, fmt.Errorf("parse repository %q: only github.com repositories are supported", raw)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return RepoRef{}, fmt.Errorf("parse repository %q: want owner/repo or a github.com URL", raw)
		}
		owner, repo = parts[0], parts[1]
	case strings.HasPrefix(raw, "git@"):
		_, path, _ := strings.Cut(raw, ":")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) != 2 {
			return RepoRef{}, fmt.Errorf("parse repository %q: want owner/repo or a github.com URL", raw)
		}
		owner, repo = parts[0], parts[1]
	default:
		parts := strings.Split(strings.Trim(raw, "/"), "/")
		if len(parts) != 2 {
			return RepoRef{}, fmt.Errorf("parse repository %q: want owner/repo or a github.com URL", raw)
		}
		owner, repo = parts[0], parts[1]
	}
	repo = strings.TrimSuffix(repo, ".git")
	if !validRepoName(owner) || !validRepoName(repo) {
		return RepoRef{}, fmt.Errorf("parse repository %q: want owner/repo or a github.com URL", raw)
	}
	return RepoRef{Owner: owner, Repo: repo}, nil
}

// validRepoName reports whether name is a plausible GitHub owner or
// repository name: non-empty and limited to the characters GitHub allows.
func validRepoName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// Resolver resolves GitHub repositories into install Plans. GH performs the
// ETag-cached API calls; HTTP downloads release.json metadata and release
// assets. Both are safe for concurrent use.
type Resolver struct {
	GH   *github.Client
	HTTP *http.Client
}

// httpClient returns the download client, defaulting to a plain client with
// a bounded per-request timeout.
func (r *Resolver) httpClient() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: downloadTimeout}
}

// Resolve runs the resolution order of the plan (spec §2) for one repository
// and flavor: the staleness detector first (a stale repository switches to
// a source-pkgmeta build at the newest tag / branch HEAD), then release.json
// metadata, zip-name heuristics second, and a source-pkgmeta Plan (with a
// "source build required" reason) when no release asset can serve the flavor.
// Only API and download failures are errors; a repository that needs a source
// build resolves to a well-formed Plan with the ref and version stamp its
// build produces. A failure inside the staleness check
// degrades to plain release resolution — the detector must never break
// installs — but a configured strategy pin is always honored.
func (r *Resolver) Resolve(ctx context.Context, req Request) (Plan, error) {
	if req.Owner == "" || req.Repo == "" {
		return Plan{}, errors.New("resolve: owner and repo are required")
	}
	releases, err := r.GH.Releases(ctx, req.Owner, req.Repo)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve %s/%s: %w", req.Owner, req.Repo, err)
	}
	if req.PinnedStrategy == state.StrategySourcePkgmeta {
		return r.sourcePlan(ctx, req, r.sourceRef(ctx, req), "pinned to strategy "+string(state.StrategySourcePkgmeta)), nil
	}
	if d, err := r.CheckStale(ctx, req, releases); err == nil && d.Trigger {
		return r.sourcePlan(ctx, req, d.Ref, d.Reason), nil
	}
	if best := r.pick(ctx, releases, req); best != nil {
		return best.plan(), nil
	}
	return r.sourcePlan(ctx, req, r.sourceRef(ctx, req),
		fmt.Sprintf("source build required: no release asset matches flavor %s", req.Flavor)), nil
}

// sourcePlan assembles a source-pkgmeta plan at ref: the git URL to build,
// the ref, its version stamp and the reason shown by --explain.
func (r *Resolver) sourcePlan(ctx context.Context, req Request, ref, reason string) Plan {
	return Plan{
		Strategy: state.StrategySourcePkgmeta,
		URL:      RepoRef{Owner: req.Owner, Repo: req.Repo}.URL() + ".git",
		Ref:      ref,
		Version:  r.sourceVersion(ctx, req, ref),
		Reason:   reason,
	}
}

// sourceRef picks the git ref a source build tracks when the plan is not
// anchored to a concrete tag: the explicitly tracked branch, the
// repository's default branch, or "main" as the last resort.
func (r *Resolver) sourceRef(ctx context.Context, req Request) string {
	if t := req.Tracking; t != "" && t != trackingStable && t != trackingRelease {
		return t
	}
	if req.DefaultBranch != "" {
		return req.DefaultBranch
	}
	if repo, err := r.GH.Repository(ctx, req.Owner, req.Repo); err == nil && repo.DefaultBranch != "" {
		return repo.DefaultBranch
	}
	return "main"
}

// sourceVersion stamps a source-pkgmeta plan with the version the build
// produces, so callers can detect updates without rebuilding: the ref itself
// when it is a tag (tags are immutable), otherwise the short SHA of the
// commit the ref points at (a branch ref moves when the branch does). A
// lookup failure leaves Version empty: callers then rebuild rather than
// silently consider the addon up to date.
func (r *Resolver) sourceVersion(ctx context.Context, req Request, ref string) string {
	if ref == "" {
		return ""
	}
	if tags, err := r.GH.Tags(ctx, req.Owner, req.Repo); err == nil {
		for _, tag := range tags {
			if tag.Name == ref {
				return ref
			}
		}
	}
	commit, err := r.GH.HeadCommit(ctx, req.Owner, req.Repo, ref)
	if err != nil || commit.SHA == "" {
		return ""
	}
	if len(commit.SHA) > 8 {
		return commit.SHA[:8]
	}
	return commit.SHA
}
