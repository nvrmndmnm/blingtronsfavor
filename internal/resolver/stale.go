package resolver

import (
	"context"
	"fmt"
	"time"

	"github.com/nvrmndmnm/blingtronsfavor/internal/github"
	"github.com/nvrmndmnm/blingtronsfavor/internal/gitops"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
	"github.com/nvrmndmnm/blingtronsfavor/internal/toc"
)

// trackingRelease pins an addon to the release strategy (the recorded form of
// "bfctl add --strategy release"): the staleness detector never fires.
const trackingRelease = "release"

// trackingStable is the default tracking value: follow stable releases.
const trackingStable = "stable"

// StaleDecision is the outcome of the staleness check (M4): whether the
// repository should switch to the source-pkgmeta strategy, which ref the
// build would use and why. Reason is precise enough for "bfctl sync
// --explain".
type StaleDecision struct {
	Trigger bool   // switch the Plan to source-pkgmeta
	Ref     string // git ref a source build would use: newest tag or branch HEAD
	Reason  string // precise reason, shown by --explain
}

// refTime is one repository ref with the commit date behind it.
type refTime struct {
	name string
	when time.Time
}

// CheckStale runs the staleness detector for one repository: it decides
// whether the release strategy can serve the addon or whether a
// source-pkgmeta build at the newest tag / branch HEAD is the better plan.
//
// It triggers iff (plan §ISSUE-08):
//
//	a. the repository has no GitHub Releases at all;
//	b. the latest release lacks an asset for the detected flavor while an
//	   older release serves it (the author bumped the flavor's .toc but cut
//	   no new zip);
//	c. the newest tag or branch HEAD is more than Staleness past the latest
//	   release and the delta touches the target .toc — verified through the
//	   compare API. A compare payload that is unavailable or truncated by
//	   GitHub's MaxCompareFiles window triggers conservatively with
//	   "toc-delta unverifiable".
//
// It never triggers for addons pinned to the release strategy
// (Request.PinnedStrategy, Tracking "release") or pinned to an exact tag.
// Missing timestamps never trigger: without a proven delta there is no
// staleness to act on.
//
// releases is the already-fetched release list (from Resolve), so the check
// adds no extra call for the release anchor. Ref gathering is ETag-cached
// via the GitHub client, with a git ls-remote fallback for refs.
func (r *Resolver) CheckStale(ctx context.Context, req Request, releases []github.Release) (StaleDecision, error) {
	if label := strategyPin(req); label != "" {
		return StaleDecision{Reason: label}, nil
	}
	tags, tagErr := r.tagTimes(ctx, req)
	head, _ := r.headTime(ctx, req)
	work, workName := newestWork(tags, head)
	if label := tagPin(req, tags, tagErr != nil); label != "" {
		return StaleDecision{Reason: label}, nil
	}

	anchor := latestRelease(releases) // newest non-draft release, any channel
	if anchor == nil {
		// Trigger (a): nothing was ever published.
		return StaleDecision{
			Trigger: true,
			Ref:     workRef(workName, req),
			Reason:  "no releases",
		}, nil
	}
	if d, ok := r.flavorUnserved(ctx, req, releases, workName); ok {
		return d, nil
	}

	stale := req.staleness()
	workKnown := workName != "" && !work.when.IsZero()
	delta := work.when.Sub(anchor.PublishedAt)
	switch {
	case !workKnown:
		return StaleDecision{Reason: "staleness unverifiable"}, nil
	case delta > stale:
		// Trigger (c): the delta must touch the target .toc.
		days := int(delta.Hours() / 24)
		touched, verifiable := r.tocTouched(ctx, req, anchor.TagName, workName)
		switch {
		case touched:
			return StaleDecision{
				Trigger: true,
				Ref:     workName,
				Reason:  fmt.Sprintf("%s moved %d days past release %s and touches the %s toc", workName, days, anchor.TagName, req.Flavor),
			}, nil
		case !verifiable:
			return StaleDecision{Trigger: true, Ref: workName, Reason: "toc-delta unverifiable"}, nil
		default:
			return StaleDecision{Reason: fmt.Sprintf("toc untouched since release %s", anchor.TagName)}, nil
		}
	case delta > 0:
		return StaleDecision{
			Reason: fmt.Sprintf("%s is %d days past release %s (within %d)", workName, int(delta.Hours()/24), anchor.TagName, int(stale.Hours()/24)),
		}, nil
	}
	if anchor.Prerelease {
		return StaleDecision{Reason: fmt.Sprintf("pre-release %s covers recent work", anchor.TagName)}, nil
	}
	return StaleDecision{Reason: fmt.Sprintf("release %s is current", anchor.TagName)}, nil
}

// flavorUnserved implements trigger (b): the latest release lacks an asset
// for the detected flavor even though an older release serves it — the
// author bumped the flavor's .toc and cut zips, but not for this flavor, so
// the user would silently stay on a stale package. Two cases are left to
// other paths on purpose: a pre-release never invalidates flavor coverage
// (mirroring select.go, the stable package stays installable), and when no
// release at all serves the flavor Resolve's plain source-build fallback
// already emits the same plan with its "source build required" reason. ok is
// false when the trigger does not apply.
func (r *Resolver) flavorUnserved(ctx context.Context, req Request, releases []github.Release, workName string) (StaleDecision, bool) {
	latest := latestRelease(releases)
	if latest == nil || latest.Prerelease {
		return StaleDecision{}, false
	}
	if r.bestAsset(ctx, *latest, req.Flavor) != nil {
		return StaleDecision{}, false
	}
	for i := range releases {
		rel := &releases[i]
		if rel.Draft || rel == latest {
			continue
		}
		if r.bestAsset(ctx, *rel, req.Flavor) != nil {
			return StaleDecision{
				Trigger: true,
				Ref:     workRef(workName, req),
				Reason:  fmt.Sprintf("latest release %s has no asset for flavor %s", latest.TagName, req.Flavor),
			}, true
		}
	}
	return StaleDecision{}, false
}

// strategyPin reports the pin label that silences the staleness detector for
// strategy-pinned addons, or "" when the addon is not pinned.
func strategyPin(req Request) string {
	if req.PinnedStrategy == state.StrategySourcePkgmeta {
		return "pinned to strategy " + string(state.StrategySourcePkgmeta)
	}
	if req.PinnedStrategy == state.StrategyRelease || req.Tracking == trackingRelease {
		return "pinned to strategy " + string(state.StrategyRelease)
	}
	return ""
}

// tagPin reports the pin label for addons pinned to an exact tag, or "" when
// the addon tracks "stable", a branch, or a release pin. When the tag list
// could not be fetched at all, an explicit non-branch tracking value is
// treated as a pin: silencing the detector is the safe side of a pin.
func tagPin(req Request, tags []refTime, tagsFailed bool) string {
	switch req.Tracking {
	case "", trackingStable, trackingRelease:
		return ""
	}
	for _, t := range tags {
		if t.name == req.Tracking {
			return "pinned to tag " + t.name
		}
	}
	if tagsFailed {
		return "pinned to ref " + req.Tracking
	}
	return ""
}

// latestRelease returns the newest non-draft release, pre-releases included:
// the last time the author published anything, the anchor the staleness
// delta is measured against.
func latestRelease(releases []github.Release) *github.Release {
	var best *github.Release
	for i := range releases {
		rel := &releases[i]
		if rel.Draft {
			continue
		}
		if best == nil || newer(*rel, *best) {
			best = rel
		}
	}
	return best
}

// newestWork picks the newer of the newest tag and the branch HEAD — the ref
// carrying the work that landed after the last release.
func newestWork(tags []refTime, head refTime) (refTime, string) {
	best := head
	for _, t := range tags {
		if t.when.After(best.when) {
			best = t
		}
	}
	if best.name == "" || best.when.IsZero() {
		return refTime{}, ""
	}
	return best, best.name
}

// workRef renders the ref a source build at the newest work would use:
// the newest tag or branch HEAD, falling back to the tracked or default
// branch when no work timestamp is known.
func workRef(workName string, req Request) string {
	if workName != "" {
		return workName
	}
	if req.Tracking != "" && req.Tracking != trackingStable && req.Tracking != trackingRelease {
		return req.Tracking
	}
	if req.DefaultBranch != "" {
		return req.DefaultBranch
	}
	return "main"
}

// tocTouched verifies via the compare API whether the base...head delta
// touched the target .toc. verifiable is false when the payload is
// unavailable or truncated by GitHub's file window: the caller then triggers
// conservatively.
func (r *Resolver) tocTouched(ctx context.Context, req Request, base, head string) (touched, verifiable bool) {
	if base == "" || head == "" {
		return false, false
	}
	cmp, err := r.GH.Compare(ctx, req.Owner, req.Repo, base, head)
	if err != nil {
		return false, false
	}
	for _, f := range cmp.Files {
		if toc.TouchesFlavor(f.Filename, req.Flavor) {
			return true, true
		}
	}
	return false, len(cmp.Files) < github.MaxCompareFiles
}

// tagTimes returns the repository's tags with their commit dates. The tag
// list comes from the ETag-cached tags API with a git ls-remote fallback for
// the refs themselves; commit dates come from the commit API. Tags whose
// commit date cannot be fetched keep their name (for pin detection) with a
// zero date.
func (r *Resolver) tagTimes(ctx context.Context, req Request) ([]refTime, error) {
	tags, err := r.GH.Tags(ctx, req.Owner, req.Repo)
	if err != nil {
		lsTags, _, lsErr := gitops.LsRemote(ctx, gitURL(req))
		if lsErr != nil {
			return nil, err
		}
		tags = make([]github.Tag, 0, len(lsTags))
		for name, sha := range lsTags {
			tags = append(tags, github.Tag{Name: name, Commit: github.TagCommit{SHA: sha}})
		}
	}
	seen := make(map[string]time.Time)
	var out []refTime
	for _, t := range tags {
		if len(out) >= 10 {
			break
		}
		out = append(out, refTime{name: t.Name})
		if t.Commit.SHA == "" {
			continue
		}
		if when, ok := seen[t.Commit.SHA]; ok {
			out[len(out)-1].when = when
			continue
		}
		c, err := r.GH.HeadCommit(ctx, req.Owner, req.Repo, t.Commit.SHA)
		if err != nil {
			continue
		}
		when := c.Commit.Committer.Date
		seen[t.Commit.SHA] = when
		out[len(out)-1].when = when
	}
	return out, nil
}

// headTime returns the default-branch HEAD with its commit date. The branch
// name comes from the request, then the repository API, then "main"; the
// ref itself falls back to git ls-remote when the commit API is unavailable.
func (r *Resolver) headTime(ctx context.Context, req Request) (refTime, error) {
	branch := req.DefaultBranch
	if branch == "" {
		if repo, err := r.GH.Repository(ctx, req.Owner, req.Repo); err == nil && repo.DefaultBranch != "" {
			branch = repo.DefaultBranch
		}
	}
	if branch == "" {
		branch = "main"
	}
	c, err := r.GH.HeadCommit(ctx, req.Owner, req.Repo, branch)
	if err != nil {
		_, heads, lsErr := gitops.LsRemote(ctx, gitURL(req))
		if lsErr != nil {
			return refTime{}, err
		}
		sha, ok := heads[branch]
		if !ok {
			return refTime{}, err
		}
		c, err = r.GH.HeadCommit(ctx, req.Owner, req.Repo, sha)
		if err != nil {
			return refTime{}, err
		}
	}
	return refTime{name: branch, when: c.Commit.Committer.Date}, nil
}

// gitURL renders the clone URL a git ls-remote fallback fetches refs from.
func gitURL(req Request) string {
	return RepoRef{Owner: req.Owner, Repo: req.Repo}.URL() + ".git"
}

// staleness returns the configured staleness window, defaulting to
// state.DefaultStalenessDays.
func (r Request) staleness() time.Duration {
	if r.Staleness > 0 {
		return r.Staleness
	}
	return time.Duration(state.DefaultStalenessDays) * 24 * time.Hour
}
