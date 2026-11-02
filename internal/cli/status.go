package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
	"github.com/itp5b5/blessforge/internal/toc"
	"github.com/itp5b5/blessforge/internal/tui"
)

func runStatus(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("status")
	scanDir := fs.String("scan", "", "scan an AddOns directory and report .toc selection and compatibility per addon")
	flavorFlag := registerFlavorFlag(fs, scopedFlavorUsage)
	scanInterface := fs.Int("interface", 0, "client interface number for --scan (requires --flavor, which pins the target client)")
	offline := fs.Bool("offline", false, "no network access: drop the Upstream column")
	jsonOut := fs.Bool("json", false, "machine-readable JSON report on stdout")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "bfctl status: unexpected argument %q — usage: bfctl status [flags]\n", fs.Arg(0))
		subcommandUsage(fs)
		return ExitUsage
	}
	if *scanDir != "" {
		if *jsonOut {
			fmt.Fprintln(os.Stderr, "bfctl status: --json and --scan cannot be combined — pass only one of them")
			return ExitUsage
		}
		return runScan(expandPathFlag("scan", *scanDir), *flavorFlag, *scanInterface)
	}
	flavor, err := scopedFlavor(*flavorFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl status: %v\n", err)
		return ExitUsage
	}
	if *jsonOut {
		return runStatusJSON(flavor)
	}
	return runStatusTable(*offline, flavor)
}

// statusView is the presentation mode of "bfctl status": on a terminal the
// table is colorized with the shared status color language and topped by
// the freshness line; in pipes and under NO_COLOR the historical plain
// output is produced byte for byte.
type statusView struct {
	tty   bool // stdout is a terminal: show the freshness line
	color bool // colorize the status cells (tty and NO_COLOR unset)
}

// statusViewFor derives the presentation mode from the output stream:
// terminal output is decorated, redirected output stays plain.
func statusViewFor(f *os.File) statusView {
	tty := streamIsTTY(f)
	return statusView{tty: tty, color: tty && os.Getenv("NO_COLOR") == ""}
}

// runStatusTable renders the managed addons of the recorded client flavors
// as one table per flavor — scoped to one flavor when --flavor names it.
func runStatusTable(offline bool, flavor client.Flavor) int {
	st, err := service.LoadStatus()
	if err != nil {
		return fail("status", err, ExitError)
	}
	return printStatusTables(filterStatus(st, flavor), offline, statusViewFor(os.Stdout))
}

// filterStatus scopes the report to one client flavor — the --flavor
// target — leaving it untouched when no flavor is named. The flat addons
// list is filtered alongside the per-flavor grouping so every output shape
// agrees on the scope.
func filterStatus(st service.Status, flavor client.Flavor) service.Status {
	if flavor == "" {
		return st
	}
	out := service.Status{Clients: []service.ClientRow{}, Addons: []service.AddonRow{}}
	for _, cs := range st.Clients {
		if cs.Flavor == string(flavor) {
			out.Clients = append(out.Clients, cs)
		}
	}
	for _, a := range st.Addons {
		if a.Flavor == string(flavor) {
			out.Addons = append(out.Addons, a)
		}
	}
	return out
}

// printStatusTables renders the report's per-flavor tables. On a terminal
// the freshness line comes first — the snapshot is checked live in this
// run — and the status cells carry the shared status color language; plain
// output (pipes, NO_COLOR) is the historical table unchanged.
func printStatusTables(st service.Status, offline bool, view statusView) int {
	if len(st.Clients) == 0 {
		fmt.Println("no addons configured")
		return ExitOK
	}
	if view.tty {
		printFreshness(freshnessLine(!offline), view.color)
	}
	for i, cs := range st.Clients {
		if i > 0 {
			fmt.Println()
		}
		printClientStatus(cs, offline, view)
	}
	return ExitOK
}

// freshnessLine is the "checked just now" context of the status report:
// the snapshot is assembled live in this run (the TOC verdicts come from a
// scan of the AddOns directory right now), while the Upstream column only
// knows what the last update recorded.
func freshnessLine(upstream bool) string {
	if upstream {
		return "checked just now — TOC verdicts are live; the Upstream column is from the last update"
	}
	return "checked just now — TOC verdicts are live"
}

// printFreshness prints the freshness line: dimmed under color (the TUI's
// dim), bare words under NO_COLOR — the words always carry the meaning.
func printFreshness(line string, color bool) {
	if color {
		line = lipgloss.NewStyle().Faint(true).Render(line)
	}
	fmt.Println(line)
}

// runStatusJSON prints the machine-readable status report: one flat "addons"
// array plus the per-flavor "clients" grouping, ready for jq — scoped to
// one flavor when --flavor names it.
func runStatusJSON(flavor client.Flavor) int {
	st, err := service.LoadStatus()
	if err != nil {
		return fail("status", err, ExitError)
	}
	st = filterStatus(st, flavor)
	if st.Clients == nil {
		st.Clients = []service.ClientRow{}
	}
	if st.Addons == nil {
		st.Addons = []service.AddonRow{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fail("status", err, ExitError)
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
// with network access; --offline drops it entirely. With color the TOC
// cells carry the shared status color language; without it the rows go
// through text/tabwriter exactly as they always have.
func printClientStatus(cs service.ClientRow, offline bool, view statusView) {
	badge := cs.FlavorName
	if cs.Interface != 0 {
		badge = fmt.Sprintf("%s (Interface %d)", badge, cs.Interface)
	}
	fmt.Printf("%s — %s\n", badge, cs.Root)
	if len(cs.Addons) == 0 {
		fmt.Println("  no addons installed")
		return
	}

	header := []string{"Addon", "Version", "Strategy", "Dirs", "TOC"}
	if !offline {
		header = append(header, "Upstream")
	}
	rows := make([][]string, 0, len(cs.Addons))
	for _, a := range cs.Addons {
		dirs := "-"
		if len(a.Dirs) > 0 {
			dirs = strings.Join(a.Dirs, ", ")
		}
		row := []string{a.Name, a.Installed, a.Strategy, dirs, a.Status}
		if !offline {
			row = append(row, a.Upstream)
		}
		rows = append(rows, row)
	}
	printStatusTable(os.Stdout, header, rows, 4, view.color)
}

// tablePad is the column padding of the status table — text/tabwriter's
// padding parameter.
const tablePad = 2

// printStatusTable renders one status table. Without color the rows go
// through text/tabwriter exactly as they always have, so redirected output
// stays byte-identical. With color the cell in statusCol is wrapped in the
// shared status color language and the columns are padded from the plain
// text rune widths — text/tabwriter's measure — so ANSI styling can never
// shift a column.
func printStatusTable(w io.Writer, header []string, rows [][]string, statusCol int, color bool) {
	if !color {
		tw := tabwriter.NewWriter(w, 0, 4, tablePad, ' ', 0)
		_, _ = fmt.Fprintln(tw, strings.Join(header, "\t"))
		for _, row := range rows {
			_, _ = fmt.Fprintln(tw, strings.Join(row, "\t"))
		}
		_ = tw.Flush()
		return
	}
	lines := append([][]string{header}, rows...)
	widths := columnWidths(lines)
	for i, cells := range lines {
		col := statusCol
		if i == 0 {
			col = -1 // the header labels stay plain
		}
		for j, cell := range cells {
			text := cell
			if j == col {
				text = tui.ColorizeStatus(cell, true)
			}
			if j == len(cells)-1 {
				_, _ = fmt.Fprint(w, text)
				break
			}
			_, _ = fmt.Fprint(w, text, strings.Repeat(" ", widths[j]-utf8.RuneCountInString(cell)))
		}
		_, _ = fmt.Fprintln(w)
	}
}

// columnWidths returns the width of every aligned column: the widest cell
// in runes plus the padding, the text/tabwriter measure. The trailing cell
// of each row is written verbatim (as text/tabwriter does) and gets no
// width.
func columnWidths(lines [][]string) []int {
	var widths []int
	for _, cells := range lines {
		for i, cell := range cells {
			if i == len(cells)-1 {
				break
			}
			for len(widths) <= i {
				widths = append(widths, 0)
			}
			if w := utf8.RuneCountInString(cell) + tablePad; w > widths[i] {
				widths[i] = w
			}
		}
	}
	return widths
}

// runScan lists every addon directory under dir with its selected .toc, its
// interface values and the compatibility verdict against the target client.
func runScan(dir, flavorFlag string, interfaceFlag int) int {
	target, err := scanTarget(flavorFlag, interfaceFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl status: %v\n", err)
		return ExitUsage
	}
	view := statusViewFor(os.Stdout)
	addons, err := toc.Scan(dir, target)
	if err != nil {
		return failHint("status", err, ExitError, fmt.Sprintf("check that %s is a readable AddOns directory", dir))
	}
	if len(addons) == 0 {
		fmt.Printf("no addons found in %s\n", dir)
		return ExitOK
	}
	if view.tty {
		printFreshness(freshnessLine(false), view.color)
	}
	if target == nil {
		fmt.Fprintln(os.Stderr, "bfctl status: no client configured — verdicts shown as Unknown; pass --flavor/--interface or run bfctl init --path ...")
	}
	header := []string{"Addon", "TOC", "Interfaces", "Verdict"}
	rows := make([][]string, 0, len(addons))
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
		rows = append(rows, []string{addon.Dir, tocName, interfaces, addon.Verdict.String()})
	}
	printStatusTable(os.Stdout, header, rows, 3, view.color)
	return ExitOK
}

// scanTarget resolves the client side of a --scan: explicit --flavor and
// --interface pin the target client, --flavor alone selects the recorded
// client of that flavor (re-detected so a game patch is reflected
// immediately, falling back to its recorded interface), and with neither
// flag the single recorded client is used. With several recorded clients
// and no --flavor the error lists them — a verdict needs one target client
// — and with no recorded client at all the scan only lists the selection
// and reports Unknown verdicts.
func scanTarget(flavorFlag string, interfaceFlag int) (*toc.Target, error) {
	if interfaceFlag != 0 && flavorFlag == "" {
		return nil, errors.New("--interface requires --flavor — pass both or neither")
	}
	if flavorFlag != "" {
		flavor, err := parseFlavor(flavorFlag)
		if err != nil {
			return nil, err
		}
		if interfaceFlag != 0 {
			return &toc.Target{Flavor: flavor, Interface: interfaceFlag}, nil
		}
		return recordedTarget(flavor)
	}
	st, err := loadState()
	if err != nil {
		// A state read failure degrades the scan to Unknown verdicts, but
		// the error still teaches its fix before the scan continues.
		fmt.Fprintf(os.Stderr, "bfctl status: %v\n", err)
		return nil, nil
	}
	clients := sortedClients(st)
	switch len(clients) {
	case 0:
		return nil, nil
	case 1:
		return detectedTarget(clients[0])
	default:
		return nil, fmt.Errorf("several clients configured (%s): pass --flavor", clientList(clients))
	}
}

// recordedTarget selects the configured client of one flavor for a scan:
// the flavor must be configured (the shared --flavor contract), then its
// install is re-detected like the unscoped path does.
func recordedTarget(flavor client.Flavor) (*toc.Target, error) {
	st, err := loadState()
	if err != nil {
		return nil, err
	}
	cs := st.Client(flavor)
	if cs == nil {
		return nil, fmt.Errorf("no client configured for flavor %s: run \"bfctl init\" first", flavor)
	}
	return detectedTarget(cs)
}

// detectedTarget re-detects the recorded client so a game patch shows up
// at once, falling back to its recorded interface. When neither is
// available the error names the pin that always works.
func detectedTarget(cs *state.ClientState) (*toc.Target, error) {
	install, err := client.Detect(cs.Root)
	if err != nil {
		slog.Debug("detect recorded client", "root", cs.Root, "error", err)
		if cs.Interface != 0 {
			return &toc.Target{Flavor: cs.Flavor, Interface: cs.Interface}, nil
		}
		return nil, fmt.Errorf("cannot determine the interface for flavor %s: pass --flavor/--interface to pin the target client", cs.Flavor)
	}
	return &toc.Target{Flavor: install.Flavor, Interface: install.Interface}, nil
}

// loadState loads the state from the default state file. Its errors carry
// their next step: a state read failure names the fix for the file it
// names, a path resolution failure points at HOME.
func loadState() (*state.State, error) {
	path, err := paths.StateFile()
	if err != nil {
		return nil, withHint(fmt.Errorf("resolve state file: %w", err), hintHome)
	}
	st, err := state.Open(path).Load()
	if err != nil {
		return nil, withHint(err, hintStateFile)
	}
	return st, nil
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
