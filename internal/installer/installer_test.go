package installer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/state"
)

// testSetup points bfctl's config and cache at fresh temp directories and
// records a fake client whose Interface/AddOns lives in the test's own temp
// tree. It returns the installer, the store, the flavor and the WoW root.
func testSetup(t *testing.T) (*Installer, *state.Store, client.Flavor, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	if err := paths.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "_blessforge_")
	if err := os.MkdirAll(filepath.Join(root, "Interface", "AddOns"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	store := state.Open(path)
	st := state.New()
	st.Clients[client.FlavorMainline] = &state.ClientState{
		Flavor: client.FlavorMainline,
		Root:   root,
		Addons: make(map[string]*state.AddonState),
	}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	return &Installer{Store: store}, store, client.FlavorMainline, root
}

// writeAddonDir creates one addon directory with a matching .toc whose
// Version marks which payload it came from.
func writeAddonDir(t *testing.T, parent, name, version string) {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tocBody := "## Interface: 110004\n## Version: " + version + "\n"
	if err := os.WriteFile(filepath.Join(dir, name+".toc"), []byte(tocBody), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writePayload creates one addon directory per name under src, each with a
// matching .toc carrying the given version marker.
func writePayload(t *testing.T, src, version string, names ...string) {
	t.Helper()
	for _, name := range names {
		writeAddonDir(t, src, name, version)
	}
}

// dirNames lists the directory names directly under dir, sorted.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	return names
}

// readTree snapshots the addon tree under dir: every top-level directory with
// the contents of its files. Two snapshots being equal is the "exact prior
// tree" check rollback has to satisfy.
func readTree(t *testing.T, dir string) map[string]map[string]string {
	t.Helper()
	tree := make(map[string]map[string]string)
	for _, name := range dirNames(t, dir) {
		files := make(map[string]string)
		entries, err := os.ReadDir(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			files[entry.Name()] = string(data)
		}
		tree[name] = files
	}
	return tree
}

// loadAddon returns the recorded state entry for one addon, or nil.
func loadAddon(t *testing.T, store *state.Store, flavor client.Flavor, id string) *state.AddonState {
	t.Helper()
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	cs := st.Client(flavor)
	if cs == nil {
		return nil
	}
	return cs.Addons[id]
}

// trashLeftovers lists what the trash root still holds after an operation.
func trashLeftovers(t *testing.T) []string {
	t.Helper()
	trashRoot, err := paths.CacheSubdir("trash")
	if err != nil {
		t.Fatal(err)
	}
	return dirNames(t, trashRoot)
}

// stageLeftovers lists staging directories left next to Interface/AddOns.
func stageLeftovers(t *testing.T, addonsDir string) []string {
	t.Helper()
	var names []string
	for _, name := range dirNames(t, filepath.Dir(addonsDir)) {
		if strings.HasPrefix(name, ".bfctl-stage-") {
			names = append(names, name)
		}
	}
	return names
}

// TestAtomicInstallUpgradeIntegration is the acceptance test: install a
// three-directory addon, then upgrade it to a two-directory version. After
// the upgrade the old three directories must be gone, the new two present,
// and state.json must list exactly the new owned_dirs.
func TestAtomicInstallUpgradeIntegration(t *testing.T) {
	in, store, flavor, root := testSetup(t)
	addonsDir := filepath.Join(root, "Interface", "AddOns")

	srcV1 := filepath.Join(t.TempDir(), "v1")
	writePayload(t, srcV1, "v1", "DemoAddon", "DemoAddon_Config", "DemoAddon_Data")
	req := Request{
		Flavor:    flavor,
		Addon:     state.AddonState{ID: "DemoAddon", Strategy: state.StrategyRelease},
		SourceDir: srcV1,
	}
	if err := in.Install(req); err != nil {
		t.Fatalf("install v1: %v", err)
	}

	wantV1 := []string{"DemoAddon", "DemoAddon_Config", "DemoAddon_Data"}
	if got := dirNames(t, addonsDir); !slices.Equal(got, wantV1) {
		t.Fatalf("after install: addons = %v, want %v", got, wantV1)
	}
	a := loadAddon(t, store, flavor, "DemoAddon")
	if a == nil {
		t.Fatal("after install: no state entry for DemoAddon")
	}
	if !slices.Equal(a.OwnedDirs, wantV1) {
		t.Fatalf("after install: ownedDirs = %v, want %v", a.OwnedDirs, wantV1)
	}

	// Upgrade to a two-directory version.
	srcV2 := filepath.Join(t.TempDir(), "v2")
	writePayload(t, srcV2, "v2", "DemoAddon", "DemoAddon_Data")
	req.SourceDir = srcV2
	if err := in.Install(req); err != nil {
		t.Fatalf("install v2: %v", err)
	}

	wantV2 := []string{"DemoAddon", "DemoAddon_Data"}
	if got := dirNames(t, addonsDir); !slices.Equal(got, wantV2) {
		t.Fatalf("after upgrade: addons = %v, want %v (old three gone, new two present)", got, wantV2)
	}
	a = loadAddon(t, store, flavor, "DemoAddon")
	if !slices.Equal(a.OwnedDirs, wantV2) {
		t.Fatalf("after upgrade: ownedDirs = %v, want %v", a.OwnedDirs, wantV2)
	}
	tocV2, err := os.ReadFile(filepath.Join(addonsDir, "DemoAddon", "DemoAddon.toc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tocV2), "v2") {
		t.Fatalf("after upgrade: DemoAddon.toc still carries the old payload: %q", tocV2)
	}
	if left := trashLeftovers(t); len(left) != 0 {
		t.Fatalf("after both commits: trash holds %v, want nothing", left)
	}
	if left := stageLeftovers(t, addonsDir); len(left) != 0 {
		t.Fatalf("after both commits: staging holds %v, want nothing", left)
	}
}

// TestAtomicRollbackRestoresPriorTree simulates a failure between the trash
// moves and the swap: the rollback must restore the exact prior tree, the
// state entry must be untouched, and no trash or stage residue may remain.
func TestAtomicRollbackRestoresPriorTree(t *testing.T) {
	in, store, flavor, root := testSetup(t)
	addonsDir := filepath.Join(root, "Interface", "AddOns")

	srcV1 := filepath.Join(t.TempDir(), "v1")
	writePayload(t, srcV1, "v1", "DemoAddon", "DemoAddon_Config", "DemoAddon_Data")
	req := Request{
		Flavor:    flavor,
		Addon:     state.AddonState{ID: "DemoAddon", Strategy: state.StrategyRelease},
		SourceDir: srcV1,
	}
	if err := in.Install(req); err != nil {
		t.Fatalf("install v1: %v", err)
	}
	before := readTree(t, addonsDir)

	in.failBeforeSwap = errors.New("simulated disk failure")
	srcV2 := filepath.Join(t.TempDir(), "v2")
	writePayload(t, srcV2, "v2", "DemoAddon", "DemoAddon_Data")
	req.SourceDir = srcV2
	err := in.Install(req)
	if err == nil {
		t.Fatal("install with simulated failure: succeeded, want error")
	}
	if !strings.Contains(err.Error(), "simulated disk failure") {
		t.Fatalf("install with simulated failure: error %q does not mention the cause", err)
	}

	if after := readTree(t, addonsDir); !reflect.DeepEqual(after, before) {
		t.Fatalf("after rollback: tree = %v, want exact prior tree %v", after, before)
	}
	a := loadAddon(t, store, flavor, "DemoAddon")
	if a == nil || !slices.Equal(a.OwnedDirs, []string{"DemoAddon", "DemoAddon_Config", "DemoAddon_Data"}) {
		t.Fatalf("after rollback: state entry = %+v, want the three v1 directories", a)
	}
	if left := trashLeftovers(t); len(left) != 0 {
		t.Fatalf("after rollback: trash holds %v, want nothing", left)
	}
	if left := stageLeftovers(t, addonsDir); len(left) != 0 {
		t.Fatalf("after rollback: staging holds %v, want nothing", left)
	}
}

// TestAtomicConflictPolicy checks the foreign-folder rule: a colliding
// directory that this addon does not own is refused without --force and left
// untouched, and only taken over when --force is passed.
func TestAtomicConflictPolicy(t *testing.T) {
	in, store, flavor, root := testSetup(t)
	addonsDir := filepath.Join(root, "Interface", "AddOns")

	// A foreign, unmanaged folder already sits in Interface/AddOns.
	writeAddonDir(t, addonsDir, "DemoAddon", "foreign")

	src := filepath.Join(t.TempDir(), "payload")
	writePayload(t, src, "v1", "DemoAddon")
	req := Request{
		Flavor:    flavor,
		Addon:     state.AddonState{ID: "DemoAddon", Strategy: state.StrategyRelease},
		SourceDir: src,
	}
	err := in.Install(req)
	if err == nil {
		t.Fatal("install over a foreign folder: succeeded, want refusal")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("install over a foreign folder: error %q does not mention --force", err)
	}
	got := readTree(t, addonsDir)
	want := map[string]map[string]string{"DemoAddon": {"DemoAddon.toc": "## Interface: 110004\n## Version: foreign\n"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after refused install: tree = %v, want untouched foreign folder %v", got, want)
	}

	// With --force the folder is taken over.
	req.Force = true
	if err := in.Install(req); err != nil {
		t.Fatalf("install with --force: %v", err)
	}
	a := loadAddon(t, store, flavor, "DemoAddon")
	if a == nil || !slices.Equal(a.OwnedDirs, []string{"DemoAddon"}) {
		t.Fatalf("after --force install: state entry = %+v, want ownership of DemoAddon", a)
	}
	tocBody, err := os.ReadFile(filepath.Join(addonsDir, "DemoAddon", "DemoAddon.toc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tocBody), "v1") {
		t.Fatalf("after --force install: DemoAddon.toc still carries the foreign payload: %q", tocBody)
	}
}

// TestAtomicRemoveAddon removes an installed addon and checks that its
// directories are gone, the state entry is dropped and nothing is left in
// the trash.
func TestAtomicRemoveAddon(t *testing.T) {
	in, store, flavor, root := testSetup(t)
	addonsDir := filepath.Join(root, "Interface", "AddOns")

	src := filepath.Join(t.TempDir(), "payload")
	writePayload(t, src, "v1", "DemoAddon", "DemoAddon_Config", "DemoAddon_Data")
	req := Request{
		Flavor:    flavor,
		Addon:     state.AddonState{ID: "DemoAddon", Strategy: state.StrategyRelease},
		SourceDir: src,
	}
	if err := in.Install(req); err != nil {
		t.Fatalf("install: %v", err)
	}

	if err := in.Remove(flavor, "DemoAddon"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := dirNames(t, addonsDir); len(got) != 0 {
		t.Fatalf("after remove: addons = %v, want none", got)
	}
	if a := loadAddon(t, store, flavor, "DemoAddon"); a != nil {
		t.Fatalf("after remove: state entry = %+v, want none", a)
	}
	if left := trashLeftovers(t); len(left) != 0 {
		t.Fatalf("after remove: trash holds %v, want nothing", left)
	}
}

// TestAtomicRejectsSymlinkPayload checks that a symlink anywhere in the
// payload is rejected before anything touches Interface/AddOns.
func TestAtomicRejectsSymlinkPayload(t *testing.T) {
	in, _, flavor, root := testSetup(t)
	addonsDir := filepath.Join(root, "Interface", "AddOns")

	src := filepath.Join(t.TempDir(), "payload")
	writePayload(t, src, "v1", "DemoAddon")
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "DemoAddon", "evil.lua")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	req := Request{
		Flavor:    flavor,
		Addon:     state.AddonState{ID: "DemoAddon", Strategy: state.StrategyRelease},
		SourceDir: src,
	}
	err := in.Install(req)
	if err == nil {
		t.Fatal("install with a symlink payload: succeeded, want rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("install with a symlink payload: error %q does not mention the symlink", err)
	}
	if got := dirNames(t, addonsDir); len(got) != 0 {
		t.Fatalf("after rejected install: addons = %v, want none", got)
	}
	if left := trashLeftovers(t); len(left) != 0 {
		t.Fatalf("after rejected install: trash holds %v, want nothing", left)
	}
}

// TestAtomicRejectsMismatchedToc checks staged validation: every staged
// directory must carry exactly one matching .toc (or a valid flavor set).
func TestAtomicRejectsMismatchedToc(t *testing.T) {
	in, _, flavor, root := testSetup(t)
	addonsDir := filepath.Join(root, "Interface", "AddOns")

	src := filepath.Join(t.TempDir(), "payload")
	if err := os.MkdirAll(filepath.Join(src, "DemoAddon"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "DemoAddon", "Other.toc"), []byte("## Interface: 110004\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := Request{
		Flavor:    flavor,
		Addon:     state.AddonState{ID: "DemoAddon", Strategy: state.StrategyRelease},
		SourceDir: src,
	}
	err := in.Install(req)
	if err == nil {
		t.Fatal("install with a mismatched .toc: succeeded, want rejection")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("install with a mismatched .toc: error %q does not explain the mismatch", err)
	}
	if got := dirNames(t, addonsDir); len(got) != 0 {
		t.Fatalf("after rejected install: addons = %v, want none", got)
	}
}

// TestAtomicReconcileCrashRollback simulates a crash mid-swap (phase
// "swapping"): the next run's reconciliation must restore the exact prior
// tree and clean the transaction up.
func TestAtomicReconcileCrashRollback(t *testing.T) {
	_, store, flavor, root := testSetup(t)
	addonsDir := filepath.Join(root, "Interface", "AddOns")

	// Prior tree: a single installed directory.
	writeAddonDir(t, addonsDir, "Old", "prior")
	before := readTree(t, addonsDir)

	// The crashed transaction had already trashed "Old" — it lives in the
	// transaction trash now — and installed "New".
	trashRoot, err := paths.CacheSubdir("trash")
	if err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(filepath.Dir(addonsDir), ".bfctl-stage-crashed")
	txnDir := filepath.Join(trashRoot, "txn-crashed")
	if err := os.MkdirAll(txnDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(addonsDir, "Old"), filepath.Join(txnDir, "Old")); err != nil {
		t.Fatal(err)
	}
	writeAddonDir(t, addonsDir, "New", "crashed")
	j := journal{
		TxnID:     "txn-crashed",
		AddonID:   "DemoAddon",
		Flavor:    string(flavor),
		Phase:     phaseSwapping,
		AddonsDir: addonsDir,
		StageDir:  stageDir,
		Trashed:   []string{"Old"},
		Installed: []string{"New"},
	}
	data, err := json.Marshal(&j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(txnDir, journalName), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := reconcile(trashRoot, store); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if after := readTree(t, addonsDir); !reflect.DeepEqual(after, before) {
		t.Fatalf("after reconcile: tree = %v, want exact prior tree %v", after, before)
	}
	if left := dirNames(t, trashRoot); len(left) != 0 {
		t.Fatalf("after reconcile: trash holds %v, want nothing", left)
	}
	if _, err := os.Lstat(stageDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after reconcile: staging dir still present (err = %v)", err)
	}
}

// TestAtomicReconcileCommitCompletes simulates a crash after the state file
// was written (phase "committing" with a matching state): reconciliation must
// complete the transaction — purge the trash — and keep the new install.
func TestAtomicReconcileCommitCompletes(t *testing.T) {
	_, store, flavor, root := testSetup(t)
	addonsDir := filepath.Join(root, "Interface", "AddOns")

	writeAddonDir(t, addonsDir, "New", "committed")

	// The state file already carries the commit's owned directories.
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	st.Clients[flavor].Addons["DemoAddon"] = &state.AddonState{
		ID:        "DemoAddon",
		OwnedDirs: []string{"New"},
		Strategy:  state.StrategyRelease,
	}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}

	// The crash left the trashed predecessor behind in the trash root.
	trashRoot, err := paths.CacheSubdir("trash")
	if err != nil {
		t.Fatal(err)
	}
	txnDir := filepath.Join(trashRoot, "txn-crashed")
	if err := os.MkdirAll(txnDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAddonDir(t, txnDir, "Old", "prior")
	j := journal{
		TxnID:       "txn-crashed",
		AddonID:     "DemoAddon",
		Flavor:      string(flavor),
		Phase:       phaseCommitting,
		AddonsDir:   addonsDir,
		Trashed:     []string{"Old"},
		Installed:   []string{"New"},
		ExpectOwned: []string{"New"},
	}
	data, err := json.Marshal(&j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(txnDir, journalName), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := reconcile(trashRoot, store); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := dirNames(t, addonsDir); !slices.Equal(got, []string{"New"}) {
		t.Fatalf("after reconcile: addons = %v, want the committed install [New]", got)
	}
	if left := dirNames(t, trashRoot); len(left) != 0 {
		t.Fatalf("after reconcile: trash holds %v, want nothing", left)
	}
}
