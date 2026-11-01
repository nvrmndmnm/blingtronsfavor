package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
)

func runInit(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("init")
	flavorDir := fs.String("path", "", "path to the World of Warcraft flavor directory, e.g. .../World of Warcraft/_forever_")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if *flavorDir != "" {
		// The shared init path: service.Init is the same function the TUI's
		// guided picker runs behind the scenes (R5) — one implementation.
		outcome, err := service.Init(*flavorDir)
		if err != nil {
			slog.Error("init client", "error", err)
			return ExitError
		}
		fmt.Printf("detected %s\n", outcome.Install.Badge())
		if outcome.Replaced != "" {
			fmt.Printf("warning: overwriting recorded %s root %s with %s\n",
				outcome.Install.Flavor.DisplayName(), outcome.Replaced, outcome.Install.FlavorDir)
		}
		fmt.Printf("recorded %s -> %s\n", outcome.Install.FlavorDir, outcome.StatePath)
		return ExitOK
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
