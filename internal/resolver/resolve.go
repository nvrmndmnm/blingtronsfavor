// Package resolver turns a GitHub repository reference into an install Plan:
// which release asset to install for a client flavor, and why. The release
// strategy (M3) selects a packaged release asset — via the release.json
// metadata BigWigsMods/packager attaches first, zip-name heuristics second.
// The source-pkgmeta strategy (M4) is planned here only: a repository whose
// Plan needs a source build surfaces as "source build required" so callers can
// skip it cleanly until source builds land.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/github"
	"github.com/itp5b5/blessforge/internal/state"
)

// downloadTimeout bounds a single release.json or release asset download.
const downloadTimeout = 5 * time.Minute

// Plan is one resolution result: what to install for an addon source and why.
// It is forward-compatible with M4: Strategy "release" downloads URL;
// Strategy "source-pkgmeta" builds URL at Ref.
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
	Tracking      string // "stable" (default) or the branch a source build would track
	DefaultBranch string // repository default branch, used as the source Ref fallback
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
// and flavor: release.json metadata first, zip-name heuristics second, and a
// source-pkgmeta Plan (with a "source build required" reason) when no release
// asset can serve the flavor. Only API and download failures are errors; a
// repository that needs a source build resolves to a well-formed Plan.
func (r *Resolver) Resolve(ctx context.Context, req Request) (Plan, error) {
	if req.Owner == "" || req.Repo == "" {
		return Plan{}, errors.New("resolve: owner and repo are required")
	}
	releases, err := r.GH.Releases(ctx, req.Owner, req.Repo)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve %s/%s: %w", req.Owner, req.Repo, err)
	}
	if best := r.pick(ctx, releases, req); best != nil {
		return best.plan(), nil
	}
	ref := RepoRef{Owner: req.Owner, Repo: req.Repo}
	branch := req.Tracking
	if branch == "" || branch == "stable" {
		branch = req.DefaultBranch
	}
	if branch == "" {
		branch = "main"
	}
	return Plan{
		Strategy: state.StrategySourcePkgmeta,
		URL:      ref.URL() + ".git",
		Ref:      branch,
		Reason:   fmt.Sprintf("source build required: no release asset matches flavor %s", req.Flavor),
	}, nil
}
