package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/paths"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
)

// The fixture pair client.Detect reads: a forever client (Forever 1.60.1,
// interface 16001), mirroring internal/client's testdata fixtures.
const (
	initBuildInfo  = "Branch!STRING:64|Active!DEC:1|Build Key!STRING:32|Version!STRING:32|Product!STRING:64\nus|1|b1|1.60.0.58712|wowf\nus|1|b2|1.60.1.58900|wowf\n"
	initFlavorInfo = "Product Flavor!STRING:0\nwowf\n"
)

// testInitEnv points bfctl's config and cache at fresh temp directories,
// creates the layout in them and fabricates one detectable flavor
// directory, returning its path. The layout is created here because the
// state store's Save writes only the file (its directory must already
// exist), and some fixtures write state ahead of Init — which is what
// normally creates the layout.
func testInitEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	if err := paths.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "World of Warcraft")
	flavorDir := filepath.Join(root, "_forever_")
	if err := os.MkdirAll(flavorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".build.info"), []byte(initBuildInfo), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flavorDir, ".flavor.info"), []byte(initFlavorInfo), 0o600); err != nil {
		t.Fatal(err)
	}
	return flavorDir
}

func TestInitRecordsDetectedClient(t *testing.T) {
	flavorDir := testInitEnv(t)

	outcome, err := Init(flavorDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if outcome.Install.Flavor != client.FlavorForever {
		t.Fatalf("flavor = %q, want forever", outcome.Install.Flavor)
	}
	if outcome.Install.Version.Short() != "1.60.1" {
		t.Fatalf("version = %q, want 1.60.1", outcome.Install.Version.Short())
	}
	if outcome.Replaced != "" {
		t.Fatalf("Replaced = %q, want none", outcome.Replaced)
	}
	statePath, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	if outcome.StatePath != statePath {
		t.Fatalf("StatePath = %q, want %q", outcome.StatePath, statePath)
	}

	st, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	cs := st.Client(client.FlavorForever)
	if cs == nil {
		t.Fatal("the state records no forever client")
	}
	if cs.Root != outcome.Install.FlavorDir {
		t.Fatalf("recorded root = %q, want %q", cs.Root, outcome.Install.FlavorDir)
	}
	if cs.Interface != 16001 {
		t.Fatalf("interface = %d, want 16001", cs.Interface)
	}
}

func TestInitReportsReplacedRoot(t *testing.T) {
	flavorDir := testInitEnv(t)

	// Record the same flavor under another root first (v1 keeps one root
	// per flavor): the init must overwrite it and report the old root.
	st, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	oldRoot := filepath.Join(t.TempDir(), "old", "_forever_")
	st.Clients[client.FlavorForever] = &state.ClientState{
		Flavor: client.FlavorForever,
		Root:   oldRoot,
		Addons: map[string]*state.AddonState{},
	}
	store, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}

	outcome, err := Init(flavorDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if outcome.Replaced != oldRoot {
		t.Fatalf("Replaced = %q, want %q", outcome.Replaced, oldRoot)
	}
}

func TestInitRejectsNonInstall(t *testing.T) {
	testInitEnv(t)

	if _, err := Init(t.TempDir()); err == nil {
		t.Fatal("Init accepted a directory that is not a WoW flavor directory")
	}
	st, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(st.Clients) != 0 {
		t.Fatalf("state records %+v, want no clients", st.Clients)
	}
}

func TestInitReplacesUnreadableState(t *testing.T) {
	flavorDir := testInitEnv(t)

	statePath, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	outcome, err := Init(flavorDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if outcome.Install.Flavor != client.FlavorForever {
		t.Fatalf("flavor = %q, want forever", outcome.Install.Flavor)
	}
	st, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.Client(client.FlavorForever) == nil {
		t.Fatal("the unreadable state was not replaced by the detected client")
	}
}
