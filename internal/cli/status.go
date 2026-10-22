package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/state"
	"github.com/itp5b5/blessforge/internal/toc"
)

func runStatus(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("status")
	scanDir := fs.String("scan", "", "scan an AddOns directory and report .toc selection and compatibility per addon")
	scanFlavor := fs.String("flavor", "", "client flavor for --scan: mainline, forever, classic or mists")
	scanInterface := fs.Int("interface", 0, "client interface number for --scan")
	offline := fs.Bool("offline", false, "no network access: drop the Upstream column")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "bfctl status: unexpected argument %q\n", fs.Arg(0))
		subcommandUsage(fs)
		return ExitUsage
	}
	if *scanDir != "" {
		return runScan(expandPathFlag("scan", *scanDir), *scanFlavor, *scanInterface)
	}
	return runStatusTable(*offline)
}

// runStatusTable renders the managed addons of every recorded client flavor
// as one table per flavor.
func runStatusTable(offline bool) int {
	st, err := loadState()
	if err != nil {
		slog.Error("read state", "error", err)
		return ExitError
	}
	clients := sortedClients(st)
	if len(clients) == 0 {
		fmt.Println("no addons configured")
		return ExitOK
	}
	for i, cs := range clients {
		if i > 0 {
			fmt.Println()
		}
		printClientStatus(cs, offline)
	}
	return ExitOK
}

// printClientStatus renders one client section: a header with the flavor
// badge and root, then the table
//
//	Addon | Version | Strategy | Dirs | TOC | Upstream
//
// The Upstream column (the last upstream tag the resolver saw) is only shown
// with network access; --offline drops it entirely.
func printClientStatus(cs *state.ClientState, offline bool) {
	install, err := client.Detect(cs.Root)
	if err != nil {
		slog.Debug("detect recorded client", "root", cs.Root, "error", err)
	}
	flavor, iface := cs.Flavor, cs.Interface
	if install != nil {
		flavor, iface = install.Flavor, install.Interface
	}
	badge := flavor.DisplayName()
	if iface != 0 {
		badge = fmt.Sprintf("%s (Interface %d)", badge, iface)
	}
	fmt.Printf("%s — %s\n", badge, cs.Root)

	ids := make([]string, 0, len(cs.Addons))
	for id := range cs.Addons {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return addonLabel(cs.Addons[ids[i]]) < addonLabel(cs.Addons[ids[j]]) })
	if len(ids) == 0 {
		fmt.Println("  no addons installed")
		return
	}

	var target *toc.Target
	if iface != 0 {
		target = &toc.Target{Flavor: flavor, Interface: iface}
	}
	verdicts := tocVerdicts(cs, target)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if offline {
		_, _ = fmt.Fprintln(w, "Addon\tVersion\tStrategy\tDirs\tTOC")
	} else {
		_, _ = fmt.Fprintln(w, "Addon\tVersion\tStrategy\tDirs\tTOC\tUpstream")
	}
	for _, id := range ids {
		a := cs.Addons[id]
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
			addonLabel(a), addonVersion(a), addonStrategy(a), addonDirs(a), addonTOC(a, verdicts))
		if !offline {
			row += "\t" + addonUpstream(a)
		}
		_, _ = fmt.Fprintln(w, row)
	}
	_ = w.Flush()
}

// runScan lists every addon directory under dir with its selected .toc, its
// interface values and the compatibility verdict against the target client.
func runScan(dir, flavorFlag string, interfaceFlag int) int {
	target, err := scanTarget(flavorFlag, interfaceFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl status: %v\n", err)
		return ExitUsage
	}
	addons, err := toc.Scan(dir, target)
	if err != nil {
		slog.Error("scan addons", "dir", dir, "error", err)
		return ExitError
	}
	if len(addons) == 0 {
		fmt.Printf("no addons found in %s\n", dir)
		return ExitOK
	}
	if target == nil {
		fmt.Fprintln(os.Stderr, "bfctl status: no client configured; pass --flavor/--interface or run bfctl init --path ... (verdicts shown as Unknown)")
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "Addon\tTOC\tInterfaces\tVerdict")
	for _, addon := range addons {
		tocName, interfaces := "-", "-"
		if addon.Toc != nil {
			tocName = filepath.Base(addon.Toc.Path)
			if len(addon.Toc.Interfaces) > 0 {
				parts := make([]string, len(addon.Toc.Interfaces))
				for i, iface := range addon.Toc.Interfaces {
					parts[i] = strconv.Itoa(iface)
				}
				interfaces = strings.Join(parts, " ")
			}
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", addon.Dir, tocName, interfaces, addon.Verdict)
	}
	_ = w.Flush()
	return ExitOK
}

// scanTarget resolves the client side of a --scan: explicit --flavor and
// --interface win, otherwise the first recorded client (sorted by flavor) is
// re-detected so a game patch is reflected immediately, falling back to its
// recorded interface. With no recorded client the scan only lists the
// selection and reports Unknown verdicts.
func scanTarget(flavorFlag string, interfaceFlag int) (*toc.Target, error) {
	if flavorFlag != "" || interfaceFlag != 0 {
		if flavorFlag == "" || interfaceFlag == 0 {
			return nil, errors.New("--flavor and --interface must be passed together")
		}
		flavor := client.Flavor(flavorFlag)
		switch flavor {
		case client.FlavorMainline, client.FlavorForever, client.FlavorClassic, client.FlavorMists:
		default:
			return nil, fmt.Errorf("unknown flavor %q (want mainline, forever, classic or mists)", flavorFlag)
		}
		return &toc.Target{Flavor: flavor, Interface: interfaceFlag}, nil
	}
	st, err := loadState()
	if err != nil {
		slog.Error("read state", "error", err)
		return nil, nil
	}
	clients := sortedClients(st)
	if len(clients) == 0 {
		return nil, nil
	}
	cs := clients[0]
	install, err := client.Detect(cs.Root)
	if err != nil {
		slog.Debug("detect recorded client", "root", cs.Root, "error", err)
		if cs.Interface != 0 {
			return &toc.Target{Flavor: cs.Flavor, Interface: cs.Interface}, nil
		}
		return nil, nil
	}
	return &toc.Target{Flavor: install.Flavor, Interface: install.Interface}, nil
}

// loadState loads the state from the default state file.
func loadState() (*state.State, error) {
	path, err := paths.StateFile()
	if err != nil {
		return nil, err
	}
	return state.Open(path).Load()
}

// sortedClients returns the recorded clients sorted by flavor name, so the
// status output is deterministic.
func sortedClients(st *state.State) []*state.ClientState {
	clients := make([]*state.ClientState, 0, len(st.Clients))
	for _, cs := range st.Clients {
		clients = append(clients, cs)
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].Flavor < clients[j].Flavor })
	return clients
}

// tocVerdicts scans the client's AddOns directory once and reports the
// compatibility verdict per addon directory. A missing AddOns directory (or
// an unmounted install) yields no verdicts.
func tocVerdicts(cs *state.ClientState, target *toc.Target) map[string]toc.Verdict {
	addons, err := toc.Scan(cs.AddonsDir(), target)
	if err != nil {
		slog.Debug("scan addons", "dir", cs.AddonsDir(), "error", err)
		return nil
	}
	verdicts := make(map[string]toc.Verdict, len(addons))
	for _, addon := range addons {
		verdicts[addon.Dir] = addon.Verdict
	}
	return verdicts
}

// addonLabel is the name shown in the Addon column.
func addonLabel(a *state.AddonState) string {
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return a.ID
}

// addonVersion renders the installed version, falling back to the installed
// commit and finally "-".
func addonVersion(a *state.AddonState) string {
	if a.InstalledVersion != "" {
		return a.InstalledVersion
	}
	if a.InstalledCommit != "" {
		return a.InstalledCommit
	}
	return "-"
}

// addonStrategy renders the install strategy, or "-" when none is recorded.
func addonStrategy(a *state.AddonState) string {
	if a.Strategy == "" {
		return "-"
	}
	return string(a.Strategy)
}

// addonDirs renders the directories the addon owns inside Interface/AddOns.
func addonDirs(a *state.AddonState) string {
	if len(a.OwnedDirs) == 0 {
		return "-"
	}
	return strings.Join(a.OwnedDirs, ", ")
}

// addonTOC renders the TOC compatibility of one addon: the worst verdict
// across its owned directories, so one broken directory cannot hide. A
// missing owned directory is the most severe finding ("Missing"); an addon
// without owned directories renders "-".
func addonTOC(a *state.AddonState, verdicts map[string]toc.Verdict) string {
	if len(a.OwnedDirs) == 0 {
		return "-"
	}
	worst := toc.VerdictMatch
	for _, dir := range a.OwnedDirs {
		v, ok := verdicts[dir]
		if !ok {
			return "Missing"
		}
		if v > worst {
			worst = v
		}
	}
	return worst.String()
}

// addonUpstream renders the last upstream tag the resolver recorded. Live
// upstream lookups land with the ETag resolver; until then this is the
// resolver's last-seen tag from the state.
func addonUpstream(a *state.AddonState) string {
	if a.ETag != "" {
		return a.ETag
	}
	return "-"
}
