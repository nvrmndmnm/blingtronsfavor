package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/github"
	"github.com/itp5b5/blessforge/internal/installer"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/state"
)

// syncTask is one (client flavor, addon) pair to resolve and sync.
type syncTask struct {
	flavor client.Flavor
	addon  *state.AddonState
}

// resolveResult is one concurrent resolution outcome.
type resolveResult struct {
	plan resolver.Plan
	err  error
	skip string // non-empty: nothing to resolve (manual entry from "bfctl adopt")
}

// runSync implements "bfctl sync": it resolves every managed addon against
// its upstream concurrently, reports the per-addon plan and installs updates
// atomically. It exits 0 when every addon is clean, 4 on partial failure.
// Repositories whose plan needs a source build report "source build
// required" and are skipped cleanly until source builds land (M4); only
// --strict turns that deferral into a partial failure. Manual entries
// recorded by "bfctl adopt" are never resolved or touched — they are
// reported as skipped until they are re-bound with "bfctl adopt --bind".
func runSync(args []string, bi BuildInfo) int {
	fs := subcommandFlagSet("sync")
	checkOnly := fs.Bool("check-only", false, "resolve and report only: no downloads, no installs")
	workers := fs.Int("workers", 4, "number of concurrent resolutions")
	explain := fs.Bool("explain", false, "show why each plan was selected")
	strict := fs.Bool("strict", false, "treat source builds deferred to M4 as failures")
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

	st, err := loadState()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl sync: read state: %v\n", err)
		return ExitError
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl sync: read config: %v\n", err)
		return ExitError
	}
	tasks := collectTasks(st)
	res, err := newResolver(bi)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl sync: create github client: %v\n", err)
		return ExitError
	}
	ctx := context.Background()
	results := resolveAll(ctx, res, tasks, *workers, cfg)

	var upToDate, updates, updated, sourceBuild, skipped, failed int
	var rateHint string
	for i, task := range tasks {
		label := fmt.Sprintf("%s/%s", task.flavor, task.addon.ID)
		result := results[i]
		if result.skip != "" {
			fmt.Printf("%s: skipped: %s\n", label, result.skip)
			skipped++
			continue
		}
		if result.err != nil {
			fmt.Printf("%s: error: %v\n", label, result.err)
			if h, ok := github.RateLimitHint(result.err); ok && rateHint == "" {
				rateHint = h
			}
			failed++
			continue
		}
		plan := result.plan
		var status string
		switch {
		case plan.Strategy != state.StrategyRelease:
			status = "source build required"
			sourceBuild++
		case plan.Version == task.addon.InstalledVersion || plan.Version == task.addon.ETag:
			status = fmt.Sprintf("up to date (%s)", plan.Version)
			upToDate++
		case *checkOnly:
			status = fmt.Sprintf("update available (%s -> %s)", addonVersion(task.addon), plan.Version)
			updates++
		default:
			if err := installUpdate(ctx, res, task, plan); err != nil {
				fmt.Printf("%s: error: %v\n", label, err)
				if h, ok := github.RateLimitHint(err); ok && rateHint == "" {
					rateHint = h
				}
				failed++
				continue
			}
			status = fmt.Sprintf("updated %s -> %s", addonVersion(task.addon), plan.Version)
			updated++
		}
		fmt.Printf("%s: %s\n", label, status)
		if *explain {
			fmt.Printf("    plan: %s ref=%s reason: %s\n", plan.Strategy, plan.Ref, plan.Reason)
		}
	}

	summary := fmt.Sprintf("%d up to date, %d updated", upToDate, updated)
	if *checkOnly {
		summary = fmt.Sprintf("%d up to date, %d update(s) available", upToDate, updates)
	}
	if sourceBuild > 0 {
		summary += fmt.Sprintf(", %d source build required", sourceBuild)
	}
	if skipped > 0 {
		summary += fmt.Sprintf(", %d manual skipped", skipped)
	}
	if failed > 0 {
		summary += fmt.Sprintf(", %d failed", failed)
	}
	fmt.Println(summary)
	if rateHint != "" {
		fmt.Printf("note: GitHub rate limited — %s; rerun \"bfctl sync\" after that\n", rateHint)
	}

	if failed > 0 || (*strict && sourceBuild > 0) {
		return ExitPartial
	}
	return ExitOK
}

// installUpdate downloads and unpacks the plan's release asset and installs
// it atomically through the installer, recording the new version and upstream
// tag on the addon's existing state entry.
func installUpdate(ctx context.Context, res *resolver.Resolver, task syncTask, plan resolver.Plan) error {
	dir, err := os.MkdirTemp("", "bfctl-sync-")
	if err != nil {
		return fmt.Errorf("create fetch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	root, err := res.Fetch(ctx, plan, dir)
	if err != nil {
		return err
	}
	rec := *task.addon
	rec.Strategy = plan.Strategy
	rec.InstalledVersion = plan.Version
	rec.InstalledCommit = ""
	rec.ETag = plan.Version
	rec.UpdatedAt = time.Now().UTC()
	statePath, err := paths.StateFile()
	if err != nil {
		return err
	}
	in := &installer.Installer{Store: state.Open(statePath)}
	return in.Install(installer.Request{Flavor: task.flavor, Addon: rec, SourceDir: root})
}

// collectTasks lists every managed addon of every recorded client flavor,
// sorted by flavor then addon id so the report is deterministic.
func collectTasks(st *state.State) []syncTask {
	var tasks []syncTask
	for _, cs := range sortedClients(st) {
		ids := make([]string, 0, len(cs.Addons))
		for id := range cs.Addons {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			tasks = append(tasks, syncTask{flavor: cs.Flavor, addon: cs.Addons[id]})
		}
	}
	return tasks
}

// resolveAll resolves every task with at most workers concurrent calls — the
// errgroup-with-limit pattern the plan calls for, kept on a semaphore plus a
// WaitGroup so the module stays dependency-free. Per-task failures are
// results rather than fatal errors: sync reports them and keeps going. The
// user config supplies the staleness window (staleness_days) and any
// per-addon strategy pin.
func resolveAll(ctx context.Context, res *resolver.Resolver, tasks []syncTask, workers int, cfg *state.Config) []resolveResult {
	results := make([]resolveResult, len(tasks))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range tasks {
		task := tasks[i]
		if task.addon.Source.Kind == state.SourceManual || task.addon.Strategy == state.StrategyManual {
			results[i].skip = "installed by a foreign tool (re-bind with \"bfctl adopt --bind\" to manage it)"
			continue
		}
		if task.addon.Source.Kind != state.SourceGitHub {
			results[i].err = fmt.Errorf("unsupported source kind %q (only github resolution is implemented)", task.addon.Source.Kind)
			continue
		}
		ref, err := sourceRepo(task.addon)
		if err != nil {
			results[i].err = err
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			pin, _ := cfg.PinnedStrategy(task.addon.ID, ref.Slug())
			results[i].plan, results[i].err = res.Resolve(ctx, resolver.Request{
				Owner:          ref.Owner,
				Repo:           ref.Repo,
				Flavor:         task.flavor,
				Tracking:       task.addon.Tracking,
				Staleness:      cfg.Staleness(),
				PinnedStrategy: pin,
			})
		}(i)
	}
	wg.Wait()
	return results
}

// sourceRepo parses the addon's recorded GitHub reference.
func sourceRepo(a *state.AddonState) (resolver.RepoRef, error) {
	raw := a.Source.Slug
	if raw == "" {
		raw = a.Source.URL
	}
	ref, err := resolver.ParseRepo(raw)
	if err != nil {
		return resolver.RepoRef{}, fmt.Errorf("addon %s: %w", a.ID, err)
	}
	return ref, nil
}
