package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
)

const (
	// tmpSuffix is appended to the state file name for the write-ahead
	// temporary file used by the atomic replace in Save.
	tmpSuffix = ".tmp"
	// lockSuffix is appended to the state file name for the lock file the
	// advisory flock in Lock is taken on.
	lockSuffix = ".lock"
	// lockPoll is how often Lock re-tries a contended flock.
	lockPoll = 20 * time.Millisecond
	// filePerm is used for every file the store creates (owner only).
	filePerm = 0o600
)

// ErrLocked is wrapped into the error Lock returns when another bfctl process
// holds the state lock for longer than the caller was willing to wait.
var ErrLocked = errors.New("state file is locked by another bfctl process")

// Store reads and writes one state file. A Store is safe for concurrent use
// for reads; writers must hold the lock from Lock while they load, mutate and
// save the state.
type Store struct {
	path string
}

// Open returns the store for the state file at path (typically
// paths.StateFile()). Save creates the file; its directory must already
// exist.
func Open(path string) *Store {
	return &Store{path: path}
}

// Path returns the state file path this store reads and writes.
func (s *Store) Path() string {
	return s.path
}

// Load reads and migrates the state file. A missing file yields an empty
// state. A leftover "<path>.tmp" from a crashed write is ignored on purpose:
// Save renames it into place only after it is complete and synced, so the
// state file itself is always either the previous or the new state, never a
// partial one.
func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state %s: %w", s.path, err)
	}
	st, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", s.path, err)
	}
	return st, nil
}

// Save writes st atomically: it marshals to "<path>.tmp" in the same
// directory, syncs that file to disk and renames it over the state file. Any
// failure leaves the previous state file completely intact. Save normalizes
// st.Schema to CurrentSchema before writing.
func (s *Store) Save(st *State) error {
	if st == nil {
		return errors.New("save state: nil state")
	}
	st.Schema = CurrentSchema
	if st.Clients == nil {
		st.Clients = make(map[client.Flavor]*ClientState)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')

	tmp := s.path + tmpSuffix
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, filePerm)
	if err != nil {
		return fmt.Errorf("write state %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write state %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync state %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close state %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace state %s: %w", s.path, err)
	}
	return nil
}

// Lock is a held exclusive lock on a store's state file.
type Lock struct {
	file *os.File
}

// Lock takes the exclusive advisory lock for the store's state file, waiting
// up to timeout for a concurrent bfctl process to release it. The lock is a
// flock(2) on "<path>.lock", so "bfctl sync" in a launch script and a TUI
// cannot race each other's state writes. When the lock is not free within
// timeout the returned error wraps ErrLocked. A zero timeout only tries once.
// The caller must call Unlock (typically with defer).
func (s *Store) Lock(timeout time.Duration) (*Lock, error) {
	lockPath := s.path + lockSuffix
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", lockPath, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{file: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", lockPath, err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%w (gave up waiting %s on %s)", ErrLocked, timeout, lockPath)
		}
		time.Sleep(lockPoll)
	}
}

// Unlock releases the lock and closes the underlying lock file.
func (l *Lock) Unlock() error {
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	if cerr := l.file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("unlock state: %w", err)
	}
	return nil
}
