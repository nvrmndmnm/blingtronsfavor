// Package state persists bfctl's addon state: which addons are installed for
// which client flavor, where they came from, and which directories they own
// inside Interface/AddOns. The state file is written atomically (temp file,
// fsync, rename) and guarded by an advisory file lock so concurrent bfctl
// processes — e.g. "bfctl sync" from a launch script racing a TUI — cannot
// interleave writes or corrupt it.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
)

// DefaultStalenessDays is the staleness window used when the user has not
// configured one: a repository whose newest work is more than this many days
// past its latest release is a source-build candidate (M4 staleness
// detector).
const DefaultStalenessDays = 14

// Config is the user configuration in ~/.config/bfctl/config.json. The file
// is optional; a missing or partial file falls back to the defaults below.
// Every knob is optional.
type Config struct {
	// StalenessDays overrides DefaultStalenessDays.
	StalenessDays int `json:"staleness_days,omitempty"`

	// Strategies pins addons to an install strategy, keyed by addon ID or
	// "owner/repo" slug. A pin of StrategyRelease silences the staleness
	// detector for that addon.
	Strategies map[string]Strategy `json:"strategies,omitempty"`
}

// Staleness returns the configured staleness window in days, defaulting to
// DefaultStalenessDays when unset or nonsensical.
func (c *Config) Staleness() time.Duration {
	days := DefaultStalenessDays
	if c != nil && c.StalenessDays > 0 {
		days = c.StalenessDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// PinnedStrategy returns the strategy addon id or slug is pinned to, if any.
// Both the addon ID and the "owner/repo" slug are accepted as lookup keys.
func (c *Config) PinnedStrategy(id, slug string) (Strategy, bool) {
	if c == nil {
		return "", false
	}
	for _, key := range []string{id, slug} {
		if key == "" {
			continue
		}
		if s, ok := c.Strategies[key]; ok && s != "" {
			return s, true
		}
	}
	return "", false
}

// LoadConfig reads ~/.config/bfctl/config.json. A missing file yields the
// defaults; a malformed file is an error.
func LoadConfig() (*Config, error) {
	dir, err := paths.ConfigDir()
	if err != nil {
		return nil, err
	}
	return LoadConfigFile(filepath.Join(dir, "config.json"))
}

// LoadConfigFile reads one config file, returning defaults when it does not
// exist.
func LoadConfigFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

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
	SourceWago   SourceKind = "wago"

	// SourceManual marks an addon installed by a foreign tool that bfctl
	// tracks but never touches: "bfctl sync" skips it until the user binds
	// it to a real source ("bfctl adopt --bind").
	SourceManual SourceKind = "manual"
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
	StrategyManual        Strategy = "manual"         // installed by a foreign tool, never touched by sync
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
	Changelog        string    `json:"changelog,omitempty"` // changelog of the installed version (inline text or URL); never a signed download link
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
