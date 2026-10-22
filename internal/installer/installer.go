package installer

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/state"
)

// lockTimeout is how long an install or remove waits for the state lock
// before giving up with state.ErrLocked.
const lockTimeout = 5 * time.Second

// Request describes one install: the addon record to write to the state file
// and the unpacked payload to install. The payload's top-level directories
// are the addon's folders; each must carry exactly one matching .toc (or a
// valid flavor set).
type Request struct {
	Flavor    client.Flavor
	Addon     state.AddonState
	SourceDir string
	Force     bool // take over colliding directories that are not ours
}

// Installer installs and removes addon directory trees atomically. The whole
// swap runs under the state lock, so concurrent bfctl processes serialize
// both their state writes and their directory moves.
type Installer struct {
	Store *state.Store

	// failBeforeSwap is a test hook: when set, Install aborts with this
	// error after the trash moves and before any staged directory is
	// renamed into Interface/AddOns, exercising the rollback path.
	failBeforeSwap error
}

// Install installs the request's payload into the client's Interface/AddOns
// and records it in the state file, atomically: stage and validate the
// payload, move the addon's old directories into the transaction trash,
// rename the staged directories into place, write state.json and only then
// purge the trash. Any failure rolls the tree back to its exact prior
// contents.
func (in *Installer) Install(req Request) error {
	if req.Addon.ID == "" {
		return errors.New("install: addon ID is required")
	}
	if req.SourceDir == "" {
		return errors.New("install: source directory is required")
	}
	if in.Store == nil {
		return errors.New("install: no state store configured")
	}
	trashRoot, err := paths.CacheSubdir("trash")
	if err != nil {
		return err
	}
	lock, err := in.Store.Lock(lockTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	if err := reconcile(trashRoot, in.Store); err != nil {
		return err
	}

	st, err := in.Store.Load()
	if err != nil {
		return err
	}
	cs := st.Client(req.Flavor)
	if cs == nil {
		return fmt.Errorf("install %s: no client configured for flavor %s: run \"bfctl init\" first", req.Addon.ID, req.Flavor)
	}
	if cs.Addons == nil {
		cs.Addons = make(map[string]*state.AddonState)
	}
	addonsDir := cs.AddonsDir()
	if err := os.MkdirAll(addonsDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", addonsDir, err)
	}

	stg, err := newStage(addonsDir)
	if err != nil {
		return err
	}
	defer func() { _ = stg.cleanup() }()

	names, err := stg.populate(req.SourceDir)
	if err != nil {
		return err
	}
	if err := validateStaged(stg.dir, names); err != nil {
		return err
	}

	var prevDirs []string
	if prev := cs.Addons[req.Addon.ID]; prev != nil {
		prevDirs = prev.OwnedDirs
	}
	trashSet, err := planSwap(cs, req, names, addonsDir, prevDirs)
	if err != nil {
		return err
	}

	t, err := begin(trashRoot, journal{
		AddonID:   req.Addon.ID,
		Flavor:    string(req.Flavor),
		Phase:     phaseOpen,
		AddonsDir: addonsDir,
		StageDir:  stg.dir,
	})
	if err != nil {
		return err
	}
	if err := in.swap(t, trashSet, names); err != nil {
		return errors.Join(err, abort(t))
	}

	rec := req.Addon
	rec.OwnedDirs = names
	rec.UpdatedAt = time.Now().UTC()
	cs.Addons[req.Addon.ID] = &rec

	if err := t.commit(false, names); err != nil {
		return errors.Join(err, abort(t))
	}
	if err := in.Store.Save(st); err != nil {
		return errors.Join(err, abort(t))
	}
	// Commit point: the tree and the state file both describe the new
	// install now; only the trash is left to purge.
	if err := t.discard(); err != nil {
		slog.Warn("installed, but could not clean up the transaction trash; it will be reconciled on the next run",
			"dir", t.dir, "error", err)
	}
	return nil
}

// Remove trashes the addon's owned directories and drops its state entry,
// atomically and with the same rollback guarantees as Install.
func (in *Installer) Remove(flavor client.Flavor, id string) error {
	if id == "" {
		return errors.New("remove: addon ID is required")
	}
	if in.Store == nil {
		return errors.New("remove: no state store configured")
	}
	trashRoot, err := paths.CacheSubdir("trash")
	if err != nil {
		return err
	}
	lock, err := in.Store.Lock(lockTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	if err := reconcile(trashRoot, in.Store); err != nil {
		return err
	}

	st, err := in.Store.Load()
	if err != nil {
		return err
	}
	cs := st.Client(flavor)
	if cs == nil {
		return fmt.Errorf("remove %s: no client configured for flavor %s", id, flavor)
	}
	a := cs.Addons[id]
	if a == nil {
		return fmt.Errorf("remove %s: not managed by bfctl for flavor %s (see \"bfctl status\")", id, flavor)
	}

	addonsDir := cs.AddonsDir()
	if _, err := os.Lstat(addonsDir); errors.Is(err, os.ErrNotExist) {
		// The whole addon directory is gone already; only the state
		// entry is left to drop.
		delete(cs.Addons, id)
		return in.Store.Save(st)
	}

	t, err := begin(trashRoot, journal{
		AddonID:   id,
		Flavor:    string(flavor),
		Phase:     phaseOpen,
		AddonsDir: addonsDir,
	})
	if err != nil {
		return err
	}
	for _, name := range a.OwnedDirs {
		if _, err := os.Lstat(filepath.Join(addonsDir, name)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // already gone; the state entry just goes away
			}
			return errors.Join(err, abort(t))
		}
		if err := t.trash(name); err != nil {
			return errors.Join(err, abort(t))
		}
	}
	if err := t.commit(true, nil); err != nil {
		return errors.Join(err, abort(t))
	}
	delete(cs.Addons, id)
	if err := in.Store.Save(st); err != nil {
		return errors.Join(err, abort(t))
	}
	// Commit point: tree and state file agree again.
	if err := t.discard(); err != nil {
		slog.Warn("removed, but could not clean up the transaction trash; it will be reconciled on the next run",
			"dir", t.dir, "error", err)
	}
	return nil
}

// swap moves the old directories into the transaction trash first and only
// then renames the staged ones into place. The trash-first order is what
// makes rollback (and case-only renames on ext4/btrfs) exact: by the time a
// staged directory lands, its predecessor is already out of the way and
// recoverable.
func (in *Installer) swap(t *txn, trashSet, names []string) error {
	for _, name := range trashSet {
		if err := t.trash(name); err != nil {
			return err
		}
	}
	if in.failBeforeSwap != nil {
		return fmt.Errorf("simulated failure before swap: %w", in.failBeforeSwap)
	}
	for _, name := range names {
		if err := t.install(name); err != nil {
			return err
		}
	}
	return nil
}

// planSwap computes which existing directories must move to the trash before
// the staged payload can be renamed in: everything the addon owned before,
// plus — only with Request.Force — foreign directories the payload collides
// with. Without Force a collision with a directory this addon does not own is
// refused: bfctl never silently takes over a foreign folder. Directory names
// are compared case-insensitively so upgrades that only change letter case
// are recognised as this addon's own directories.
func planSwap(cs *state.ClientState, req Request, names []string, addonsDir string, prevDirs []string) ([]string, error) {
	entries, err := os.ReadDir(addonsDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", addonsDir, err)
	}
	onDisk := make(map[string]string) // lowercase name -> name on disk
	for _, entry := range entries {
		if entry.IsDir() {
			onDisk[strings.ToLower(entry.Name())] = entry.Name()
		}
	}
	ownedByAddon := func(name string) bool {
		return slices.ContainsFunc(prevDirs, func(d string) bool { return strings.EqualFold(d, name) })
	}
	trash := make(map[string]struct{})
	for _, d := range prevDirs {
		if actual, ok := onDisk[strings.ToLower(d)]; ok {
			trash[actual] = struct{}{}
		}
	}
	for _, name := range names {
		actual, ok := onDisk[strings.ToLower(name)]
		if !ok || ownedByAddon(actual) {
			continue
		}
		owner := otherOwner(cs, req.Addon.ID, actual)
		if !req.Force {
			who := "not owned by this addon"
			if owner != "" {
				who = fmt.Sprintf("owned by addon %s", owner)
			}
			return nil, fmt.Errorf("install %s: %s already exists in %s and is %s: pass --force to take it over",
				req.Addon.ID, actual, addonsDir, who)
		}
		if owner != "" {
			// --force takes the folder over; drop it from the previous
			// owner's state so its owned_dirs stay truthful.
			dropOwnedDir(cs.Addons[owner], actual)
		}
		trash[actual] = struct{}{}
	}
	sorted := make([]string, 0, len(trash))
	for name := range trash {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	return sorted, nil
}

// otherOwner returns the ID of the addon other than self that records name
// among its owned directories, or "" when the directory is unmanaged.
func otherOwner(cs *state.ClientState, self, name string) string {
	for id, a := range cs.Addons {
		if id == self {
			continue
		}
		if slices.ContainsFunc(a.OwnedDirs, func(d string) bool { return strings.EqualFold(d, name) }) {
			return id
		}
	}
	return ""
}

// dropOwnedDir removes name from a's owned directories.
func dropOwnedDir(a *state.AddonState, name string) {
	if a == nil {
		return
	}
	kept := make([]string, 0, len(a.OwnedDirs))
	for _, d := range a.OwnedDirs {
		if !strings.EqualFold(d, name) {
			kept = append(kept, d)
		}
	}
	a.OwnedDirs = kept
}
