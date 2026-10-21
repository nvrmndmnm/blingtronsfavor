package cli

// runAdd is a skeleton: adding addon sources to the manifest lands with the
// manifest milestone. It only validates its flags for now.
func runAdd(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("add")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	return notImplemented("add")
}
