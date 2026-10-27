package adopt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/state"
	"github.com/itp5b5/blessforge/internal/toc"
)

// fixtureDir is the Interface/AddOns tree of the fixture WoW root: the five
// acceptance cases (2 known-mapped, 1 WoWI-ID, 1 unknown, 1 multi-folder
// family) plus one orphan folder without a .toc.
var fixtureDir = filepath.Join("testdata", "wowroot", "_forever_", "Interface", "AddOns")

// fixtureTarget is the fixture client: Forever 1.60.1, Interface 16001.
var fixtureTarget = &toc.Target{Flavor: client.FlavorForever, Interface: 16001}

// writeTree writes the path -> content map under root and returns root.
func writeTree(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	for path, content := range files {
		dst := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatalf("create %s: %v", dst, err)
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", dst, err)
		}
	}
	return root
}

// findGroup returns the proposal with the given id, failing the test when it
// is missing.
func findGroup(t *testing.T, rep *Report, id string) *Group {
	t.Helper()
	for _, g := range rep.Groups {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("no proposal for %q in %+v", id, rep.Groups)
	return nil
}

// TestScanFixture runs the acceptance fixture through adopt: the binding
// table must propose four bindings (two known-mapped, one WoWI, one family)
// and one manual entry, and report the orphan folder.
func TestScanFixture(t *testing.T) {
	rep, err := Scan(fixtureDir, Options{Target: fixtureTarget})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(rep.Groups) != 5 {
		t.Fatalf("got %d groups, want 5: %+v", len(rep.Groups), rep.Groups)
	}
	if len(rep.Orphans) != 1 || rep.Orphans[0].Dir != "OrphanFolder" {
		t.Errorf("orphans = %+v, want just OrphanFolder", rep.Orphans)
	}

	dbm := findGroup(t, rep, "DeadlyBossMods")
	if dbm.Action != ActionBind || dbm.Source.Slug != "DeadlyBossMods/DeadlyBossMods" {
		t.Errorf("DBM-Core = %+v, want a bind to DeadlyBossMods/DeadlyBossMods", dbm)
	}
	if dbm.Source.Kind != state.SourceGitHub || dbm.Version != "11.2.0" {
		t.Errorf("DBM-Core source/version = %s %q", dbm.Source.Kind, dbm.Version)
	}

	wa := findGroup(t, rep, "WeakAuras2")
	if wa.Action != ActionBind || wa.Source.Slug != "WeakAuras/WeakAuras2" {
		t.Errorf("WeakAuras = %+v, want a bind to WeakAuras/WeakAuras2", wa)
	}
	if wa.Verdicts["WeakAuras"] != toc.VerdictMultiMatch {
		t.Errorf("WeakAuras verdict = %v, want MultiMatch (multi-interface toc)", wa.Verdicts["WeakAuras"])
	}

	wowi := findGroup(t, rep, "SomeWoWIAddon")
	if wowi.Action != ActionBind || wowi.Source.Kind != state.SourceWoWI || wowi.Source.Slug != "24910" {
		t.Errorf("SomeWoWIAddon = %+v, want a bind to wowi 24910", wowi)
	}
	if wowi.Strategy != state.StrategyDirect {
		t.Errorf("SomeWoWIAddon strategy = %q, want %q", wowi.Strategy, state.StrategyDirect)
	}

	family := findGroup(t, rep, "BigWigs")
	if family.Action != ActionBind || family.Source.Slug != "BigWigsMods/BigWigs" {
		t.Errorf("BigWigs = %+v, want a bind to BigWigsMods/BigWigs", family)
	}
	wantFolders := []string{"BigWigs", "BigWigs_Core", "BigWigs_Options", "BigWigs_Plugins"}
	if strings.Join(family.Folders, ",") != strings.Join(wantFolders, ",") {
		t.Errorf("BigWigs folders = %v, want %v", family.Folders, wantFolders)
	}
	if !hasNotePrefix(family, "family:") {
		t.Errorf("BigWigs notes = %v, want a family grouping note", family.Notes)
	}

	manual := findGroup(t, rep, "MysteryAddon")
	if manual.Action != ActionManual || manual.Source.Kind != state.SourceManual {
		t.Errorf("MysteryAddon = %+v, want a manual entry", manual)
	}
	if manual.Verdicts["MysteryAddon"] != toc.VerdictMismatch {
		t.Errorf("MysteryAddon verdict = %v, want Mismatch (wrong interface)", manual.Verdicts["MysteryAddon"])
	}
	if !hasNotePrefix(manual, "MysteryAddon:") {
		t.Errorf("MysteryAddon notes = %v, want a verdict note", manual.Notes)
	}
}

// hasNotePrefix reports whether any note of g starts with prefix.
func hasNotePrefix(g *Group, prefix string) bool {
	for _, n := range g.Notes {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// TestPersistFixture writes the fixture proposals into a state file and
// reads it back: bindings and manual entries must survive the round trip
// with their folders, and the manual entry must stay untouchable by sync.
func TestPersistFixture(t *testing.T) {
	rep, err := Scan(fixtureDir, Options{Target: fixtureTarget})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	st := state.New()
	st.Clients[client.FlavorForever] = &state.ClientState{
		Flavor:    client.FlavorForever,
		Root:      filepath.Dir(filepath.Dir(fixtureDir)),
		Interface: 16001,
		Addons:    make(map[string]*state.AddonState),
	}
	if err := Persist(st, client.FlavorForever, rep); err != nil {
		t.Fatalf("persist: %v", err)
	}

	path := filepath.Join(t.TempDir(), "state.json")
	store := state.Open(path)
	if err := store.Save(st); err != nil {
		t.Fatalf("save state: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	cs := loaded.Client(client.FlavorForever)
	if len(cs.Addons) != 5 {
		t.Fatalf("state holds %d addons, want 5", len(cs.Addons))
	}
	dbm := cs.Addons["DeadlyBossMods"]
	if dbm == nil || dbm.Source.Kind != state.SourceGitHub || dbm.Source.Slug != "DeadlyBossMods/DeadlyBossMods" {
		t.Errorf("DBM-Core state = %+v", dbm)
	}
	if dbm.InstalledVersion != "11.2.0" {
		t.Errorf("DBM-Core version = %q, want 11.2.0", dbm.InstalledVersion)
	}
	family := cs.Addons["BigWigs"]
	if family == nil || len(family.OwnedDirs) != 4 {
		t.Errorf("BigWigs state = %+v, want 4 owned folders", family)
	}
	manual := cs.Addons["MysteryAddon"]
	if manual == nil || manual.Source.Kind != state.SourceManual || manual.Strategy != state.StrategyManual {
		t.Errorf("MysteryAddon state = %+v, want a manual entry", manual)
	}
}

// TestScanSkipsAlreadyOwned checks the already-managed edge cases: a
// family bound to a source the state already tracks, and a family whose
// folders are owned by another entry, are skipped — not re-bound.
func TestScanSkipsAlreadyOwned(t *testing.T) {
	newState := func(addons map[string]*state.AddonState) *state.State {
		st := state.New()
		st.Clients[client.FlavorForever] = &state.ClientState{
			Flavor:    client.FlavorForever,
			Interface: 16001,
			Addons:    addons,
		}
		return st
	}

	// Same id and source: skipped with the state-entry note.
	st := newState(map[string]*state.AddonState{
		"DeadlyBossMods": {
			ID:        "DeadlyBossMods",
			Source:    state.SourceRef{Kind: state.SourceGitHub, Slug: "DeadlyBossMods/DeadlyBossMods"},
			OwnedDirs: []string{"DBM-Core"},
		},
	})
	rep, err := Scan(fixtureDir, Options{Target: fixtureTarget, St: st, Flavor: client.FlavorForever})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	dbm := findGroup(t, rep, "DeadlyBossMods")
	if dbm.Action != ActionSkip {
		t.Errorf("DBM-Core = %+v, want skip (already managed)", dbm)
	}
	if !hasNotePrefix(dbm, "state entry") {
		t.Errorf("DBM-Core notes = %v, want a state-entry note", dbm.Notes)
	}

	// Different id and source, but the folder is owned: skipped with the
	// ownership note.
	st = newState(map[string]*state.AddonState{
		"old-entry": {
			ID:        "old-entry",
			Source:    state.SourceRef{Kind: state.SourceGitHub, Slug: "other/repo"},
			OwnedDirs: []string{"DBM-Core"},
		},
	})
	rep, err = Scan(fixtureDir, Options{Target: fixtureTarget, St: st, Flavor: client.FlavorForever})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	dbm = findGroup(t, rep, "DeadlyBossMods")
	if dbm.Action != ActionSkip {
		t.Errorf("DBM-Core = %+v, want skip (folder owned elsewhere)", dbm)
	}
	if !hasNotePrefix(dbm, "folder DBM-Core is owned") {
		t.Errorf("DBM-Core notes = %v, want an ownership note", dbm.Notes)
	}
}

// TestBindOverride covers the manual --bind override: it wins over the TOC
// metadata, and an unusable value degrades to a manual entry with a note.
func TestBindOverride(t *testing.T) {
	rep, err := Scan(fixtureDir, Options{
		Target: fixtureTarget,
		Bind:   map[string]string{"MysteryAddon": "acme/mystery"},
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	manual := findGroup(t, rep, "mystery")
	if manual.Action != ActionBind || manual.Source.Slug != "acme/mystery" {
		t.Errorf("MysteryAddon = %+v, want a bind to acme/mystery", manual)
	}
	if manual.Reason != "--bind acme/mystery" {
		t.Errorf("reason = %q, want --bind acme/mystery", manual.Reason)
	}

	rep, err = Scan(fixtureDir, Options{
		Target: fixtureTarget,
		Bind:   map[string]string{"MysteryAddon": "not a slug"},
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	manual = findGroup(t, rep, "MysteryAddon")
	if manual.Action != ActionManual {
		t.Errorf("MysteryAddon = %+v, want manual (unusable --bind)", manual)
	}
	if !hasNotePrefix(manual, "cannot use --bind") {
		t.Errorf("MysteryAddon notes = %v, want a --bind error note", manual.Notes)
	}
}

// TestKnownByID checks the curated table's ID lookup: a TOC carrying a
// verified Curse or Wago project ID maps to its GitHub slug even when no
// folder name matches.
func TestKnownByID(t *testing.T) {
	table := []KnownAddon{
		{Slug: "acme/widget", Name: "Widget", Curse: "90210", Names: []string{"Widget"}},
		{Slug: "acme/gadget", Name: "Gadget", Wago: "4242", Names: []string{"Gadget"}},
	}
	if k, ok := lookupKnown(table, "90210", "", ""); !ok || k.Slug != "acme/widget" {
		t.Errorf("lookup by curse id = %+v %v", k, ok)
	}
	if k, ok := lookupKnown(table, "", "4242", ""); !ok || k.Slug != "acme/gadget" {
		t.Errorf("lookup by wago id = %+v %v", k, ok)
	}
	if _, ok := lookupKnown(table, "1", "2", "Unknown"); ok {
		t.Error("lookup with unknown identifiers matched")
	}

	dir := writeTree(t, filepath.Join(t.TempDir(), "AddOns"), map[string]string{
		"Whatever/Whatever.toc": "## Interface: 16001\n## X-Curse-Project-ID: 90210\n",
	})
	rep, err := Scan(dir, Options{Known: table, Target: fixtureTarget})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	g := findGroup(t, rep, "widget")
	if g.Action != ActionBind || g.Source.Slug != "acme/widget" {
		t.Errorf("Whatever = %+v, want a bind to acme/widget via the Curse ID", g)
	}
}

// TestDuplicateIDsConflict covers duplicate identifiers across folders: two
// folders sharing an ID but mapping to different known upstreams must not
// pick one at random — they degrade to a manual entry with a conflict note.
func TestDuplicateIDsConflict(t *testing.T) {
	dir := writeTree(t, filepath.Join(t.TempDir(), "AddOns"), map[string]string{
		"DBM-Core/DBM-Core.toc":   "## Interface: 16001\n## X-Curse-Project-ID: 1\n",
		"WeakAuras/WeakAuras.toc": "## Interface: 16001\n## X-Curse-Project-ID: 1\n",
	})
	rep, err := Scan(dir, Options{Target: fixtureTarget})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(rep.Groups) != 1 {
		t.Fatalf("got %d groups, want 1 (shared ID): %+v", len(rep.Groups), rep.Groups)
	}
	g := rep.Groups[0]
	if g.Action != ActionManual || g.Reason != "conflicting identifiers" {
		t.Errorf("group = %+v, want a manual entry for conflicting identifiers", g)
	}
	if !hasNotePrefix(g, "folders map to different known upstreams") {
		t.Errorf("notes = %v, want a conflict note", g.Notes)
	}
}

// TestRepositorySources checks TOC repository metadata: GitHub URLs become
// github sources, other forges become git-direct sources, and two folders
// pointing at the same repository merge into one family binding.
func TestRepositorySources(t *testing.T) {
	dir := writeTree(t, filepath.Join(t.TempDir(), "AddOns"), map[string]string{
		"GitLabAddon/GitLabAddon.toc": "## Interface: 16001\n## X-Repository: https://gitlab.com/group/addon.git\n",
		"SharedOne/SharedOne.toc":     "## Interface: 16001\n## X-Repository: https://github.com/foo/bar\n",
		"SharedTwo/SharedTwo.toc":     "## Interface: 16001\n## X-Repository: https://github.com/foo/bar.git\n",
	})
	rep, err := Scan(dir, Options{Target: fixtureTarget})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(rep.Groups) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(rep.Groups), rep.Groups)
	}
	git := findGroup(t, rep, "addon")
	if git.Source.Kind != state.SourceGit || git.Source.URL != "https://gitlab.com/group/addon.git" {
		t.Errorf("GitLabAddon = %+v, want a git-direct source", git)
	}
	if git.Strategy != state.StrategyDirect {
		t.Errorf("GitLabAddon strategy = %q, want %q", git.Strategy, state.StrategyDirect)
	}
	shared := findGroup(t, rep, "bar")
	if shared.Action != ActionBind || shared.Source.Slug != "foo/bar" {
		t.Errorf("shared = %+v, want a merged bind to foo/bar", shared)
	}
	if strings.Join(shared.Folders, ",") != "SharedOne,SharedTwo" {
		t.Errorf("shared folders = %v, want SharedOne + SharedTwo", shared.Folders)
	}
}

// TestWagoBinding covers the Wago Addons binding path: an X-Wago-ID without
// a higher-precedence match binds a wago:<WagoID> direct source (the short ID
// or the legacy numeric ID is the provider's URL key), folders sharing the
// Wago ID group into one family and X-Repository still outranks X-Wago-ID.
// A family bfctl has no provider for stays manual and names its unbound IDs.
func TestWagoBinding(t *testing.T) {
	dir := writeTree(t, filepath.Join(t.TempDir(), "AddOns"), map[string]string{
		"FooWago/FooWago.toc":         "## Interface: 16001\n## Title: Foo Wago\n## Version: 2.0.0\n## X-Wago-ID: VBNBxKx5\n",
		"FooWago_Opt/FooWago_Opt.toc": "## Interface: 16001\n## Title: Foo Wago Options\n## X-Wago-ID: VBNBxKx5\n",
		"RepoFirst/RepoFirst.toc":     "## Interface: 16001\n## X-Wago-ID: zzzzz\n## X-Repository: https://github.com/acme/repo-first\n",
		"CurseOnly/CurseOnly.toc":     "## Interface: 16001\n## X-Curse-Project-ID: 8315\n",
	})
	rep, err := Scan(dir, Options{Target: fixtureTarget})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(rep.Groups) != 3 {
		t.Fatalf("got %d groups, want 3: %+v", len(rep.Groups), rep.Groups)
	}
	foo := findGroup(t, rep, "FooWago")
	if foo.Action != ActionBind || foo.Source.Kind != state.SourceWago || foo.Source.Slug != "VBNBxKx5" {
		t.Errorf("FooWago = %+v, want a bind to wago VBNBxKx5", foo)
	}
	if foo.Strategy != state.StrategyDirect {
		t.Errorf("FooWago strategy = %q, want %q", foo.Strategy, state.StrategyDirect)
	}
	if foo.Reason != "X-Wago-ID VBNBxKx5" {
		t.Errorf("FooWago reason = %q, want X-Wago-ID VBNBxKx5", foo.Reason)
	}
	if strings.Join(foo.Folders, ",") != "FooWago,FooWago_Opt" {
		t.Errorf("FooWago folders = %v, want the family grouped by the shared Wago ID", foo.Folders)
	}
	first := findGroup(t, rep, "repo-first")
	if first.Source.Kind != state.SourceGitHub || first.Source.Slug != "acme/repo-first" {
		t.Errorf("RepoFirst = %+v, want X-Repository to outrank X-Wago-ID", first)
	}
	curse := findGroup(t, rep, "CurseOnly")
	if curse.Action != ActionManual || !strings.Contains(curse.Reason, "curse 8315") {
		t.Errorf("CurseOnly = %+v, want a manual entry naming its unbound curse id", curse)
	}
}
