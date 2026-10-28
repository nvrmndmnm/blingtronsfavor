package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/itp5b5/blessforge/internal/service"
)

// runSync implements "bfctl sync": it delegates to the shared service layer,
// which resolves every managed addon against its upstream and installs
// updates atomically, and reports the outcome as text or JSON. It never
// prompts, so a launch script can call it safely: every run ends with a
// deterministic exit code (0 clean, 1 interrupted or failed to start,
// 2 usage, 4 partial failure). SIGINT aborts through the installer's
// rollback and exits 1.
func runSync(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("sync")
	checkOnly := fs.Bool("check-only", false, "resolve and report only: no downloads, no installs")
	workers := fs.Int("workers", 4, "number of concurrent resolutions")
	explain := fs.Bool("explain", false, "show why each plan was selected")
	jsonOut := fs.Bool("json", false, "machine-readable JSON report on stdout")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "bfctl sync: unexpected argument %q\n", fs.Arg(0))
		subcommandUsage(fs)
		return ExitUsage
	}
	if *workers < 1 {
		fmt.Fprintln(os.Stderr, "bfctl sync: --workers must be at least 1")
		return ExitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	rep, err := service.Sync(ctx, service.SyncOptions{
		Workers:   *workers,
		CheckOnly: *checkOnly,
	}, func(r service.Result) {
		if !*jsonOut {
			printResult(r, *explain)
		}
	}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl sync: %v\n", err)
		return ExitError
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		fmt.Fprintln(os.Stderr, "bfctl sync: interrupted — completed work is kept, the running install was rolled back")
		if *jsonOut {
			_ = printSyncJSON(rep)
		} else {
			fmt.Println(rep.Summary())
		}
		return ExitError
	}
	if *jsonOut {
		if err := printSyncJSON(rep); err != nil {
			fmt.Fprintf(os.Stderr, "bfctl sync: %v\n", err)
			return ExitError
		}
	} else {
		fmt.Println(rep.Summary())
		if rep.RateHint != "" {
			fmt.Printf("note: GitHub rate limited — %s; rerun \"bfctl sync\" after that\n", rep.RateHint)
		}
	}
	if rep.Failed > 0 {
		return ExitPartial
	}
	return ExitOK
}

// printResult renders one per-addon outcome, matching the historical
// "bfctl sync" line format.
func printResult(r service.Result, explain bool) {
	label := fmt.Sprintf("%s/%s", r.Flavor, r.AddonID)
	switch r.Kind {
	case service.ResultSkipped:
		fmt.Printf("%s: skipped: %s\n", label, r.Skip)
		return
	case service.ResultFailed:
		fmt.Printf("%s: error: %v\n", label, r.Err)
	case service.ResultUpToDate:
		fmt.Printf("%s: up to date (%s)\n", label, r.To)
	case service.ResultAvailable:
		if r.From == "-" {
			fmt.Printf("%s: %s available to install\n", label, r.To)
		} else {
			fmt.Printf("%s: update available (%s -> %s)\n", label, r.From, r.To)
		}
	case service.ResultUpdated:
		if r.From == "-" {
			fmt.Printf("%s: installed %s\n", label, r.To)
		} else {
			fmt.Printf("%s: updated %s -> %s\n", label, r.From, r.To)
		}
	}
	if explain && r.Err == nil && r.Plan.Strategy != "" {
		fmt.Printf("    plan: %s ref=%s reason: %s\n", r.Plan.Strategy, r.Plan.Ref, r.Plan.Reason)
	}
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
