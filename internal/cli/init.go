package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/nvrmndmnm/blingtronsfavor/internal/paths"
	"github.com/nvrmndmnm/blingtronsfavor/internal/service"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
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
			return failHint("init", fmt.Errorf("init client: %w", err), ExitError, `pass the World of Warcraft flavor directory (e.g. .../World of Warcraft/_forever_)`)
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
		return failHint("init", fmt.Errorf("resolve config dir: %w", err), ExitError, hintHome)
	}
	if err := paths.EnsureLayout(); err != nil {
		return failHint("init", fmt.Errorf("create bfctl directories: %w", err), ExitError, `check that ~/.config and ~/.cache are writable`)
	}
	statePath, err := paths.StateFile()
	if err != nil {
		return failHint("init", fmt.Errorf("resolve state file: %w", err), ExitError, hintHome)
	}
	store := state.Open(statePath)
	_, err = os.Stat(statePath)
	switch {
	case err == nil:
		fmt.Printf("bfctl already initialised in %s\n", cfgDir)
		return ExitOK
	case !errors.Is(err, os.ErrNotExist):
		return fail("init", fmt.Errorf("inspect state file: %w", err), ExitError)
	}
	if err := store.Save(state.New()); err != nil {
		return failHint("init", fmt.Errorf("write state file: %w", err), ExitError, `check that the bfctl config directory is writable`)
	}
	fmt.Printf("initialised bfctl in %s\n", cfgDir)
	return ExitOK
}
