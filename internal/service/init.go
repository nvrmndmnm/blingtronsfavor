package service

import (
	"fmt"
	"log/slog"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/paths"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
)

// InitResult reports what Init recorded: the detected install, where the
// state landed and — when a second root replaced the recorded one for the
// same flavor (v1 keeps one WoW root per flavor) — the root it overwrote.
type InitResult struct {
	Install   client.ClientInstall
	StatePath string
	Replaced  string
}

// Init records a World of Warcraft flavor directory as bfctl's client. It
// is the single init path behind both "bfctl init --path" and the TUI's
// guided onboarding (R5): the picker performs the init behind the scenes
// through this function, never a second implementation. The path may
// carry a leading "~"; an unreadable state file is replaced by the freshly
// detected client rather than blocking the bootstrap.
func Init(flavorDir string) (InitResult, error) {
	var out InitResult
	if err := paths.EnsureLayout(); err != nil {
		return out, fmt.Errorf("create bfctl directories: %w", err)
	}
	statePath, err := paths.StateFile()
	if err != nil {
		return out, fmt.Errorf("resolve state file: %w", err)
	}
	out.StatePath = statePath
	expanded := flavorDir
	if resolved, err := paths.ExpandPath(flavorDir); err == nil {
		expanded = resolved
	} else {
		// Leave the path untouched and let Detect produce the concrete
		// diagnostic, the way expandPathFlag does for CLI flags.
		slog.Warn("expand ~ in path", "error", err)
	}
	install, err := client.Detect(expanded)
	if err != nil {
		return out, fmt.Errorf("detect client: %w", err)
	}
	out.Install = *install
	store := state.Open(statePath)
	st, err := store.Load()
	if err != nil {
		slog.Warn("replace unreadable state", "path", statePath, "error", err)
		st = state.New()
	}
	// v1 keeps one WoW root per flavor (plan R10): a second root for the
	// same flavor overwrites the recorded one, with a warning the caller
	// surfaces.
	if prev := st.Client(install.Flavor); prev != nil && prev.Root != install.FlavorDir {
		out.Replaced = prev.Root
	}
	st.Clients[install.Flavor] = &state.ClientState{
		Flavor:    install.Flavor,
		Root:      install.FlavorDir,
		Interface: install.Interface,
		Addons:    make(map[string]*state.AddonState),
	}
	if err := store.Save(st); err != nil {
		return out, fmt.Errorf("write state file: %w", err)
	}
	return out, nil
}
