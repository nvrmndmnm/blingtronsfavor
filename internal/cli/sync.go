package cli

// runSync is a skeleton: syncing installed addons with the manifest lands
// with the installer milestone. It only validates its flags for now.
func runSync(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("sync")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	return notImplemented("sync")
}
