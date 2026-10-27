package resolver

import (
	"context"
	"strconv"
	"strings"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/github"
	"github.com/itp5b5/blessforge/internal/state"
)

// releaseJSONName is the metadata asset BigWigsMods/packager attaches to a
// release next to the packaged zips. Its presence switches asset selection to
// release.json; without it the zip-name heuristics below apply.
const releaseJSONName = "release.json"

// candidate is one release asset that can serve a flavor.
type candidate struct {
	release    github.Release
	asset      github.Asset
	nolib      bool   // asset ships without bundled libraries
	tagged     bool   // asset name carries a flavor tag (vs. an untagged zip)
	viaMeta    bool   // selected via release.json metadata
	flavorName string // flavor evidence: the release.json key or the zip name's tag
}

// plan renders the candidate as a release Plan with a human-readable Reason.
func (c *candidate) plan() Plan {
	libs := "libs bundled"
	if c.nolib {
		libs = "nolib"
	}
	via := "zip name heuristics"
	if c.viaMeta {
		via = "release.json"
	}
	reason := "release " + c.release.TagName + " selected via " + via + " (" + describeFlavor(c) + ", " + libs + ")"
	if c.release.Prerelease {
		reason += ", pre-release (only flavor match)"
	}
	return Plan{
		Strategy: state.StrategyRelease,
		Version:  c.release.TagName,
		URL:      c.asset.BrowserDownloadURL,
		Ref:      c.release.TagName,
		Reason:   reason,
	}
}

// describeFlavor names the flavor evidence behind the selection: the
// release.json flavor key or the zip name's flavor tag, or the untagged-zip
// rule for mainline.
func describeFlavor(c *candidate) string {
	if c.flavorName != "" {
		if c.viaMeta {
			return "flavor " + c.flavorName
		}
		return "flavor tag " + c.flavorName
	}
	return "untagged zip treated as mainline"
}

// pick selects the best release asset for req's flavor, or nil when no
// release can serve it (the caller then plans a source build). Selection
// order (plan §2): release.json metadata first, zip-name heuristics second,
// newest usable release wins — and a pre-release is only allowed when it is
// the only flavor match, so any stable release that serves the flavor beats
// pre-releases regardless of age.
func (r *Resolver) pick(ctx context.Context, releases []github.Release, req Request) *candidate {
	var stable, pre []*candidate
	for _, rel := range releases {
		if rel.Draft {
			continue // drafts are never installable
		}
		c := r.bestAsset(ctx, rel, req.Flavor)
		if c == nil {
			continue
		}
		if rel.Prerelease {
			pre = append(pre, c)
		} else {
			stable = append(stable, c)
		}
	}
	pool := stable
	if len(pool) == 0 {
		pool = pre
	}
	return newest(pool)
}

// bestAsset resolves one release: the asset named by its release.json
// metadata when that serves the flavor, otherwise the best zip-name match.
func (r *Resolver) bestAsset(ctx context.Context, rel github.Release, flavor client.Flavor) *candidate {
	if asset, ok := findReleaseJSON(rel); ok {
		if c := r.candidateFromMeta(ctx, rel, asset, flavor); c != nil {
			return c
		}
	}
	return heuristicAsset(rel, flavor)
}

// findReleaseJSON reports the release.json metadata asset of rel, if any.
func findReleaseJSON(rel github.Release) (github.Asset, bool) {
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, releaseJSONName) {
			return a, true
		}
	}
	return github.Asset{}, false
}

// newest returns the candidate with the newest release: PublishedAt first,
// then the semver reading of the tag, then the tag name, so the order is
// total and deterministic even when timestamps repeat.
func newest(pool []*candidate) *candidate {
	var best *candidate
	for _, c := range pool {
		if best == nil || newer(c.release, best.release) {
			best = c
		}
	}
	return best
}

// newer reports whether a sorts after b as a release choice.
func newer(a, b github.Release) bool {
	if !a.PublishedAt.Equal(b.PublishedAt) {
		return a.PublishedAt.After(b.PublishedAt)
	}
	av, bv := semverValue(a.TagName), semverValue(b.TagName)
	for i := 0; i < len(av) || i < len(bv); i++ {
		x, y := 0, 0
		if i < len(av) {
			x = av[i]
		}
		if i < len(bv) {
			y = bv[i]
		}
		if x != y {
			return x > y
		}
	}
	return a.TagName > b.TagName
}

// semverValue extracts the leading dotted numeric part of a tag ("v1.2.3.4"
// -> [1 2 3 4]) for tie-breaking release order.
func semverValue(tag string) []int {
	start := -1
	for i, r := range tag {
		if r >= '0' && r <= '9' {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var out []int
	for _, part := range strings.Split(tag[start:], ".") {
		digits := part
		for i, r := range digits {
			if r < '0' || r > '9' {
				digits = digits[:i]
				break
			}
		}
		if digits == "" {
			break
		}
		n, err := strconv.Atoi(digits)
		if err != nil {
			break
		}
		out = append(out, n)
		if len(digits) == len(part) {
			continue
		}
		break // the numeric run ended inside this part ("1.2.3-rc1")
	}
	return out
}

// assetClass is the zip-name reading of one release asset.
type assetClass struct {
	nolib  bool // name carries a nolib token
	tagged bool // name carries a flavor tag
	token  string
}

// isZipName reports whether name names a zip archive (the only release
// asset format bfctl unpacks).
func isZipName(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".zip")
}

// heuristicAsset picks the best zip-name match inside one release (plan §2
// fallback): a flavor-tagged zip beats an untagged one, libs bundled beats
// nolib, and the file name breaks remaining ties deterministically.
func heuristicAsset(rel github.Release, flavor client.Flavor) *candidate {
	var best *candidate
	for _, a := range rel.Assets {
		class, ok := classifyAsset(a.Name, flavor)
		if !ok {
			continue
		}
		c := &candidate{
			release:    rel,
			asset:      a,
			nolib:      class.nolib,
			tagged:     class.tagged,
			flavorName: class.token,
		}
		if best == nil || betterAsset(c, best) {
			best = c
		}
	}
	return best
}

// betterAsset orders two candidates of the same release.
func betterAsset(a, b *candidate) bool {
	if a.tagged != b.tagged {
		return a.tagged
	}
	if a.nolib != b.nolib {
		return !a.nolib
	}
	return a.asset.Name < b.asset.Name
}

// classifyAsset reads a release asset's file name the way the plan's zip-name
// heuristics describe it: "*_<Mainline|Vanilla|Classic|Mists|Camelot|Forever>[_-]*.zip",
// CurseForge-style "*-<flavor>*-<version>.zip" and the flavor-at-end packager
// variant are all covered by matching flavor tokens anywhere in the name. An
// untagged zip is treated as a mainline package (in packager naming the
// retail zip is the untagged one). Assets tagged with a flavor bfctl does not
// support (wrath, cata, ...) or with several conflicting flavors never match.
func classifyAsset(name string, flavor client.Flavor) (assetClass, bool) {
	var c assetClass
	if !isZipName(name) {
		return c, false
	}
	base := strings.ToLower(name[:len(name)-len(".zip")])
	tokens := strings.FieldsFunc(base, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' '
	})
	var seen client.Flavor
	for _, t := range tokens {
		if t == "nolib" || t == "nolibs" {
			c.nolib = true
			continue
		}
		if isForeignFlavor(t) {
			return assetClass{}, false // another game flavor's package
		}
		f, ok := flavorForKey(t)
		if !ok {
			continue
		}
		if seen != "" && seen != f {
			return assetClass{}, false // conflicting flavor tags
		}
		seen = f
		c.tagged = true
		c.token = t
	}
	switch {
	case !c.tagged:
		// An untagged zip serves mainline only.
		return c, flavor == client.FlavorMainline
	case seen == flavor:
		return c, true
	default:
		return assetClass{}, false
	}
}
