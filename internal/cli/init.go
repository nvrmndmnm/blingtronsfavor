package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/state"
)

func runInit(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("init")
	flavorDir := fs.String("path", "", "path to the World of Warcraft flavor directory, e.g. .../World of Warcraft/_forever_")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	cfgDir, err := paths.ConfigDir()
	if err != nil {
		slog.Error("resolve config dir", "error", err)
		return ExitError
	}
	if err := paths.EnsureLayout(); err != nil {
		slog.Error("create bfctl directories", "error", err)
		return ExitError
	}
	statePath, err := paths.StateFile()
	if err != nil {
		slog.Error("resolve state file", "error", err)
		return ExitError
	}
	store := state.Open(statePath)
	if *flavorDir != "" {
		install, err := client.Detect(expandPathFlag("path", *flavorDir))
		if err != nil {
			slog.Error("detect client", "error", err)
			return ExitError
		}
		fmt.Printf("detected %s\n", install.Badge())
		st, err := store.Load()
		if err != nil {
			// An unreadable state file is replaced by the freshly detected
			// client rather than blocking the bootstrap.
			slog.Warn("replace unreadable state", "path", statePath, "error", err)
			st = state.New()
		}
		// v1 keeps one WoW root per flavor (plan R10): a second root for the
		// same flavor overwrites the recorded one, with a warning.
		if prev := st.Client(install.Flavor); prev != nil && prev.Root != install.FlavorDir {
			fmt.Printf("warning: overwriting recorded %s root %s with %s\n",
				install.Flavor.DisplayName(), prev.Root, install.FlavorDir)
		}
		st.Clients[install.Flavor] = &state.ClientState{
			Flavor:    install.Flavor,
			Root:      install.FlavorDir,
			Interface: install.Interface,
			Addons:    make(map[string]*state.AddonState),
		}
		if err := store.Save(st); err != nil {
			slog.Error("write state file", "error", err)
			return ExitError
		}
		fmt.Printf("recorded %s -> %s\n", install.FlavorDir, statePath)
		return ExitOK
	}
	_, err = os.Stat(statePath)
	switch {
	case err == nil:
		fmt.Printf("bfctl already initialised in %s\n", cfgDir)
		return ExitOK
	case !errors.Is(err, os.ErrNotExist):
		slog.Error("inspect state file", "error", err)
		return ExitError
	}
	if err := store.Save(state.New()); err != nil {
		slog.Error("write state file", "error", err)
		return ExitError
	}
	fmt.Printf("initialised bfctl in %s\n", cfgDir)
	return ExitOK
}
