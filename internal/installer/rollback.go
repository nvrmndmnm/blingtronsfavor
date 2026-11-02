package installer

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
)

// moveDir renames src to dst. Both must live on the same filesystem (the
// callers guarantee this up front): rename(2) is what makes the swap atomic.
func moveDir(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return fmt.Errorf("move %s to %s: %w — keep Interface/AddOns and the bfctl cache on the same filesystem", src, dst, err)
		}
		return fmt.Errorf("move %s to %s: %w", src, dst, err)
	}
	return nil
}

// trash moves one directory out of Interface/AddOns into the transaction's
// trash directory. The journal intent is persisted before the move, so a
// crash between the two is reconciled by looking at what the trash directory
// actually holds.
func (t *txn) trash(name string) error {
	t.j.Trashed = append(t.j.Trashed, name)
	if err := t.save(); err != nil {
		return err
	}
	return moveDir(filepath.Join(t.j.AddonsDir, name), filepath.Join(t.dir, name))
}

// install renames one staged directory into Interface/AddOns. As with trash,
// the journal intent is persisted first.
func (t *txn) install(name string) error {
	t.j.Installed = append(t.j.Installed, name)
	if err := t.save(); err != nil {
		return err
	}
	return moveDir(filepath.Join(t.j.StageDir, name), filepath.Join(t.j.AddonsDir, name))
}

// rollback undoes every move the transaction made, restoring the exact prior
// tree: directories the transaction installed go back to the stage, and
// everything the trash directory holds goes back into Interface/AddOns.
// The trash directory is inspected directly (not just the journal) so moves
// that crashed halfway are still undone.
func (t *txn) rollback() error {
	var errs []error
	// Installed directories go back to the stage first, so restoring the
	// trashed ones can never collide with them.
	for _, name := range t.j.Installed {
		src := filepath.Join(t.j.AddonsDir, name)
		if _, err := os.Lstat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			errs = append(errs, err)
			continue
		}
		if err := os.MkdirAll(t.j.StageDir, 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := moveDir(src, filepath.Join(t.j.StageDir, name)); err != nil {
			errs = append(errs, err)
		}
	}
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue // the journal and its temp file
		}
		dst := filepath.Join(t.j.AddonsDir, entry.Name())
		if _, err := os.Lstat(dst); err == nil {
			errs = append(errs, fmt.Errorf("rollback: %s exists again, keeping %s in the transaction trash for manual recovery",
				dst, filepath.Join(t.dir, entry.Name())))
			continue
		}
		if err := moveDir(filepath.Join(t.dir, entry.Name()), dst); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// abort rolls a failed transaction back and discards it. Both the original
// and any rollback error are reported.
func abort(t *txn) error {
	if err := t.rollback(); err != nil {
		return errors.Join(
			fmt.Errorf("rollback of transaction %s failed (left in %s for manual recovery)", t.j.TxnID, t.dir),
			err)
	}
	if err := t.discard(); err != nil {
		return err
	}
	return nil
}

// reconcile resolves transactions left in the trash root by a crashed run:
// a transaction in phaseOpen is discarded (nothing was moved yet), one in
// phaseSwapping is rolled back, and one in phaseCommitting is completed when
// the state file already matches its commit intent and rolled back
// otherwise. It runs under the state lock, so it cannot race a live
// transaction in another bfctl process.
func reconcile(trashRoot string, store *state.Store) error {
	entries, err := os.ReadDir(trashRoot)
	if err != nil {
		return fmt.Errorf("read trash %s: %w", trashRoot, err)
	}
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(trashRoot, entry.Name())
		j, err := loadJournal(dir)
		if err != nil {
			slog.Warn("trash: skipping transaction without a readable journal; inspect it manually", "dir", dir, "error", err)
			continue
		}
		t := &txn{dir: dir, j: *j}
		if err := t.resolve(store); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// loadJournal reads one transaction's journal file.
func loadJournal(dir string) (*journal, error) {
	data, err := os.ReadFile(filepath.Join(dir, journalName))
	if err != nil {
		return nil, err
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, journalName), err)
	}
	return &j, nil
}

// resolve finishes or rolls back one leftover transaction according to its
// journal phase.
func (t *txn) resolve(store *state.Store) error {
	switch t.j.Phase {
	case phaseOpen:
		// Nothing had been moved yet; the stage is a throwaway copy.
		return t.discard()
	case phaseSwapping:
		return abort(t)
	case phaseCommitting:
		if journalMatchesState(store, &t.j) {
			// The state file was written: the install is done and only
			// the trash is left to purge.
			return t.discard()
		}
		return abort(t)
	default:
		slog.Warn("trash: skipping transaction with unknown phase; inspect it manually",
			"dir", t.dir, "phase", t.j.Phase)
		return nil
	}
}
