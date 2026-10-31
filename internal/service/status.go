package service

import (
	"log/slog"
	"sort"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/state"
	"github.com/itp5b5/blessforge/internal/toc"
)

// AddonRow is one addon in a status report. The strings are display-ready:
// they match what "bfctl status" prints in its table (including the "-"
// fallbacks), so the CLI, the --json output and the TUI all agree.
type AddonRow struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Flavor     string   `json:"flavor"`
	Installed  string   `json:"installed"`
	Upstream   string   `json:"upstream"`
	Strategy   string   `json:"strategy"`
	Tracking   string   `json:"tracking"`
	Status     string   `json:"status"`             // worst TOC compatibility verdict
	BuiltFor   []int    `json:"builtFor,omitempty"` // interface numbers the worst-verdict .toc files declare — the "built for 110002" facts behind the plain-language verdict
	Dirs       []string `json:"dirs,omitempty"`
	SourceKind string   `json:"sourceKind,omitempty"`
	Source     string   `json:"source,omitempty"`
	Changelog  string   `json:"changelog,omitempty"` // changelog of the installed version (inline text or URL)
}

// ClientRow is one client flavor in a status report: the flavor badge fields
// plus that flavor's addons.
type ClientRow struct {
	Flavor     string     `json:"flavor"`
	FlavorName string     `json:"flavorName"`
	Version    string     `json:"version,omitempty"`
	Interface  int        `json:"interface"`
	Root       string     `json:"root"`
	Addons     []AddonRow `json:"addons"`
}

// Status is the machine-readable status report consumed by "bfctl status
// --json" and the TUI. Addons is the flat list of every client's addons;
// Clients keeps the per-flavor grouping.
type Status struct {
	Clients []ClientRow `json:"clients"`
	Addons  []AddonRow  `json:"addons"`
}

// LoadStatus assembles the status report for every recorded client flavor.
func LoadStatus() (Status, error) {
	st, err := LoadState()
	if err != nil {
		return Status{}, err
	}
	var out Status
	for _, cs := range sortedClients(st) {
		cr := clientRow(cs)
		out.Clients = append(out.Clients, cr)
		out.Addons = append(out.Addons, cr.Addons...)
	}
	return out, nil
}

// sortedClients returns the recorded clients sorted by flavor name, so the
// report is deterministic.
func sortedClients(st *state.State) []*state.ClientState {
	clients := make([]*state.ClientState, 0, len(st.Clients))
	for _, cs := range st.Clients {
		clients = append(clients, cs)
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].Flavor < clients[j].Flavor })
	return clients
}

// clientRow builds one flavor's report section: re-detect the recorded client
// so a game patch shows up immediately, then render every addon sorted by
// display name.
func clientRow(cs *state.ClientState) ClientRow {
	install, err := client.Detect(cs.Root)
	if err != nil {
		slog.Debug("detect recorded client", "root", cs.Root, "error", err)
	}
	flavor, iface := cs.Flavor, cs.Interface
	version := ""
	if install != nil {
		flavor, iface = install.Flavor, install.Interface
		version = install.Version.Short()
	}
	row := ClientRow{
		Flavor:     string(flavor),
		FlavorName: flavor.DisplayName(),
		Version:    version,
		Interface:  iface,
		Root:       cs.Root,
	}

	ids := make([]string, 0, len(cs.Addons))
	for id := range cs.Addons {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return addonLabel(cs.Addons[ids[i]]) < addonLabel(cs.Addons[ids[j]]) })
	if len(ids) == 0 {
		return row
	}

	var target *toc.Target
	if iface != 0 {
		target = &toc.Target{Flavor: flavor, Interface: iface}
	}
	scanned := tocAddons(cs, target)
	for _, id := range ids {
		a := cs.Addons[id]
		src := a.Source.URL
		if src == "" {
			src = a.Source.Slug
		}
		status, builtFor := addonStatus(a, scanned)
		row.Addons = append(row.Addons, AddonRow{
			ID:         a.ID,
			Name:       addonLabel(a),
			Flavor:     string(cs.Flavor),
			Installed:  versionOr(a.InstalledVersion, a.InstalledCommit),
			Upstream:   upstreamOr(a),
			Strategy:   strategyOr(a),
			Tracking:   trackingOr(a),
			Status:     status,
			BuiltFor:   builtFor,
			Dirs:       a.OwnedDirs,
			SourceKind: string(a.Source.Kind),
			Source:     src,
			Changelog:  a.Changelog,
		})
	}
	return row
}

// tocAddons scans the client's AddOns directory once and reports the
// scanned addon per addon directory: its selected .toc and the
// compatibility verdict. A missing AddOns directory (or an unmounted
// install) yields no entries.
func tocAddons(cs *state.ClientState, target *toc.Target) map[string]toc.Addon {
	addons, err := toc.Scan(cs.AddonsDir(), target)
	if err != nil {
		slog.Debug("scan addons", "dir", cs.AddonsDir(), "error", err)
		return nil
	}
	byDir := make(map[string]toc.Addon, len(addons))
	for _, addon := range addons {
		byDir[addon.Dir] = addon
	}
	return byDir
}

// addonLabel is the name shown in the Addon column.
func addonLabel(a *state.AddonState) string {
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return a.ID
}

// addonStatus renders the TOC compatibility of one addon: the worst verdict
// across its owned directories, so one broken directory cannot hide. A
// missing owned directory is the most severe finding ("Missing"); an addon
// without owned directories renders "-". builtFor lists the interface
// numbers the worst-verdict directories' selected .toc files declare — the
// facts behind the plain-language verdict ("built for 110002 — may be
// outdated").
func addonStatus(a *state.AddonState, addons map[string]toc.Addon) (status string, builtFor []int) {
	if len(a.OwnedDirs) == 0 {
		return "-", nil
	}
	worst := toc.VerdictMatch
	for _, dir := range a.OwnedDirs {
		scanned, ok := addons[dir]
		if !ok {
			return "Missing", nil
		}
		if scanned.Verdict > worst {
			worst = scanned.Verdict
		}
	}
	seen := make(map[int]bool)
	for _, dir := range a.OwnedDirs {
		scanned := addons[dir]
		if scanned.Verdict != worst || scanned.Toc == nil {
			continue
		}
		for _, iface := range scanned.Toc.Interfaces {
			if !seen[iface] {
				seen[iface] = true
				builtFor = append(builtFor, iface)
			}
		}
	}
	sort.Ints(builtFor)
	return worst.String(), builtFor
}

// upstreamOr renders the last upstream tag the resolver recorded, or "-".
func upstreamOr(a *state.AddonState) string {
	if a.ETag != "" {
		return a.ETag
	}
	return "-"
}

// strategyOr renders the install strategy, or "-" when none is recorded.
func strategyOr(a *state.AddonState) string {
	if a.Strategy == "" {
		return "-"
	}
	return string(a.Strategy)
}

// trackingOr renders the tracking mode, or "-".
func trackingOr(a *state.AddonState) string {
	if a.Tracking == "" {
		return "-"
	}
	return a.Tracking
}
