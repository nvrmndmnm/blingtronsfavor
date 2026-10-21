package cli

// runRemove is a skeleton: removing addons from the manifest lands with the
// manifest milestone. It only validates its flags for now.
func runRemove(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("remove")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	return notImplemented("remove")
}
