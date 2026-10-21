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
	"github.com/itp5b5/blessforge/internal/toc"
)

// addonState is the subset of state.json the status skeleton needs. The full
// state model lands with the state management milestone.
type addonState struct {
	FlavorDir string                     `json:"flavorDir"`
	Addons    map[string]json.RawMessage `json:"addons"`
}

func runStatus(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("status")
	scanDir := fs.String("scan", "", "scan an AddOns directory and report .toc selection and compatibility per addon")
	scanFlavor := fs.String("flavor", "", "client flavor for --scan: mainline, forever, classic or mists")
	scanInterface := fs.Int("interface", 0, "client interface number for --scan")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "bfctl status: unexpected argument %q\n", fs.Arg(0))
		subcommandUsage(fs)
		return ExitUsage
	}
	if *scanDir != "" {
		return runScan(*scanDir, *scanFlavor, *scanInterface)
	}
	names, err := configuredAddons()
	if err != nil {
		slog.Error("read state", "error", err)
		return ExitError
	}
	if len(names) == 0 {
		fmt.Println("no addons configured")
		return ExitOK
	}
	fmt.Printf("%d addon(s) configured:\n", len(names))
	for _, name := range names {
		fmt.Printf("  %s\n", name)
	}
	return ExitOK
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
	fmt.Fprintln(w, "Addon\tTOC\tInterfaces\tVerdict")
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", addon.Dir, tocName, interfaces, addon.Verdict)
	}
	_ = w.Flush()
	return ExitOK
}

// scanTarget resolves the client side of a --scan: explicit --flavor and
// --interface win, otherwise the client recorded by "bfctl init --path" is
// detected again. With neither, the scan only lists the selection and reports
// Unknown verdicts.
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
	if install := configuredClient(); install != nil {
		return &toc.Target{Flavor: install.Flavor, Interface: install.Interface}, nil
	}
	return nil, nil
}

// configuredAddons returns the sorted addon names from state.json. An absent
// or empty state yields no names.
func configuredAddons() ([]string, error) {
	st, err := readState()
	if err != nil || st == nil {
		return nil, err
	}
	names := make([]string, 0, len(st.Addons))
	for name := range st.Addons {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// configuredClient re-detects the client recorded by "bfctl init --path". It
// returns nil when no client has been recorded or the install cannot be read;
// callers degrade to an untargeted scan in that case.
func configuredClient() *client.ClientInstall {
	st, err := readState()
	if err != nil || st == nil || st.FlavorDir == "" {
		return nil
	}
	install, err := client.Detect(st.FlavorDir)
	if err != nil {
		slog.Debug("detect recorded client", "flavorDir", st.FlavorDir, "error", err)
		return nil
	}
	return install
}

// readState loads state.json. An absent state yields (nil, nil).
func readState() (*addonState, error) {
	statePath, err := paths.StateFile()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st addonState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", statePath, err)
	}
	return &st, nil
}
