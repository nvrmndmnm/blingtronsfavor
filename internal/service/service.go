// Package service holds the service functions behind both the bfctl command
// line and the TUI: status assembly, syncing, and addon management. The UI
// layers render what this package returns and never contain business logic
// of their own — every action goes through these functions.
package service

import (
	"time"

	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/state"
)

// lockTimeout is how long a state write waits for the state lock before
// giving up with state.ErrLocked.
const lockTimeout = 5 * time.Second

// ToolVersion is the bfctl version stamped into the GitHub user agent. The
// CLI sets it once at startup from its build metadata.
var ToolVersion string

// openStore opens the default state store.
func openStore() (*state.Store, error) {
	path, err := paths.StateFile()
	if err != nil {
		return nil, err
	}
	return state.Open(path), nil
}

// LoadState loads the current state from the default state file.
func LoadState() (*state.State, error) {
	store, err := openStore()
	if err != nil {
		return nil, err
	}
	return store.Load()
}

// StrategyLabel renders an install strategy the way the TUI strategy column
// shows it: GH-Release, Git-Pkgmeta, Direct or Manual.
func StrategyLabel(s state.Strategy) string {
	switch s {
	case state.StrategyRelease:
		return "GH-Release"
	case state.StrategySourcePkgmeta:
		return "Git-Pkgmeta"
	case state.StrategyDirect:
		return "Direct"
	case state.StrategyManual:
		return "Manual"
	case "":
		return "-"
	default:
		return string(s)
	}
}

// versionOr renders the installed version, falling back to the installed
// commit and finally "-".
func versionOr(version, commit string) string {
	if version != "" {
		return version
	}
	if commit != "" {
		return commit
	}
	return "-"
}
