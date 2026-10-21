package cli

import "fmt"

func runVersion(args []string, bi BuildInfo) int {
	fs := subcommandFlagSet("version")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	fmt.Printf("bfctl %s (commit %s, built %s)\n", bi.Version, bi.Commit, bi.Date)
	return ExitOK
}
