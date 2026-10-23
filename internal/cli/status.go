package cli

import (
	"encoding/json"
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
	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
	"github.com/itp5b5/blessforge/internal/toc"
)

func runStatus(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("status")
	scanDir := fs.String("scan", "", "scan an AddOns directory and report .toc selection and compatibility per addon")
	scanFlavor := fs.String("flavor", "", "client flavor for --scan: mainline, forever, classic or mists")
	scanInterface := fs.Int("interface", 0, "client interface number for --scan")
	offline := fs.Bool("offline", false, "no network access: drop the Upstream column")
	jsonOut := fs.Bool("json", false, "machine-readable JSON report on stdout")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "bfctl status: unexpected argument %q\n", fs.Arg(0))
		subcommandUsage(fs)
		return ExitUsage
	}
	if *scanDir != "" {
		if *jsonOut {
			fmt.Fprintln(os.Stderr, "bfctl status: --json and --scan cannot be combined")
			return ExitUsage
		}
		return runScan(expandPathFlag("scan", *scanDir), *scanFlavor, *scanInterface)
	}
	if *jsonOut {
		return runStatusJSON()
	}
	return runStatusTable(*offline)
}

// runStatusTable renders the managed addons of every recorded client flavor
// as one table per flavor.
func runStatusTable(offline bool) int {
	st, err := service.LoadStatus()
	if err != nil {
		slog.Error("read state", "error", err)
		return ExitError
	}
	if len(st.Clients) == 0 {
		fmt.Println("no addons configured")
		return ExitOK
	}
	for i, cs := range st.Clients {
		if i > 0 {
			fmt.Println()
		}
		printClientStatus(cs, offline)
	}
	return ExitOK
}

// runStatusJSON prints the machine-readable status report: one flat "addons"
// array plus the per-flavor "clients" grouping, ready for jq.
func runStatusJSON() int {
	st, err := service.LoadStatus()
	if err != nil {
		slog.Error("read state", "error", err)
		return ExitError
	}
	if st.Clients == nil {
		st.Clients = []service.ClientRow{}
	}
	if st.Addons == nil {
		st.Addons = []service.AddonRow{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		slog.Error("encode status JSON", "error", err)
		return ExitError
	}
	fmt.Println(string(data))
	return ExitOK
}

// printClientStatus renders one client section: a header with the flavor
// badge and root, then the table
//
//	Addon | Version | Strategy | Dirs | TOC | Upstream
//
// The Upstream column (the last upstream tag the resolver saw) is only shown
// with network access; --offline drops it entirely.
func printClientStatus(cs service.ClientRow, offline bool) {
	badge := cs.FlavorName
	if cs.Interface != 0 {
		badge = fmt.Sprintf("%s (Interface %d)", badge, cs.Interface)
	}
	fmt.Printf("%s — %s\n", badge, cs.Root)
	if len(cs.Addons) == 0 {
		fmt.Println("  no addons installed")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if offline {
		_, _ = fmt.Fprintln(w, "Addon\tVersion\tStrategy\tDirs\tTOC")
	} else {
		_, _ = fmt.Fprintln(w, "Addon\tVersion\tStrategy\tDirs\tTOC\tUpstream")
	}
	for _, a := range cs.Addons {
		dirs := "-"
		if len(a.Dirs) > 0 {
			dirs = strings.Join(a.Dirs, ", ")
		}
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", a.Name, a.Installed, a.Strategy, dirs, a.Status)
		if !offline {
			row += "\t" + a.Upstream
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
