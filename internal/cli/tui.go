package cli

import (
	"fmt"
	"os"

	"github.com/itp5b5/blessforge/internal/tui"
)

// runTUI implements "bfctl tui" (and the bare "bfctl" on a terminal): it
// opens the interactive addon manager. Every action goes through the shared
// service layer — the TUI only renders and dispatches.
func runTUI(args []string, bi BuildInfo) int {
	fs := subcommandFlagSet("tui")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "bfctl tui: unexpected argument %q\n", fs.Arg(0))
		subcommandUsage(fs)
		return ExitUsage
	}
	if err := tui.Run(tui.Options{Version: bi.Version}); err != nil {
		fmt.Fprintf(os.Stderr, "bfctl tui: %v\n", err)
		return ExitError
	}
	return ExitOK
}
