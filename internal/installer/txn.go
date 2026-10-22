// Package installer installs and removes addon directory trees inside a
// client's Interface/AddOns atomically. Everything happens inside one
// transaction: the old directories are moved into a per-transaction trash
// directory first, the staged directories are renamed into place, the state
// file is written, and only then is the trash purged. Any failure rolls the
// tree back to its exact prior contents; a crashed run is reconciled from the
// transaction journal on the next run.
package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/state"
)

const (
	// journalName is the transaction journal file inside the transaction's
	// trash directory.
	journalName = "journal.json"
)

// Transaction phases, in the order they are written to the journal. The
// journal is persisted before each move, so a crash always leaves enough
// information to either roll back or complete the transaction.
const (
	// phaseOpen: the transaction exists but nothing has been moved yet.
	phaseOpen = "open"
	// phaseSwapping: directories are being moved into the trash and the
	// staged directories renamed into Interface/AddOns. The state file has
	// not been written yet, so a crash here rolls back.
	phaseSwapping = "swapping"
	// phaseCommitting: the swap is complete and the state file is about to
	// be (or has been) written. Reconciliation compares the state file with
	// ExpectGone/ExpectOwned to decide between completing and rolling back.
	phaseCommitting = "committing"
)

// journal is the on-disk record of one install or remove transaction. It
// lives in the transaction's trash directory so that the trash and its
// journal can only disappear together.
type journal struct {
	TxnID     string `json:"txnId"`
	AddonID   string `json:"addonId"`
	Flavor    string `json:"flavor"`
	Phase     string `json:"phase"`
	AddonsDir string `json:"addonsDir"`
	StageDir  string `json:"stageDir"`
	// Trashed are directory names moved out of AddonsDir into the
	// transaction trash directory.
	Trashed []string `json:"trashed,omitempty"`
	// Installed are directory names renamed from the stage into AddonsDir.
	Installed []string `json:"installed,omitempty"`
	// ExpectGone records that the commit removes the addon's state entry.
	ExpectGone bool `json:"expectGone,omitempty"`
	// ExpectOwned records the owned directories the commit writes to the
	// state entry. Reconciliation treats the transaction as committed only
	// when the state file already matches one of these expectations.
	ExpectOwned []string `json:"expectOwned,omitempty"`
}

// txn is one live or leftover transaction: a trash directory plus its
// journal.
type txn struct {
	dir string
	j   journal
}

// begin creates the transaction's trash directory under trashRoot and writes
// the initial journal. The trash directory must share a filesystem with
// addonsDir (j.AddonsDir) so that moving directories into it is a rename(2).
func begin(trashRoot string, j journal) (*txn, error) {
	if err := requireSameDev(j.AddonsDir, trashRoot); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(trashRoot, "txn-")
	if err != nil {
		return nil, fmt.Errorf("create transaction dir in %s: %w", trashRoot, err)
	}
	j.TxnID = filepath.Base(dir)
	t := &txn{dir: dir, j: j}
	if err := t.save(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return t, nil
}

// save writes the journal atomically (temp file, fsync, rename) so a crash
// never leaves a half-written journal behind.
func (t *txn) save() error {
	data, err := json.MarshalIndent(&t.j, "", "  ")
	if err != nil {
		return fmt.Errorf("encode transaction journal: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(t.dir, journalName)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write transaction journal %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write transaction journal %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync transaction journal %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close transaction journal %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace transaction journal %s: %w", path, err)
	}
	return nil
}

// commit records the transaction's commit intent and moves it into
// phaseCommitting. It must be called before the state file is saved: only
// then can reconciliation tell a crash before the save (roll back) from one
// after it (complete).
func (t *txn) commit(gone bool, owned []string) error {
	t.j.Phase = phaseCommitting
	t.j.ExpectGone = gone
	t.j.ExpectOwned = owned
	return t.save()
}

// discard removes the transaction's stage and trash directories after the
// transaction is resolved one way or the other.
func (t *txn) discard() error {
	if t.j.StageDir != "" {
		if err := os.RemoveAll(t.j.StageDir); err != nil {
			return fmt.Errorf("remove staging dir %s: %w", t.j.StageDir, err)
		}
	}
	if err := os.RemoveAll(t.dir); err != nil {
		return fmt.Errorf("purge transaction trash %s: %w", t.dir, err)
	}
	return nil
}

// journalMatchesState reports whether the state file already reflects the
// transaction's commit intent, i.e. the commit's Save succeeded before the
// crash. It is false for an unreadable state: rolling back is then the safe
// choice.
func journalMatchesState(store *state.Store, j *journal) bool {
	st, err := store.Load()
	if err != nil {
		return false
	}
	cs := st.Client(client.Flavor(j.Flavor))
	if j.ExpectGone {
		return cs == nil || cs.Addons[j.AddonID] == nil
	}
	if cs == nil {
		return false
	}
	a := cs.Addons[j.AddonID]
	return a != nil && sameNames(a.OwnedDirs, j.ExpectOwned)
}

// sameNames compares two directory name lists as sets, case-insensitively.
func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, n := range a {
		seen[strings.ToLower(n)] = struct{}{}
	}
	for _, n := range b {
		if _, ok := seen[strings.ToLower(n)]; !ok {
			return false
		}
	}
	return true
}
