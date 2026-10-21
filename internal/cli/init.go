package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/itp5b5/blessforge/internal/paths"
)

// emptyState is the starter state file written by "bfctl init".
const emptyState = `{
  "addons": {}
}
`

func runInit(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("init")
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
