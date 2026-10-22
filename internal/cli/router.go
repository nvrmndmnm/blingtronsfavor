// Package cli implements the bfctl command-line interface: a small stdlib
// router plus one file per subcommand.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// BuildInfo carries version metadata injected at link time via -ldflags.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// Exit codes shared by every subcommand.
const (
	ExitOK      = 0 // success
	ExitError   = 1 // runtime failure
	ExitUsage   = 2 // usage error
	ExitPartial = 4 // partial failure: some units succeeded, some failed
)

// Logging flags shared by the global flag set and every subcommand flag set.
// Both bind to the same variables, so --verbose/--quiet may appear before or
// after the subcommand name.
var (
	logVerbose bool
	logQuiet   bool
)

type command struct {
	name  string
	short string
	run   func(args []string, bi BuildInfo) int
}

// commands returns the command table. It is a function rather than a package
// variable because the run handlers call back into the flag-set and usage
// helpers (runAdd → subcommandFlagSet → subcommandUsage → shortFor), which
// need this table: a package-level variable would form an initialization
// cycle, which Go rejects, while a reference cycle through functions is
// legal.
func commands() []command {
	return []command{
		{name: "init", short: "create the bfctl configuration and state", run: runInit},
		{name: "adopt", short: "adopt an existing addon into bfctl management", run: runAdopt},
		{name: "add", short: "add an addon to the manifest", run: runAdd},
		{name: "sync", short: "sync installed addons with the manifest", run: runSync},
		{name: "status", short: "show configured addons", run: runStatus},
		{name: "remove", short: "remove an addon from the manifest", run: runRemove},
		{name: "version", short: "print version information", run: runVersion},
	}
}

// Run executes the CLI with the given arguments and returns the process exit
// code.
func Run(args []string, bi BuildInfo) int {
	logVerbose, logQuiet = false, false

	gfs := flag.NewFlagSet("bfctl", flag.ContinueOnError)
	gfs.SetOutput(os.Stderr)
	registerLogFlags(gfs)
	gfs.Usage = func() { usage(gfs.Output()) }

	if err := gfs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK
		}
		return ExitUsage
	}
	configureLogging()

	rest := gfs.Args()
	if len(rest) == 0 {
		usage(os.Stderr)
		return ExitUsage
	}
	for _, c := range commands() {
		if c.name == rest[0] {
			return c.run(rest[1:], bi)
		}
	}
	fmt.Fprintf(os.Stderr, "bfctl: unknown command %q\n\n", rest[0])
	usage(os.Stderr)
	return ExitUsage
}

// notImplemented reports a subcommand that has a router entry and a skeleton
// handler but whose real behaviour lands in a later milestone.
func notImplemented(name string) int {
	fmt.Fprintf(os.Stderr, "bfctl: %s is not implemented yet\n", name)
	return ExitError
}

func registerLogFlags(fs *flag.FlagSet) {
	fs.BoolVar(&logVerbose, "verbose", false, "enable debug logging")
	fs.BoolVar(&logQuiet, "quiet", false, "only log errors")
}

// subcommandFlagSet builds a FlagSet for one subcommand with the shared
// logging flags and consistent --help output.
func subcommandFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	registerLogFlags(fs)
	fs.Usage = func() { subcommandUsage(fs) }
	return fs
}

// parseSubcommand parses args for fs and applies the logging configuration.
// When ok is false the caller must return code; -h/--help counts as success.
func parseSubcommand(fs *flag.FlagSet, args []string) (code int, ok bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK, false
		}
		return ExitUsage, false
	}
	configureLogging()
	return ExitOK, true
}

// configureLogging installs a text logger on stderr honouring the shared
// logging flags. Secrets and response bodies must never be logged.
func configureLogging() {
	level := slog.LevelInfo
	switch {
	case logVerbose:
		level = slog.LevelDebug
	case logQuiet:
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

func usage(w io.Writer) {
	fmt.Fprint(w, `bfctl - BlessForge addon manager

Usage:
  bfctl [global flags] <command> [flags]

Commands:
`)
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-8s %s\n", c.name, c.short)
	}
	fmt.Fprint(w, `
Global flags:
  --verbose  enable debug logging
  --quiet    only log errors

Run "bfctl <command> --help" for command flags.
`)
}

func subcommandUsage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintf(out, "bfctl %s - %s\n\nUsage:\n  bfctl %s [flags]\n\nFlags:\n",
		fs.Name(), shortFor(fs.Name()), fs.Name())
	fs.PrintDefaults()
}

func shortFor(name string) string {
	for _, c := range commands() {
		if c.name == name {
			return c.short
		}
	}
	return ""
}
