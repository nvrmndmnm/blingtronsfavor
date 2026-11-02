package state

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
)

// Env vars used by the helper process behind TestLockTwoProcesses.
const (
	flockHelperEnv = "BFCTL_STATE_FLOCK_HELPER"
	flockPathEnv   = "BFCTL_STATE_FLOCK_PATH"
	flockHoldEnv   = "BFCTL_STATE_FLOCK_HOLD"
)

// writeStateFile writes raw content to path.
func writeStateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// sampleState returns a state exercising every AddonState field.
func sampleState() *State {
	return &State{
		Schema: CurrentSchema,
		Clients: map[client.Flavor]*ClientState{
			client.FlavorForever: {
				Flavor:    client.FlavorForever,
				Root:      "/games/wow/_forever_",
				Interface: 16001,
				Addons: map[string]*AddonState{
					"deadly-boss-mods": {
						ID:          "deadly-boss-mods",
						DisplayName: "Deadly Boss Mods",
						Source: SourceRef{
							Kind: SourceGitHub,
							URL:  "https://github.com/DeadlyBossMods/DeadlyBossMods",
							Slug: "DeadlyBossMods/DeadlyBossMods",
						},
						Strategy:         StrategySourcePkgmeta,
						Tracking:         "stable",
						InstalledVersion: "11.2.0",
						InstalledCommit:  "abc1234def",
						ETag:             "v11.2.0",
						OwnedDirs:        []string{"DBM-Core", "DBM-GUI"},
						UpdatedAt:        time.Date(2026, 10, 22, 1, 2, 3, 0, time.UTC),
					},
				},
			},
			client.FlavorMists: {
				Flavor: client.FlavorMists,
				Root:   "/games/wow/_mists_",
				Addons: map[string]*AddonState{},
			},
		},
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "state.json"))
	want := sampleState()
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Schema != CurrentSchema {
		t.Fatalf("Schema = %d, want %d", got.Schema, CurrentSchema)
	}
	// Compare as marshaled JSON so time.Time internals (location pointers)
	// cannot make the comparison flaky.
	wantData, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	gotData, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	if !bytes.Equal(wantData, gotData) {
		t.Fatalf("round trip mismatch:\nwant %s\ngot  %s", wantData, gotData)
	}
}

func TestLoadMissingStateFile(t *testing.T) {
	got, err := Open(filepath.Join(t.TempDir(), "state.json")).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Schema != CurrentSchema {
		t.Fatalf("Schema = %d, want %d", got.Schema, CurrentSchema)
	}
	if len(got.Clients) != 0 {
		t.Fatalf("Clients = %+v, want empty", got.Clients)
	}
}

func TestLoadToleratesStaleTmpFile(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "state.json"))
	want := New()
	want.Clients[client.FlavorClassic] = &ClientState{
		Flavor:    client.FlavorClassic,
		Root:      "/games/wow/_classic_",
		Interface: 11509,
		Addons:    map[string]*AddonState{},
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	writeStateFile(t, store.Path()+tmpSuffix, `{ half written garbage`)
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load with stale %s: %v", tmpSuffix, err)
	}
	if got.Client(client.FlavorClassic) == nil {
		t.Fatalf("Clients = %+v, want a classic entry", got.Clients)
	}

	// A crash before the very first rename leaves only a stale tmp file;
	// loading must yield an empty state, not an error.
	onlyTmp := filepath.Join(t.TempDir(), "state.json")
	writeStateFile(t, onlyTmp+tmpSuffix, `{ half written garbage`)
	got, err = Open(onlyTmp).Load()
	if err != nil {
		t.Fatalf("Load without state file: %v", err)
	}
	if len(got.Clients) != 0 {
		t.Fatalf("Clients = %+v, want empty", got.Clients)
	}
}

func TestLoadIgnoresUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	writeStateFile(t, path, `{
  "schema": 1,
  "clients": {
    "forever": {
      "flavor": "forever",
      "root": "/games/wow/_forever_",
      "interface": 16001,
      "addons": {
        "dbm": {
          "id": "dbm",
          "source": {"kind": "github", "slug": "DeadlyBossMods/DeadlyBossMods", "addedLater": true},
          "strategy": "release",
          "installedVersion": "11.2.0"
        }
      },
      "addedLater": 42
    }
  },
  "hint": "unknown fields must not break loading"
}`)
	got, err := Open(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cs := got.Client(client.FlavorForever)
	if cs == nil || cs.Interface != 16001 {
		t.Fatalf("client = %+v, want forever with interface 16001", cs)
	}
	addon := cs.Addons["dbm"]
	if addon == nil || addon.Strategy != StrategyRelease || addon.Source.Kind != SourceGitHub {
		t.Fatalf("addon = %+v, want a github addon on the release strategy", addon)
	}
}

func TestLoadRejectsFutureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	writeStateFile(t, path, `{"schema": 99, "clients": {}}`)
	if _, err := Open(path).Load(); err == nil {
		t.Fatal("Load should refuse a state file from a newer bfctl")
	} else if !strings.Contains(err.Error(), "schema 99") {
		t.Fatalf("error %q should name the schema version found", err)
	}
}

func TestSaveFailureKeepsPreviousState(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "state.json"))
	before := New()
	before.Clients[client.FlavorMainline] = &ClientState{
		Flavor:    client.FlavorMainline,
		Root:      "/games/wow/_retail_",
		Interface: 120100,
		Addons:    map[string]*AddonState{},
	}
	if err := store.Save(before); err != nil {
		t.Fatalf("Save: %v", err)
	}
	original, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	// Inject a write failure: the temporary file path is occupied by a
	// directory, so Save cannot even start writing.
	if err := os.Mkdir(store.Path()+tmpSuffix, 0o700); err != nil {
		t.Fatalf("inject write failure: %v", err)
	}
	after := New()
	after.Clients[client.FlavorClassic] = &ClientState{
		Flavor: client.FlavorClassic,
		Root:   "/games/wow/_classic_",
		Addons: map[string]*AddonState{},
	}
	if err := store.Save(after); err == nil {
		t.Fatal("Save should fail when the temporary file cannot be written")
	}

	// The previous state must be intact, byte for byte.
	now, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !bytes.Equal(original, now) {
		t.Fatalf("state file changed after a failed Save:\nwas  %s\nnow  %s", original, now)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Client(client.FlavorMainline) == nil || got.Client(client.FlavorClassic) != nil {
		t.Fatalf("Clients = %+v, want only the previous mainline entry", got.Clients)
	}
}

func TestLoadBootstrapState(t *testing.T) {
	const (
		buildInfo = "Branch!STRING:64|Active!DEC:1|Build Key!STRING:32|Version!STRING:32|Product!STRING:64\n" +
			"us|1|b1|1.60.1.58900|wowf\n"
		flavorInfo = "Product Flavor!STRING:0\nwowf\n"
	)

	t.Run("re-detects the recorded install", func(t *testing.T) {
		root := t.TempDir()
		flavorDir := filepath.Join(root, "_forever_")
		if err := os.Mkdir(flavorDir, 0o700); err != nil {
			t.Fatalf("create flavor dir: %v", err)
		}
		writeStateFile(t, filepath.Join(root, ".build.info"), buildInfo)
		writeStateFile(t, filepath.Join(flavorDir, ".flavor.info"), flavorInfo)
		path := filepath.Join(t.TempDir(), "state.json")
		writeStateFile(t, path, fmt.Sprintf(`{"root": %q, "flavorDir": %q, "addons": {}}`, root, flavorDir))

		got, err := Open(path).Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Schema != CurrentSchema {
			t.Fatalf("Schema = %d, want %d", got.Schema, CurrentSchema)
		}
		cs := got.Client(client.FlavorForever)
		if cs == nil {
			t.Fatalf("Clients = %+v, want a forever entry", got.Clients)
		}
		if cs.Interface != 16001 {
			t.Fatalf("Interface = %d, want 16001", cs.Interface)
		}
		resolved, err := filepath.EvalSymlinks(flavorDir)
		if err != nil {
			t.Fatalf("resolve flavor dir: %v", err)
		}
		if cs.Root != resolved {
			t.Fatalf("Root = %q, want %q", cs.Root, resolved)
		}
	})

	t.Run("falls back to the directory name when the install is gone", func(t *testing.T) {
		flavorDir := filepath.Join(t.TempDir(), "_forever_")
		path := filepath.Join(t.TempDir(), "state.json")
		writeStateFile(t, path, fmt.Sprintf(`{"root": %q, "flavorDir": %q, "addons": {}}`, filepath.Dir(flavorDir), flavorDir))

		got, err := Open(path).Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		cs := got.Client(client.FlavorForever)
		if cs == nil {
			t.Fatalf("Clients = %+v, want a forever entry", got.Clients)
		}
		if cs.Root != flavorDir {
			t.Fatalf("Root = %q, want %q", cs.Root, flavorDir)
		}
		if cs.Interface != 0 {
			t.Fatalf("Interface = %d, want 0 (unknown)", cs.Interface)
		}
	})

	t.Run("empty bootstrap becomes an empty state", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.json")
		writeStateFile(t, path, `{"addons": {}}`)
		got, err := Open(path).Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Schema != CurrentSchema || len(got.Clients) != 0 {
			t.Fatalf("state = %+v, want an empty state at schema %d", got, CurrentSchema)
		}
	})
}

func TestLockBlocksSecondStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, err := Open(path).Lock(0)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	if _, err := Open(path).Lock(100 * time.Millisecond); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Lock error = %v, want ErrLocked", err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	third, err := Open(path).Lock(0)
	if err != nil {
		t.Fatalf("Lock after Unlock: %v", err)
	}
	if err := third.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}

// TestFlockHelperProcess is the child process body behind
// TestLockTwoProcesses. It only does something when the parent sets
// flockHelperEnv; the test binary re-executes itself to run it.
func TestFlockHelperProcess(t *testing.T) {
	if os.Getenv(flockHelperEnv) == "" {
		t.Skip("helper process for TestLockTwoProcesses")
	}
	hold, err := time.ParseDuration(os.Getenv(flockHoldEnv))
	if err != nil {
		t.Fatalf("parse %s: %v", flockHoldEnv, err)
	}
	lock, err := Open(os.Getenv(flockPathEnv)).Lock(5 * time.Second)
	if err != nil {
		t.Fatalf("helper Lock: %v", err)
	}
	fmt.Println("locked") // handshake with the parent
	time.Sleep(hold)
	if err := lock.Unlock(); err != nil {
		t.Fatalf("helper Unlock: %v", err)
	}
}

// TestLockTwoProcesses proves the lock excludes a second OS process: while a
// child holds it, Lock must fail with ErrLocked, and once the child is gone
// the lock must be free again.
func TestLockTwoProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestFlockHelperProcess$")
	cmd.Env = append(os.Environ(),
		flockHelperEnv+"=1",
		flockPathEnv+"="+path,
		flockHoldEnv+"=2s",
	)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	abort := func(reason string, args ...any) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf(reason, args...)
	}

	locked := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "locked" {
				close(locked)
				return
			}
		}
		close(exited) // helper produced no handshake before exiting
	}()
	select {
	case <-locked:
	case <-exited:
		abort("helper exited before acquiring the lock")
	case <-time.After(10 * time.Second):
		abort("timed out waiting for the helper process to take the lock")
	}

	// While the helper holds the lock, a second process must not get it.
	if _, err := Open(path).Lock(200 * time.Millisecond); !errors.Is(err, ErrLocked) {
		abort("Lock while the helper holds it: error = %v, want ErrLocked", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper process: %v", err)
	}
	lock, err := Open(path).Lock(time.Second)
	if err != nil {
		t.Fatalf("Lock after the helper exited: %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}
