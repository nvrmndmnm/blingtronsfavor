package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
)

// emptyState is the starter state file written by "bfctl init".
const emptyState = `{
  "addons": {}
}
`

// bootstrapStateFormat is the tiny state file written by "bfctl init
// --path". The
// full structure lands in a later milestone and replaces this.
const bootstrapStateFormat = `{
  "root": %q,
  "flavorDir": %q,
  "addons": {}
}
`

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
	if *flavorDir != "" {
		install, err := client.Detect(*flavorDir)
		if err != nil {
			slog.Error("detect client", "error", err)
			return ExitError
		}
		fmt.Printf("detected %s\n", install.Badge())
		state := fmt.Sprintf(bootstrapStateFormat, install.Root, install.FlavorDir)
		if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
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
	if err := os.WriteFile(statePath, []byte(emptyState), 0o600); err != nil {
		slog.Error("write state file", "error", err)
		return ExitError
	}
	fmt.Printf("initialised bfctl in %s\n", cfgDir)
	return ExitOK
}
