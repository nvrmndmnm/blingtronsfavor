package service

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/github"
	"github.com/itp5b5/blessforge/internal/installer"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/state"
)

// SyncOptions configures one Sync run.
type SyncOptions struct {
	// Workers bounds concurrent upstream resolutions; values below 1
	// default to 4.
	Workers int

	// CheckOnly resolves and reports only: no downloads, no installs.
	CheckOnly bool
}

// ResultKind classifies one per-addon sync outcome.
type ResultKind string

// Per-addon sync outcomes.
const (
	ResultUpToDate  ResultKind = "up-to-date"
	ResultAvailable ResultKind = "update-available"
	ResultUpdated   ResultKind = "updated"
	ResultSkipped   ResultKind = "skipped"
	ResultFailed    ResultKind = "failed"
)

// Result is one per-addon sync outcome, ready for text, JSON or TUI display.
type Result struct {
	Flavor  client.Flavor
	AddonID string
	Kind    ResultKind
	From    string // installed version (or commit) before the run
	To      string // resolved version, when one was resolved
	Plan    resolver.Plan
	Skip    string // set for ResultSkipped
	Err     error  // set for ResultFailed
}

// Report is the aggregate outcome of a Sync run.
type Report struct {
	Results   []Result
	CheckOnly bool
	UpToDate  int
	Updates   int // update-available outcomes (check-only runs)
	Updated   int
	Skipped   int
	Failed    int
	RateHint  string // GitHub rate-limit wait hint, when rate limited
}

// Summary renders the one-line summary "bfctl sync" prints.
func (r Report) Summary() string {
	summary := fmt.Sprintf("%d up to date, %d updated", r.UpToDate, r.Updated)
	if r.CheckOnly {
		summary = fmt.Sprintf("%d up to date, %d update(s) available", r.UpToDate, r.Updates)
	}
	if r.Skipped > 0 {
		summary += fmt.Sprintf(", %d manual skipped", r.Skipped)
	}
	if r.Failed > 0 {
		summary += fmt.Sprintf(", %d failed", r.Failed)
	}
	return summary
}

// Sync resolves every managed addon against its upstream concurrently and —
// unless CheckOnly is set — installs the available updates atomically. It
// never prompts: it either succeeds or reports per-addon failures, so a
// launch script can rely on the caller's exit-code mapping. onResult, when
// non-nil, is called for each addon as soon as its outcome is decided, in
// the deterministic task order.
func Sync(ctx context.Context, opts SyncOptions, onResult func(Result)) (Report, error) {
	workers := opts.Workers
	if workers < 1 {
		workers = 4
	}
	st, err := LoadState()
	if err != nil {
		return Report{}, fmt.Errorf("read state: %w", err)
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		return Report{}, fmt.Errorf("read config: %w", err)
	}
	res, err := newResolver()
	if err != nil {
		return Report{}, fmt.Errorf("create github client: %w", err)
	}
	tasks := collectTasks(st)
	results := resolveAll(ctx, res, tasks, workers, cfg)

	rep := Report{CheckOnly: opts.CheckOnly}
	emit := func(r Result) {
		rep.Results = append(rep.Results, r)
		if onResult != nil {
			onResult(r)
		}
	}
	for i, task := range tasks {
		outcome := results[i]
		r := Result{Flavor: task.flavor, AddonID: task.addon.ID, From: versionOr(task.addon.InstalledVersion, task.addon.InstalledCommit)}
		switch {
		case outcome.skip != "":
			r.Kind, r.Skip = ResultSkipped, outcome.skip
			rep.Skipped++
		case outcome.err != nil:
			r.Kind, r.Err = ResultFailed, outcome.err
			if h, ok := github.RateLimitHint(outcome.err); ok && rep.RateHint == "" {
				rep.RateHint = h
			}
			rep.Failed++
		default:
			plan := outcome.plan
			r.Plan, r.To = plan, versionOr(plan.Version, plan.Ref)
			switch {
			case plan.Version != "" && (plan.Version == task.addon.InstalledVersion || plan.Version == task.addon.ETag):
				r.Kind = ResultUpToDate
				rep.UpToDate++
			case opts.CheckOnly:
				r.Kind = ResultAvailable
				rep.Updates++
			default:
				if err := InstallPlan(ctx, res, task.flavor, *task.addon, plan, false); err != nil {
					r.Kind, r.Err = ResultFailed, err
					if h, ok := github.RateLimitHint(err); ok && rep.RateHint == "" {
						rep.RateHint = h
					}
					rep.Failed++
				} else {
					r.Kind = ResultUpdated
					rep.Updated++
				}
			}
		}
		emit(r)
	}
	return rep, nil
}

// newResolver builds the GitHub-backed resolver.
func newResolver() (*resolver.Resolver, error) {
	gh, err := github.New(github.Options{Version: ToolVersion, Token: github.TokenFromEnv()})
	if err != nil {
		return nil, err
	}
	return &resolver.Resolver{GH: gh}, nil
}

// installerFor builds the atomic installer backed by the default state store.
func installerFor() (*installer.Installer, error) {
	store, err := openStore()
	if err != nil {
		return nil, err
	}
	return &installer.Installer{Store: store}, nil
}

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
			pin, _ := cfg.PinnedStrategy(tasks[i].addon.ID, ref.Slug())
			results[i].plan, results[i].err = res.Resolve(ctx, resolver.Request{
				Owner:          ref.Owner,
				Repo:           ref.Repo,
				Flavor:         tasks[i].flavor,
				Tracking:       tasks[i].addon.Tracking,
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
