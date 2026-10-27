package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/itp5b5/blessforge/internal/adopt"
	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/state"
	"github.com/itp5b5/blessforge/internal/toc"
)

// bindFlags collects repeated --bind folder=owner/repo overrides (or
// folder=cfwidget:<projectId> to pin an addon to the CurseForge mirror).
type bindFlags map[string]string

// String renders the collected overrides for flag diagnostics.
func (b bindFlags) String() string {
	pairs := make([]string, 0, len(b))
	for folder, repo := range b {
		pairs = append(pairs, folder+"="+repo)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

// Set records one folder=upstream override.
func (b bindFlags) Set(v string) error {
	folder, repo, ok := strings.Cut(v, "=")
	if !ok || folder == "" || repo == "" {
		return fmt.Errorf("want folder=owner/repo or folder=cfwidget:<projectId>, got %q", v)
	}
	b[folder] = repo
	return nil
}

// runAdopt implements "bfctl adopt": it scans the client's Interface/AddOns
// directory (or --path), extracts upstream identifiers from every addon's
// .toc metadata, groups multi-folder families by their shared IDs and
// proposes a binding table. Dry-run is the default: without --yes nothing is
// written. Unknown addons become manual entries — installed by a foreign
// tool, never touched by "bfctl sync" until re-bound with --bind.
func runAdopt(args []string, bi BuildInfo) int {
	fs := subcommandFlagSet("adopt")
	pathFlag := fs.String("path", "", "addon directory to scan (default: the configured client's Interface/AddOns)")
	yesFlag := fs.Bool("yes", false, "write the proposed bindings into the state (default is a dry run)")
	dryRunFlag := fs.Bool("dry-run", false, "only print the proposed bindings (the default)")
	flavorFlag := fs.String("flavor", "", "client flavor to adopt for: mainline, forever, classic or mists (required when several are configured)")
	binds := bindFlags{}
	fs.Var(binds, "bind", "bind one folder explicitly as folder=owner/repo or folder=cfwidget:<projectId> (repeatable)")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "bfctl adopt: unexpected argument %q\n", fs.Arg(0))
		subcommandUsage(fs)
		return ExitUsage
	}
	if *yesFlag && *dryRunFlag {
		fmt.Fprintln(os.Stderr, "bfctl adopt: --yes and --dry-run are mutually exclusive")
		return ExitUsage
	}

	flavor, err := targetFlavor(*flavorFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl adopt: %v\n", err)
		return ExitUsage
	}
	st, err := loadState()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl adopt: read state: %v\n", err)
		return ExitError
	}
	cs := st.Client(flavor)
	root, err := paths.ExpandPath(cs.Root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl adopt: resolve client root: %v\n", err)
		return ExitError
	}
	scanDir := *pathFlag
	if scanDir == "" {
		scanDir = filepath.Join(root, "Interface", "AddOns")
	} else if scanDir, err = paths.ExpandPath(scanDir); err != nil {
		fmt.Fprintf(os.Stderr, "bfctl adopt: resolve --path: %v\n", err)
		return ExitUsage
	}

	rep, err := adopt.Scan(scanDir, adopt.Options{
		Bind:   binds,
		St:     st,
		Flavor: flavor,
		Target: &toc.Target{Flavor: flavor, Interface: cs.Interface},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl adopt: %v\n", err)
		return ExitError
	}
	printAdoptReport(rep, flavor, !*yesFlag)

	if *yesFlag {
		if err := persistAdopt(flavor, rep); err != nil {
			fmt.Fprintf(os.Stderr, "bfctl adopt: %v\n", err)
			return ExitError
		}
		fmt.Printf("\nrecorded %d entr%s for %s in the state\n",
			adoptCount(rep), plural(adoptCount(rep), "y", "ies"), flavor)
		fmt.Println("sync will update the bound addons and leave the manual ones alone")
	} else {
		fmt.Printf("\ndry run: nothing written — rerun with --yes to record these bindings\n")
	}
	return ExitOK
}

// adoptCount counts the proposals --yes would write: bindings and manuals.
func adoptCount(rep *adopt.Report) int {
	var n int
	for _, g := range rep.Groups {
		if g.Action == adopt.ActionBind || g.Action == adopt.ActionManual {
			n++
		}
	}
	return n
}

// printAdoptReport renders the proposed-binding table, per-family notes and
// the summary line.
func printAdoptReport(rep *adopt.Report, flavor client.Flavor, dryRun bool) {
	fmt.Printf("proposed bindings for %s (%s)\n\n", rep.Dir, flavor)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "ACTION\tID\tSOURCE\tVERSION\tFOLDERS\tWHY\n")
	for _, g := range rep.Groups {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			g.Action, g.ID, sourceLabel(g.Source), dash(g.Version), strings.Join(g.Folders, ", "), dash(g.Reason))
	}
	for _, o := range rep.Orphans {
		_, _ = fmt.Fprintf(w, "orphan\t%s\t-\t-\t%s\t%s\n", o.Dir, o.Dir, o.Reason)
	}
	_ = w.Flush()

	noted := false
	for _, g := range rep.Groups {
		for _, n := range g.Notes {
			if !noted {
				fmt.Println("\nnotes:")
				noted = true
			}
			fmt.Printf("  %s: %s\n", g.ID, n)
		}
	}

	var bind, manual, skip int
	for _, g := range rep.Groups {
		switch g.Action {
		case adopt.ActionBind:
			bind++
		case adopt.ActionManual:
			manual++
		default:
			skip++
		}
	}
	summary := fmt.Sprintf("%d addon famil%s — %d bind, %d manual, %d skipped; %d orphan folder%s",
		len(rep.Groups), plural(len(rep.Groups), "y", "ies"),
		bind, manual, skip, len(rep.Orphans), plural(len(rep.Orphans), "", "s"))
	if dryRun {
		summary += " (dry run)"
	}
	fmt.Printf("\n%s\n", summary)
}

// sourceLabel renders a proposed source for the table.
func sourceLabel(s state.SourceRef) string {
	switch s.Kind {
	case state.SourceManual:
		return "-"
	case state.SourceGitHub:
		return "github:" + s.Slug
	case state.SourceGit:
		return "git:" + s.Slug
	default:
		return string(s.Kind) + ":" + s.Slug
	}
}

// dash renders empty table cells as "-".
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// plural returns the singular or the plural form for n.
func plural(n int, singular, p string) string {
	if n == 1 {
		return singular
	}
	return p
}

// persistAdopt writes the report's bind and manual proposals into the state
// under the state lock, leaving skipped groups and unrelated state untouched.
func persistAdopt(flavor client.Flavor, rep *adopt.Report) error {
	statePath, err := paths.StateFile()
	if err != nil {
		return err
	}
	store := state.Open(statePath)
	lock, err := store.Lock(stateLockTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	st, err := store.Load()
	if err != nil {
		return err
	}
	if err := adopt.Persist(st, flavor, rep); err != nil {
		return err
	}
	return store.Save(st)
}
