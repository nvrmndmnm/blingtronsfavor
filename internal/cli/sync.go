package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/itp5b5/blessforge/internal/service"
)

// runSync implements "bfctl sync": it delegates to the shared service layer,
// which resolves every managed addon against its upstream and installs
// updates atomically — several addons at a time — and reports the outcome
// as text or JSON. --flavor scopes the run to one configured client flavor
// — the same per-client scope the TUI's sync runs with; without it every
// configured flavor is synced. While the work runs, progress reports stream to stderr
// through the shared renderer (over a terminal a live block with one row
// per addon — "syncing: 1/4 flavor/addon: downloading" spinning in place
// until the row flips to its terminal line and then becomes the addon's
// result line; plain incremental lines over a pipe, nothing under
// --quiet); stdout carries only the frozen result lines (in report order)
// and the JSON document. It never prompts, so a launch script can call it
// safely: every run ends with a deterministic exit code (0 clean, 1
// interrupted or failed to start, 2 usage, 4 partial failure). SIGINT
// aborts through the installer's rollback and exits 1.
func runSync(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("sync")
	flavorFlag := registerFlavorFlag(fs, scopedFlavorUsage)
	checkOnly := fs.Bool("check-only", false, "resolve and report only: no downloads, no installs")
	explain := fs.Bool("explain", false, "show why each plan was selected")
	jsonOut := fs.Bool("json", false, "machine-readable JSON report on stdout")
	progress := registerProgressFlag(fs)
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "bfctl sync: unexpected argument %q — usage: bfctl sync [flags]\n", fs.Arg(0))
		subcommandUsage(fs)
		return ExitUsage
	}
	flavor, err := scopedFlavor(*flavorFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl sync: %v\n", err)
		return ExitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	prog := newProgressRenderer(os.Stderr, progressOptions{Mode: *progress, Quiet: logQuiet, TTY: stderrIsTTY(), Verb: syncVerb(*checkOnly)})
	defer prog.Stop()

	rep, err := service.Sync(ctx, service.SyncOptions{
		CheckOnly: *checkOnly,
		Flavor:    flavor,
	}, func(r service.Result) {
		if !*jsonOut {
			// The unit's row becomes its result line: Print writes the
			// frozen line to stdout unchanged and seats it in the
			// terminal at the unit's row (risk R1), so the row and the
			// result can never collide on screen.
			prog.Print(service.Unit{Flavor: r.Flavor, AddonID: r.AddonID}, resultLines(r, *explain))
		}
	}, prog.Handle)
	// The work is over — quiesce the renderer before any outcome output.
	// On SIGINT this is the presentation polish of the interrupt path: the
	// rollback message below prints on a clean terminal with the cursor
	// restored, and its wording and exit code stay exactly as they are
	// (objective O5) — only the trailing next step is appended, the
	// additive "errors teach" tail (UX proposal B5).
	prog.Stop()
	if err != nil {
		return fail("sync", err, ExitError)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		fmt.Fprintln(os.Stderr, "bfctl sync: interrupted — completed work is kept, the running install was rolled back; rerun \"bfctl sync\" to continue")
		if *jsonOut {
			_ = printSyncJSON(rep)
		} else {
			fmt.Println(rep.Summary())
		}
		return ExitError
	}
	if *jsonOut {
		if err := printSyncJSON(rep); err != nil {
			return fail("sync", err, ExitError)
		}
	} else {
		fmt.Println(rep.Summary())
		if rep.RateHint != "" {
			fmt.Printf("note: GitHub rate limited — %s; export GH_TOKEN=<token> (or GITHUB_TOKEN) for 5000 req/h, then rerun \"bfctl sync\" after that\n", rep.RateHint)
		}
	}
	if rep.Failed > 0 {
		return ExitPartial
	}
	return ExitOK
}

// syncVerb names the flow on the live rows: a check-only run resolves and
// reports but installs nothing, so its rows work under "checking" — the
// same verb the TUI's check preview uses (one vocabulary, both surfaces).
func syncVerb(checkOnly bool) string {
	if checkOnly {
		return "checking"
	}
	return "syncing"
}

// resultLines renders one per-addon outcome, matching the historical
// "bfctl sync" line format. printResult writes it straight to stdout; the
// progress renderer seats the same text in the unit's live row.
func resultLines(r service.Result, explain bool) string {
	var b strings.Builder
	label := fmt.Sprintf("%s/%s", r.Flavor, r.AddonID)
	switch r.Kind {
	case service.ResultSkipped:
		fmt.Fprintf(&b, "%s: skipped: %s\n", label, r.Skip)
		return b.String()
	case service.ResultFailed:
		fmt.Fprintf(&b, "%s: error: %v\n", label, r.Err)
	case service.ResultUpToDate:
		fmt.Fprintf(&b, "%s: up to date (%s)\n", label, r.To)
	case service.ResultAvailable:
		if r.From == "-" {
			fmt.Fprintf(&b, "%s: %s available to install\n", label, r.To)
		} else {
			fmt.Fprintf(&b, "%s: update available (%s -> %s)\n", label, r.From, r.To)
		}
	case service.ResultUpdated:
		if r.From == "-" {
			fmt.Fprintf(&b, "%s: installed %s\n", label, r.To)
		} else {
			fmt.Fprintf(&b, "%s: updated %s -> %s\n", label, r.From, r.To)
		}
	}
	if explain && r.Err == nil && r.Plan.Strategy != "" {
		fmt.Fprintf(&b, "    plan: %s ref=%s reason: %s\n", r.Plan.Strategy, r.Plan.Ref, r.Plan.Reason)
	}
	return b.String()
}

// printResult writes one per-addon outcome to stdout.
func printResult(r service.Result, explain bool) {
	fmt.Print(resultLines(r, explain))
}

// syncResultJSON is one per-addon outcome in the "bfctl sync --json" report.
type syncResultJSON struct {
	Flavor string `json:"flavor"`
	Addon  string `json:"addon"`
	Status string `json:"status"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// syncSummaryJSON is the aggregate counters of the JSON report.
type syncSummaryJSON struct {
	UpToDate int `json:"up_to_date"`
	Updates  int `json:"updates_available"`
	Updated  int `json:"updated"`
	Skipped  int `json:"manual_skipped"`
	Failed   int `json:"failed"`
}

// syncJSON is the top level of the "bfctl sync --json" report.
type syncJSON struct {
	Results []syncResultJSON `json:"results"`
	Summary syncSummaryJSON  `json:"summary"`
	Note    string           `json:"note,omitempty"`
}

// printSyncJSON writes the machine-readable sync report to stdout.
func printSyncJSON(rep service.Report) error {
	out := syncJSON{
		Results: make([]syncResultJSON, 0, len(rep.Results)),
		Summary: syncSummaryJSON{
			UpToDate: rep.UpToDate,
			Updates:  rep.Updates,
			Updated:  rep.Updated,
			Skipped:  rep.Skipped,
			Failed:   rep.Failed,
		},
		Note: rep.RateHint,
	}
	for _, r := range rep.Results {
		item := syncResultJSON{
			Flavor: string(r.Flavor),
			Addon:  r.AddonID,
			Status: string(r.Kind),
			From:   r.From,
			To:     r.To,
		}
		switch {
		case r.Skip != "":
			item.Error = r.Skip
		case r.Err != nil:
			item.Error = r.Err.Error()
		default:
			item.Reason = r.Plan.Reason
		}
		out.Results = append(out.Results, item)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON report: %w", err)
	}
	_, err = fmt.Println(string(data))
	return err
}
