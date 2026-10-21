package cli

// runAdopt is a skeleton: adopting existing addon directories lands with the
// adoption milestone. It only validates its flags for now.
func runAdopt(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("adopt")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	return notImplemented("adopt")
}
