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
	"strings"

	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/service"
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
		{name: "tui", short: "open the interactive addon manager", run: runTUI},
		{name: "version", short: "print version information", run: runVersion},
	}
}

// Run executes the CLI with the given arguments and returns the process exit
// code.
func Run(args []string, bi BuildInfo) int {
	logVerbose, logQuiet = false, false
	service.ToolVersion = bi.Version

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
		// A bare "bfctl" on a terminal opens the TUI; in scripts and
		// pipes it falls back to the usage text and exit code 2.
		if stdoutIsTTY() {
			return runTUI(nil, bi)
		}
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

// stdoutIsTTY reports whether standard output is a terminal, which is how a
// bare "bfctl" decides between the TUI and the usage text.
func stdoutIsTTY() bool {
	return streamIsTTY(os.Stdout)
}

// stderrIsTTY reports whether standard error is a terminal. The two
// streams are detected independently (risk R5): "bfctl sync >log" leaves
// stderr a terminal (live progress), while "bfctl sync 2>log" degrades the
// progress renderer to plain lines even though stdout is interactive.
func stderrIsTTY() bool {
	return streamIsTTY(os.Stderr)
}

func streamIsTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
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

// expandPathFlag resolves a leading "~" in a user-supplied path flag via
// paths.ExpandPath, so a shell-quoted "~/..." reaches the filesystem APIs as
// an absolute path. When the home directory cannot be resolved the flag is
// left untouched and a warning is logged, letting the downstream stat
// produce the concrete diagnostic.
func expandPathFlag(name, path string) string {
	expanded, err := paths.ExpandPath(path)
	if err != nil {
		slog.Warn("expand ~ in --"+name, "error", err)
		return path
	}
	return expanded
}

// boolFlag is the optional interface a flag.Value uses to report a boolean
// flag, which does not consume a separate argument on the command line.
type boolFlag interface {
	IsBoolFlag() bool
}

// interleave reorders args so every flag precedes the positional arguments.
// Go's flag package stops parsing at the first non-flag argument, which would
// make "bfctl add owner/repo --explain" read --explain as a second positional;
// moving flags to the front keeps flags and positionals freely mixed. "--"
// still ends flag parsing: everything after it stays positional, including
// arguments that start with a dash.
func interleave(fs *flag.FlagSet, args []string) []string {
	flags := make([]string, 0, len(args))
	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			continue // -flag=value carries its own value
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // unknown flag: let fs.Parse reject it
		}
		if bf, ok := f.Value.(boolFlag); ok && bf.IsBoolFlag() {
			continue // boolean flags take no separate value
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	// Re-insert the "--" terminator before the positional block: without it
	// fs.Parse would re-read positionals like "-owner/repo" as flags. The
	// terminator is consumed by flag.Parse and never reaches fs.Args(), and
	// only the first one is consumed, so a literal "--" positional survives.
	out := make([]string, 0, len(flags)+len(positional)+1)
	out = append(out, flags...)
	out = append(out, "--")
	out = append(out, positional...)
	return out
}

// parseSubcommand parses args for fs and applies the logging configuration.
// When ok is false the caller must return code; -h/--help counts as success.
func parseSubcommand(fs *flag.FlagSet, args []string) (code int, ok bool) {
	if err := fs.Parse(interleave(fs, args)); err != nil {
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
	_, _ = fmt.Fprint(w, `bfctl - BlessForge addon manager

Usage:
  bfctl [global flags] <command> [flags]

Commands:
`)
	for _, c := range commands() {
		_, _ = fmt.Fprintf(w, "  %-8s %s\n", c.name, c.short)
	}
	_, _ = fmt.Fprint(w, `
Global flags:
  --verbose  enable debug logging
  --quiet    only log errors

Run "bfctl <command> --help" for command flags.
`)
}

func subcommandUsage(fs *flag.FlagSet) {
	out := fs.Output()
	_, _ = fmt.Fprintf(out, "bfctl %s - %s\n\nUsage:\n  bfctl %s [flags]\n\nFlags:\n",
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
