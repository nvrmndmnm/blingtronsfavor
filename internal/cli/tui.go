package cli

import (
	"fmt"
	"os"

	"github.com/nvrmndmnm/blingtronsfavor/internal/tui"
)

// runTUI implements "bfctl tui" (and the bare "bfctl" on a terminal): it
// opens the interactive addon manager. Every action goes through the shared
// service layer — the TUI only renders and dispatches.
func runTUI(args []string, bi BuildInfo) int {
	fs := subcommandFlagSet("tui")
	ascii := fs.Bool("ascii", false, "ASCII-only glyphs: degrade the status glyphs, punctuation and borders to plain ASCII")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "bfctl tui: unexpected argument %q — usage: bfctl tui [flags]\n", fs.Arg(0))
		subcommandUsage(fs)
		return ExitUsage
	}
	if err := tui.Run(tui.Options{Version: bi.Version, ASCII: *ascii}); err != nil {
		return fail("tui", err, ExitError)
	}
	return ExitOK
}
