package state

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/itp5b5/blessforge/internal/client"
)

// CurrentSchema is the state schema version this build writes. Loading a
// state file with a lower version runs the migrations below; a higher version
// is refused with a clear error instead of being rewritten into data loss.
const CurrentSchema = 1

// bootstrapState is the tiny state file "bfctl init" wrote before schema 1
// existed: {root, flavorDir, addons}. Its addon map was never populated by
// any writer, so only the recorded client carries over.
type bootstrapState struct {
	Root      string                     `json:"root"`
	FlavorDir string                     `json:"flavorDir"`
	Addons    map[string]json.RawMessage `json:"addons"`
}

// decode parses one state file payload and migrates it to CurrentSchema.
// Unknown JSON fields are ignored on purpose — a newer bfctl may add fields
// without bumping the schema, and old builds must keep reading those files.
func decode(data []byte) (*State, error) {
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if st.Schema == 0 && st.Clients == nil {
		var boot bootstrapState
		if err := json.Unmarshal(data, &boot); err != nil {
			return nil, fmt.Errorf("parse state: %w", err)
		}
		if boot.FlavorDir != "" || boot.Root != "" || len(boot.Addons) > 0 {
			st = fromBootstrap(boot)
		}
	}
	if err := migrate(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

// migrate upgrades st in place from its recorded schema version to
// CurrentSchema.
func migrate(st *State) error {
	if st.Schema > CurrentSchema {
		return fmt.Errorf("state schema %d was written by a newer bfctl (this build understands up to %d): upgrade bfctl to read it",
			st.Schema, CurrentSchema)
	}
	for st.Schema < CurrentSchema {
		if err := migrateOne(st); err != nil {
			return err
		}
	}
	return nil
}

// migrateOne upgrades st by exactly one schema version.
func migrateOne(st *State) error {
	switch st.Schema {
	case 0:
		// Schema 0 is the bootstrap file; decode already converted its
		// client record, so only the version stamp is left to bump.
		st.Schema = 1
		if st.Clients == nil {
			st.Clients = make(map[client.Flavor]*ClientState)
		}
	default:
		return fmt.Errorf("no migration from state schema %d", st.Schema)
	}
	return nil
}

// fromBootstrap converts a schema 0 bootstrap file into a schema 1 state: the
// single recorded flavor dir becomes the client for its flavor. The client is
// re-detected when the install is still readable so the flavor and interface
// come from the real files; when it is gone (moved, unmounted drive) the
// flavor is inferred from the directory name and the interface stays unknown.
func fromBootstrap(boot bootstrapState) State {
	st := *New()
	if boot.FlavorDir == "" {
		return st
	}
	cs := &ClientState{
		Root:   boot.FlavorDir,
		Addons: make(map[string]*AddonState),
	}
	if install, err := client.Detect(boot.FlavorDir); err == nil {
		cs.Flavor, cs.Interface, cs.Root = install.Flavor, install.Interface, install.FlavorDir
	} else {
		cs.Flavor = flavorFromDirName(boot.FlavorDir)
	}
	st.Clients[cs.Flavor] = cs
	return st
}

// flavorFromDirName infers the client flavor from a WoW flavor directory
// name (e.g. "_forever_"). It mirrors the directory matching inside
// client.flavorFromProduct; unknown names map to mainline.
func flavorFromDirName(dir string) client.Flavor {
	name := strings.ToLower(filepath.Base(dir))
	switch {
	case strings.Contains(name, "forever"), strings.Contains(name, "camelot"):
		return client.FlavorForever
	case strings.Contains(name, "mists"), strings.Contains(name, "progression"):
		return client.FlavorMists
	case strings.Contains(name, "classic"), strings.Contains(name, "vanilla"), strings.Contains(name, "era"):
		return client.FlavorClassic
	default:
		return client.FlavorMainline
	}
}
