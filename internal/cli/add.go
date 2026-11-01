package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
)

// runAdd implements "bfctl add <source>": the reference is classified by
// the shared service parsers (service.ParseAddRef) and installed by the
// shared service add flow (service.Add) — the same functions the TUI's add
// input uses. GitHub "owner/repo" references and github.com URLs resolve
// an install Plan and install the matching release asset through the atomic
// installer; Wago Addons references (the addon page URL or "wago:<key>")
// install through the Wago provider and stay bound to it for updates;
// CurseForge references (the addon page URL or "cfwidget:<key>") install
// through the cfwidget CurseForge mirror provider and stay bound to it;
// any other git remote installs through the git-direct provider. A GitHub
// Plan that needs a source build is composed locally through the packager
// pipeline (.pkgmeta externals, keyword substitution, TOC flavor split) and
// installed the same way. While the work runs, progress reports stream to
// stderr through the shared renderer (deliverable D4); stdout keeps the
// frozen result lines.
func runAdd(args []string, bi BuildInfo) int {
	fs := subcommandFlagSet("add")
	branchFlag := fs.String("branch", "", "branch to track instead of stable releases")
	strategyFlag := fs.String("strategy", "", "install strategy pin: \"release\" pins to GitHub releases and silences the staleness detector")
	flavorFlag := fs.String("flavor", "", "client flavor to install for: mainline, forever (a.k.a. camelot), classic or mists (required when several are configured)")
	forceFlag := fs.Bool("force", false, "take over addon directories owned by another addon")
	explainFlag := fs.Bool("explain", false, "show why the plan was selected")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "bfctl add: expected exactly one repository reference — usage: bfctl add <source>")
		subcommandUsage(fs)
		return ExitUsage
	}
	ref, err := service.ParseAddRef(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl add: %v\n", err)
		return ExitUsage
	}
	// --branch and --strategy only apply to GitHub repositories; a
	// provider-backed reference rejects them before touching state or the
	// network.
	if ref.Kind != state.SourceGitHub && (*branchFlag != "" || *strategyFlag != "") {
		fmt.Fprintln(os.Stderr, "bfctl add: --branch and --strategy only apply to GitHub repositories — drop them, or add a GitHub owner/repo reference")
		return ExitUsage
	}
	tracking := "stable"
	if *branchFlag != "" {
		tracking = *branchFlag
	}
	switch *strategyFlag {
	case "":
	case "release":
		if *branchFlag != "" {
			fmt.Fprintln(os.Stderr, "bfctl add: --strategy release and --branch are mutually exclusive — pass only one of them")
			return ExitUsage
		}
		tracking = "release"
	default:
		fmt.Fprintf(os.Stderr, "bfctl add: unsupported --strategy %q (only \"release\" is a pin) — pass --strategy release or drop the flag\n", *strategyFlag)
		return ExitUsage
	}
	flavor, err := targetFlavor(*flavorFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl add: %v\n", err)
		return ExitUsage
	}
	// One renderer per run: progress on stderr, stdout untouched. "bfctl
	// add" has no graceful SIGINT path, so the guard only restores the
	// terminal before the process dies exactly as it otherwise would.
	prog := newProgressRenderer(os.Stderr, progressOptions{Quiet: logQuiet, TTY: stderrIsTTY(), Verb: "adding"})
	defer prog.Stop()
	defer interruptGuard(prog)()
	opts := service.AddOptions{Tracking: tracking, Force: *forceFlag, OnProgress: prog.Handle}
	if *explainFlag {
		// The unit's row is still live (a successful plan keeps reporting
		// through the install), so the plan note goes through Print like
		// every mid-run stdout line: it lands just above the row and the
		// row keeps spinning below it.
		opts.OnPlan = func(plan resolver.Plan) {
			prog.Print(service.Unit{Flavor: flavor, AddonID: ref.ID()}, fmt.Sprintf("plan: %s\n", plan.Reason))
		}
	}
	res, err := service.Add(context.Background(), ref, flavor, opts)
	if err != nil {
		prog.Stop()
		return fail("add", err, ExitError)
	}
	prog.Stop()
	fmt.Printf("added %s %s (%s%s)\n", res.ID, res.Version, res.Strategy, sourceSuffix(res.Kind))
	return ExitOK
}

// sourceSuffix labels the provider an add installed through in the result
// line — the frozen "added" output stays exactly as it always printed.
func sourceSuffix(kind state.SourceKind) string {
	switch kind {
	case state.SourceGit:
		return ", source-git"
	case state.SourceWago:
		return ", wago"
	case state.SourceCfwidget:
		return ", cfwidget"
	}
	return ""
}

// targetFlavor picks the client flavor "bfctl add" installs for: --flavor
// when it is passed, otherwise the single configured client flavor.
func targetFlavor(flavorFlag string) (client.Flavor, error) {
	st, err := loadState()
	if err != nil {
		return "", fmt.Errorf("read state: %w", err)
	}
	if flavorFlag != "" {
		flavor, ok := client.FlavorForKey(flavorFlag)
		if !ok {
			return "", fmt.Errorf("unknown flavor %q (want mainline, forever, classic or mists; \"camelot\" is an accepted spelling of forever)", flavorFlag)
		}
		if st.Client(flavor) == nil {
			return "", fmt.Errorf("no client configured for flavor %s: run \"bfctl init\" first", flavor)
		}
		return flavor, nil
	}
	clients := sortedClients(st)
	switch len(clients) {
	case 0:
		return "", fmt.Errorf("no client configured: run \"bfctl init\" first")
	case 1:
		return clients[0].Flavor, nil
	default:
		return "", fmt.Errorf("several clients configured (%s): pass --flavor", clientList(clients))
	}
}

// clientList renders the recorded flavors for error messages.
func clientList(clients []*state.ClientState) string {
	flavors := make([]string, 0, len(clients))
	for _, cs := range clients {
		flavors = append(flavors, string(cs.Flavor))
	}
	return strings.Join(flavors, ", ")
}
