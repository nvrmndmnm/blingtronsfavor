// Package adopt proposes bfctl bindings for addons that already live in a
// World of Warcraft Interface/AddOns directory: it scans every addon folder's
// .toc metadata, groups multi-folder families by their shared project IDs,
// maps each family to its upstream through the curated table in known.go and
// reports a proposed-binding table. Unknown addons are tracked as manual
// entries — installed by a foreign tool, never touched by "bfctl sync" until
// the user binds them explicitly.
package adopt

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/state"
	"github.com/itp5b5/blessforge/internal/toc"
)

// Action is what adopt proposes for one addon family.
type Action string

const (
	// ActionBind binds the family to a resolved upstream: state records it
	// as a managed addon and sync owns its updates.
	ActionBind Action = "bind"

	// ActionManual records a manual entry: installed by a foreign tool,
	// tracked but never touched by sync until re-bound with --bind.
	ActionManual Action = "manual"

	// ActionSkip leaves the family alone (already managed in the state).
	ActionSkip Action = "skip"
)

// Group is one proposed binding: a single addon family — one or more AddOns
// directories sharing project IDs or a resolved upstream — and the source it
// maps to.
type Group struct {
	ID       string          // proposed state entry id
	Title    string          // display name for the binding table
	Folders  []string        // addon directory names, sorted
	Source   state.SourceRef // proposed upstream
	Strategy state.Strategy  // proposed install strategy ("" = decided at sync time)
	Version  string          // "## Version" of the primary .toc
	Action   Action
	Reason   string   // why this source was chosen
	Notes    []string // conflicts and caveats worth showing
	Verdicts map[string]toc.Verdict
}

// Orphan is an AddOns directory adopt cannot propose anything for.
type Orphan struct {
	Dir    string
	Reason string
}

// Report is one complete scan result.
type Report struct {
	Dir     string
	Groups  []*Group
	Orphans []*Orphan
}

// Options tunes Scan. The zero value scans with the curated table, no
// manual overrides, no state (so nothing is ever skipped as already managed)
// and Unknown compatibility verdicts.
type Options struct {
	Known  []KnownAddon      // curated mapping table; nil selects KnownAddons
	Bind   map[string]string // manual overrides: addon folder name -> "owner/repo"
	St     *state.State      // existing state, for already-owned detection
	Flavor client.Flavor     // client flavor the addons are adopted for
	Target *toc.Target       // compatibility target; nil reports Unknown verdicts
}

// folder is one scanned AddOns directory with its selected .toc.
type folder struct {
	name    string
	toc     *toc.Toc
	verdict toc.Verdict
}

// Scan proposes bindings for every addon directory under dir. It never
// touches the filesystem outside dir and never mutates state: the caller
// shows the table and, on --yes, writes the proposals via Persist.
func Scan(dir string, opts Options) (*Report, error) {
	table := opts.Known
	if table == nil {
		table = KnownAddons
	}
	addons, err := toc.Scan(dir, opts.Target)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	rep := &Report{Dir: dir}
	var folders []*folder
	for _, a := range addons {
		if a.Toc == nil {
			rep.Orphans = append(rep.Orphans, &Orphan{Dir: a.Dir, Reason: "orphan: no .toc file"})
			continue
		}
		folders = append(folders, &folder{name: a.Dir, toc: a.Toc, verdict: a.Verdict})
	}

	for _, g := range groupFolders(folders) {
		rep.Groups = append(rep.Groups, propose(g, table, opts))
	}
	mergeBySource(rep.Groups)
	for _, g := range rep.Groups {
		applyStateConflicts(g, opts)
	}
	sort.Slice(rep.Groups, func(i, j int) bool {
		a, b := rep.Groups[i], rep.Groups[j]
		if a.Action != b.Action {
			return actionRank(a.Action) < actionRank(b.Action)
		}
		return a.ID < b.ID
	})
	return rep, nil
}

// actionRank orders proposals: binds first, then manuals, then skips.
func actionRank(a Action) int {
	switch a {
	case ActionBind:
		return 0
	case ActionManual:
		return 1
	default:
		return 2
	}
}

// groupFolders unions folders that share a project ID (Curse, Wago, WoWI) or
// a normalized repository URL — the multi-folder families — and returns one
// folder set per family, each sorted by directory name.
func groupFolders(folders []*folder) [][]*folder {
	set := newDisjointSet(len(folders))
	byKey := make(map[string]int)
	for i, f := range folders {
		for _, key := range identityKeys(f.toc) {
			if j, ok := byKey[key]; ok {
				set.union(i, j)
			} else {
				byKey[key] = i
			}
		}
	}
	groups := make(map[int][]*folder)
	for i, f := range folders {
		groups[set.find(i)] = append(groups[set.find(i)], f)
	}
	out := make([][]*folder, 0, len(groups))
	for _, g := range groups {
		sort.Slice(g, func(i, j int) bool { return g[i].name < g[j].name })
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0].name < out[j][0].name })
	return out
}

// identityKeys are the family identity keys of one .toc: every non-empty
// project ID plus the normalized repository URL.
func identityKeys(t *toc.Toc) []string {
	var keys []string
	if t.CurseProjectID != "" {
		keys = append(keys, "curse:"+t.CurseProjectID)
	}
	if t.WagoID != "" {
		keys = append(keys, "wago:"+t.WagoID)
	}
	if t.WoWIID != "" {
		keys = append(keys, "wowi:"+t.WoWIID)
	}
	if repo := normalizeRepoURL(firstURL(t.Repository, t.Website)); repo != "" {
		keys = append(keys, "repo:"+repo)
	}
	return keys
}

// propose resolves the upstream for one family and proposes an action.
func propose(folders []*folder, table []KnownAddon, opts Options) *Group {
	g := &Group{
		Verdicts: make(map[string]toc.Verdict, len(folders)),
	}
	for _, f := range folders {
		g.Folders = append(g.Folders, f.name)
		g.Verdicts[f.name] = f.verdict
	}
	primary := folders[0]
	g.Title = primary.toc.Title
	if g.Title == "" {
		g.Title = primary.name
	}
	g.Version = primary.toc.Version

	src, strategy, reason, notes := resolveSource(folders, table, opts.Bind)
	g.Source, g.Strategy, g.Reason, g.Notes = src, strategy, reason, notes
	if g.Source.Kind == state.SourceManual {
		g.Action = ActionManual
		g.ID = primary.name
	} else {
		g.Action = ActionBind
		// GitHub and git sources take the repository name as their state id
		// (the same convention "bfctl add" uses); provider IDs are opaque
		// numbers, so those entries keep the folder name.
		switch g.Source.Kind {
		case state.SourceGitHub, state.SourceGit:
			g.ID = slugName(g.Source.Slug)
		}
		if g.ID == "" {
			g.ID = primary.name
		}
	}
	if len(folders) > 1 {
		g.Notes = append(g.Notes, fmt.Sprintf("family: %d folders grouped by shared identifiers", len(folders)))
	}
	for _, f := range folders {
		if f.verdict > toc.VerdictMultiMatch {
			g.Notes = append(g.Notes, fmt.Sprintf("%s: %s for the target client", f.name, f.verdict))
		}
	}
	return g
}

// resolveSource maps one family to its upstream, highest precedence first:
//
//  1. a --bind manual override (folder -> "owner/repo" or
//     "cfwidget:<projectId>");
//  2. the curated table (Curse/Wago ID first, folder and TOC names second);
//  3. an explicit X-Repository / X-Website git URL (GitHub -> source kind
//     github, other forges -> git-direct);
//  4. an X-WoWI-ID -> the WoWInterface provider;
//  5. an X-Wago-ID -> the Wago Addons provider;
//  6. an X-Curse-Project-ID -> the cfwidget CurseForge mirror;
//  7. nothing matched -> a manual entry.
func resolveSource(folders []*folder, table []KnownAddon, bind map[string]string) (state.SourceRef, state.Strategy, string, []string) {
	var notes []string

	// 1. --bind overrides for any folder of the family.
	var bound string
	for _, f := range folders {
		if slug := bind[f.name]; slug != "" {
			if bound == "" {
				bound = slug
			} else if bound != slug {
				notes = append(notes, fmt.Sprintf("conflicting --bind values %q and %q; using %q", bound, slug, bound))
			}
		}
	}
	if bound != "" {
		ref, strategy, err := parseBindRef(bound)
		if err != nil {
			notes = append(notes, fmt.Sprintf("cannot use --bind value %q: %v; tracked as manual", bound, err))
			return manualRef(folders[0]), state.StrategyManual, "unusable --bind override", notes
		}
		return ref, strategy, "--bind " + bound, notes
	}

	// 2. curated table, checked per folder so family members that carry
	// different IDs cannot silently pick a random upstream.
	var (
		matched state.SourceRef
		reason  string
	)
	for _, f := range folders {
		k, ok := lookupKnown(table, f.toc.CurseProjectID, f.toc.WagoID, f.toc.Name)
		if !ok {
			k, ok = lookupKnown(table, f.toc.CurseProjectID, f.toc.WagoID, f.name)
		}
		if !ok {
			continue
		}
		ref := state.SourceRef{Kind: state.SourceGitHub, URL: k.SlugURL(), Slug: k.Slug}
		if matched.Slug == "" {
			matched, reason = ref, "known addon "+k.Name
			continue
		}
		if matched.Slug != ref.Slug {
			notes = append(notes, fmt.Sprintf("folders map to different known upstreams (%s and %s); bind manually with --bind", matched.Slug, ref.Slug))
			return manualRef(folders[0]), state.StrategyManual, "conflicting identifiers", notes
		}
	}
	if matched.Slug != "" {
		return matched, "", reason, notes
	}

	// 3. explicit repository URL in the TOC metadata.
	for _, f := range folders {
		raw := firstURL(f.toc.Repository, f.toc.Website)
		if raw == "" {
			continue
		}
		if ref, ok := gitRef(raw); ok {
			return ref.src, ref.strategy, "X-Repository " + raw, notes
		}
		notes = append(notes, fmt.Sprintf("%s: repository URL %q is not a git remote; ignored", f.name, raw))
	}

	// 4. WoWInterface project ID.
	for _, f := range folders {
		if f.toc.WoWIID != "" {
			return state.SourceRef{Kind: state.SourceWoWI, Slug: f.toc.WoWIID}, state.StrategyDirect,
				"X-WoWI-ID " + f.toc.WoWIID, notes
		}
	}

	// 5. Wago Addons project ID — the short ID or the legacy numeric ID
	// both work as the Wago provider's URL key.
	for _, f := range folders {
		if f.toc.WagoID != "" {
			return state.SourceRef{Kind: state.SourceWago, Slug: f.toc.WagoID}, state.StrategyDirect,
				"X-Wago-ID " + f.toc.WagoID, notes
		}
	}

	// 6. CurseForge project ID — the cfwidget mirror's key is the numeric
	// project ID straight from "## X-Curse-Project-ID".
	for _, f := range folders {
		if f.toc.CurseProjectID != "" {
			return state.SourceRef{Kind: state.SourceCfwidget, Slug: f.toc.CurseProjectID}, state.StrategyDirect,
				"X-Curse-Project-ID " + f.toc.CurseProjectID, notes
		}
	}

	// 7. nothing matched: a manual entry.
	return manualRef(folders[0]), state.StrategyManual, "no upstream found", notes
}

// parseBindRef resolves one --bind value: "owner/repo" binds the GitHub
// source (the release/staleness resolver picks the install strategy), and
// "cfwidget:<projectId>" binds the CurseForge mirror by numeric project ID
// (the "## X-Curse-Project-ID" value) with the direct-file strategy — the
// way to keep an addon whose only source of truth is CurseForge maintained
// through CurseForge.
func parseBindRef(value string) (state.SourceRef, state.Strategy, error) {
	if slug, ok := strings.CutPrefix(value, string(state.SourceCfwidget)+":"); ok {
		if slug != "" {
			return state.SourceRef{Kind: state.SourceCfwidget, Slug: slug}, state.StrategyDirect, nil
		}
		return state.SourceRef{}, "", errors.New("empty cfwidget project id")
	}
	ref, err := resolver.ParseRepo(value)
	if err != nil {
		return state.SourceRef{}, "", errors.New("want folder=owner/repo or cfwidget:<projectId>")
	}
	return state.SourceRef{Kind: state.SourceGitHub, URL: ref.URL(), Slug: ref.Slug()}, "", nil
}

// gitRefClassified is the source classification of a repository URL.
type gitRefClassified struct {
	src      state.SourceRef
	strategy state.Strategy
}

// gitRef classifies a repository URL from the TOC metadata: GitHub URLs
// become github sources (the full release resolver), any other git remote
// becomes a git-direct source built through the packager pipeline.
func gitRef(raw string) (gitRefClassified, bool) {
	if ref, err := resolver.ParseRepo(raw); err == nil {
		return gitRefClassified{
			src: state.SourceRef{Kind: state.SourceGitHub, URL: ref.URL(), Slug: ref.Slug()},
		}, true
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "git") {
		return gitRefClassified{}, false
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.HasSuffix(host, "gitlab.com"), strings.HasSuffix(host, "codeberg.org"),
		strings.HasSuffix(raw, ".git"), strings.HasPrefix(raw, "git://"):
		return gitRefClassified{
			src:      state.SourceRef{Kind: state.SourceGit, URL: raw, Slug: host + "/" + strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")},
			strategy: state.StrategyDirect,
		}, true
	}
	return gitRefClassified{}, false
}

// mergeBySource merges groups that resolve to the same upstream — a family
// split across folders without shared IDs — so one addon is never proposed
// twice. Manual and skipped groups are left alone.
func mergeBySource(groups []*Group) {
	byKey := make(map[string]*Group)
	merged := groups[:0]
	for _, g := range groups {
		key := sourceKey(g.Source)
		if key == "" {
			merged = append(merged, g)
			continue
		}
		if prev, ok := byKey[key]; ok {
			prev.Folders = append(prev.Folders, g.Folders...)
			sort.Strings(prev.Folders)
			for name, v := range g.Verdicts {
				prev.Verdicts[name] = v
			}
			if prev.Version == "" {
				prev.Version = g.Version
			}
			continue
		}
		byKey[key] = g
		merged = append(merged, g)
	}
}

// applyStateConflicts downgrades a proposal to a skip when the state already
// manages the addon or any of its folders: bound to the same upstream under
// another id, or the folders are owned by an existing entry. Manual entries
// are exempt: re-binding an addon a foreign tool installed is exactly what
// --bind is for, and Persist replaces those entries.
func applyStateConflicts(g *Group, opts Options) {
	st := opts.St
	if st == nil {
		return
	}
	cs := st.Client(opts.Flavor)
	if cs == nil {
		return
	}
	for _, a := range cs.Addons {
		if isManual(a) && g.Action == ActionBind {
			continue // re-binding a manual entry: not a conflict
		}
		if a.ID == g.ID {
			g.Action = ActionSkip
			g.Reason = "already managed"
			g.Notes = append(g.Notes, fmt.Sprintf("state entry %q already owns this addon", a.ID))
			return
		}
		if sourceKey(a.Source) != "" && sourceKey(a.Source) == sourceKey(g.Source) {
			g.Action = ActionSkip
			g.Reason = "already managed"
			g.Notes = append(g.Notes, fmt.Sprintf("already bound to %s as %q", sourceKey(g.Source), a.ID))
			return
		}
		for _, owned := range a.OwnedDirs {
			for _, f := range g.Folders {
				if owned == f {
					g.Action = ActionSkip
					g.Reason = "already managed"
					g.Notes = append(g.Notes, fmt.Sprintf("folder %s is owned by state entry %q", f, a.ID))
					return
				}
			}
		}
	}
}

// Persist writes the report's bind and manual proposals into the state for
// flavor, leaving skipped groups and any unrelated state untouched. It never
// writes the state file itself: the caller holds the state lock and saves.
func Persist(st *state.State, flavor client.Flavor, rep *Report) error {
	cs := st.Client(flavor)
	if cs == nil {
		return fmt.Errorf("no client configured for flavor %s: run \"bfctl init\" first", flavor)
	}
	if cs.Addons == nil {
		cs.Addons = make(map[string]*state.AddonState)
	}
	for _, g := range rep.Groups {
		switch g.Action {
		case ActionBind, ActionManual:
		default:
			continue
		}
		if prev := cs.Addons[g.ID]; prev != nil && !isManual(prev) && sourceKey(prev.Source) != sourceKey(g.Source) {
			return fmt.Errorf("state entry %q already exists with a different source (%s)", g.ID, sourceKey(prev.Source))
		}
		// A --bind re-binding replaces the manual entries that owned these
		// folders: same addon, now managed by bfctl.
		for id, a := range cs.Addons {
			if isManual(a) && ownsAny(a, g.Folders) {
				delete(cs.Addons, id)
			}
		}
		cs.Addons[g.ID] = &state.AddonState{
			ID:               g.ID,
			DisplayName:      g.Title,
			Source:           g.Source,
			Strategy:         g.Strategy,
			Tracking:         "stable",
			InstalledVersion: g.Version,
			OwnedDirs:        append([]string(nil), g.Folders...),
			UpdatedAt:        time.Now().UTC(),
		}
	}
	return nil
}

// isManual reports whether the state entry is a manual one: installed by a
// foreign tool, tracked but not managed until re-bound.
func isManual(a *state.AddonState) bool {
	return a.Source.Kind == state.SourceManual || a.Strategy == state.StrategyManual
}

// ownsAny reports whether the entry owns any of the given folders.
func ownsAny(a *state.AddonState, folders []string) bool {
	for _, owned := range a.OwnedDirs {
		for _, f := range folders {
			if owned == f {
				return true
			}
		}
	}
	return false
}

// manualRef is the source reference of a manual entry: installed by a
// foreign tool, tracked but not managed.
func manualRef(primary *folder) state.SourceRef {
	return state.SourceRef{Kind: state.SourceManual, Slug: primary.name}
}

// sourceKey is a stable identity for a source reference: the provider-local
// slug, falling back to the URL. Manual entries have no identity.
func sourceKey(s state.SourceRef) string {
	if s.Kind == state.SourceManual {
		return ""
	}
	if s.Slug != "" {
		return string(s.Kind) + ":" + s.Slug
	}
	return string(s.Kind) + ":" + s.URL
}

// slugName renders the repo part of an "owner/repo" slug.
func slugName(slug string) string {
	if slug == "" {
		return ""
	}
	if i := strings.LastIndex(slug, "/"); i >= 0 {
		return slug[i+1:]
	}
	return slug
}

// firstURL returns the first non-empty candidate URL.
func firstURL(candidates ...string) string {
	for _, c := range candidates {
		if strings.TrimSpace(c) != "" {
			return strings.TrimSpace(c)
		}
	}
	return ""
}

// normalizeRepoURL reduces a repository URL to a comparison key: lowercase
// host, no scheme, no trailing slash or ".git".
func normalizeRepoURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	p := strings.TrimSuffix(strings.TrimRight(u.Path, "/"), ".git")
	return strings.ToLower(u.Host) + "/" + strings.Trim(p, "/")
}

// SlugURL renders the canonical GitHub web URL of a curated entry.
func (k KnownAddon) SlugURL() string {
	return "https://github.com/" + k.Slug
}

// disjointSet is a tiny union-find over folder indices, used to group addon
// folders into families by their shared identifiers.
type disjointSet struct{ parent []int }

func newDisjointSet(n int) *disjointSet {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return &disjointSet{parent: p}
}

func (d *disjointSet) find(x int) int {
	for d.parent[x] != x {
		d.parent[x] = d.parent[d.parent[x]]
		x = d.parent[x]
	}
	return x
}

func (d *disjointSet) union(a, b int) {
	ra, rb := d.find(a), d.find(b)
	if ra != rb {
		d.parent[rb] = ra
	}
}
