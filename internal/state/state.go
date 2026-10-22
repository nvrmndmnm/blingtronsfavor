// Package state persists bfctl's addon state: which addons are installed for
// which client flavor, where they came from, and which directories they own
// inside Interface/AddOns. The state file is written atomically (temp file,
// fsync, rename) and guarded by an advisory file lock so concurrent bfctl
// processes — e.g. "bfctl sync" from a launch script racing a TUI — cannot
// interleave writes or corrupt it.
package state

import (
	"path/filepath"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
)

// State is the whole state file, one entry per client flavor.
type State struct {
	Schema  int                            `json:"schema"`
	Clients map[client.Flavor]*ClientState `json:"clients"`
}

// ClientState is the per-client-flavor part of the state. v1 keeps one WoW
// root per flavor ("bfctl init" overwrites with a warning).
type ClientState struct {
	Flavor    client.Flavor          `json:"flavor"`
	Root      string                 `json:"root"` // WoW flavor dir
	Interface int                    `json:"interface"`
	Addons    map[string]*AddonState `json:"addons"` // keyed by addon ID
}

// SourceKind identifies the upstream provider an addon is tracked against.
type SourceKind string

// Supported addon source kinds.
const (
	SourceGitHub SourceKind = "github"
	SourceGit    SourceKind = "git"
	SourceTukui  SourceKind = "tukui"
	SourceWoWI   SourceKind = "wowi"
)

// SourceRef points at an addon's upstream. URL is the clone/API URL and Slug
// is the provider-local identifier (e.g. "owner/repo" or the WoWI project ID).
type SourceRef struct {
	Kind SourceKind `json:"kind"`
	URL  string     `json:"url,omitempty"`
	Slug string     `json:"slug,omitempty"`
}

// Strategy is how an installed addon was (or will be) built and unpacked.
type Strategy string

// Supported install strategies.
const (
	StrategyRelease       Strategy = "release"        // a GitHub release asset
	StrategySourcePkgmeta Strategy = "source-pkgmeta" // local build from source with .pkgmeta
	StrategyDirect        Strategy = "direct"         // provider download or plain git
)

// AddonState tracks one managed addon inside one client flavor.
type AddonState struct {
	ID               string    `json:"id"`
	DisplayName      string    `json:"displayName,omitempty"`
	Source           SourceRef `json:"source"`
	Strategy         Strategy  `json:"strategy"`
	Tracking         string    `json:"tracking,omitempty"` // "stable" or a branch name
	InstalledVersion string    `json:"installedVersion,omitempty"`
	InstalledCommit  string    `json:"installedCommit,omitempty"`
	ETag             string    `json:"etag,omitempty"`      // resolver's last-seen upstream tag
	OwnedDirs        []string  `json:"ownedDirs,omitempty"` // dirs inside Interface/AddOns
	UpdatedAt        time.Time `json:"updatedAt"`
}

// New returns an empty state at the current schema version.
func New() *State {
	return &State{
		Schema:  CurrentSchema,
		Clients: make(map[client.Flavor]*ClientState),
	}
}

// Client returns the client state for flavor, or nil when the flavor is not
// recorded.
func (s *State) Client(flavor client.Flavor) *ClientState {
	if s == nil {
		return nil
	}
	return s.Clients[flavor]
}

// AddonsDir returns the Interface/AddOns directory the client's addons live
// in.
func (c *ClientState) AddonsDir() string {
	return filepath.Join(c.Root, "Interface", "AddOns")
}
