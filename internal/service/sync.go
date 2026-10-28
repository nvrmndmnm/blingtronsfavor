package service

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
	"github.com/itp5b5/blessforge/internal/provider"
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
// the deterministic task order. onProgress, when non-nil, receives a
// ProgressEvent for every phase of every unit as the work happens (see
// ProgressFunc for the delivery contract).
func Sync(ctx context.Context, opts SyncOptions, onResult func(Result), onProgress ProgressFunc) (Report, error) {
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
	sink := newProgressSink(onProgress)
	results := resolveAll(ctx, res, tasks, workers, cfg, sink)

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
				slot := progressSlot{p: sink, unit: Unit{Flavor: task.flavor, AddonID: task.addon.ID}, index: i + 1, total: len(tasks)}
				if err := installUpdate(ctx, res, task, outcome, slot); err != nil {
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
		sink.emit(completionEvent(r, i+1, len(tasks)))
	}
	return rep, nil
}

// completionEvent maps one decided Result to its terminal progress event.
func completionEvent(r Result, index, total int) ProgressEvent {
	ev := ProgressEvent{Unit: Unit{Flavor: r.Flavor, AddonID: r.AddonID}, Index: index, Total: total}
	switch r.Kind {
	case ResultSkipped:
		ev.Phase, ev.Detail.Reason = PhaseSkipped, r.Skip
	case ResultFailed:
		ev.Phase, ev.Detail.Err = PhaseFailed, r.Err
	default:
		ev.Phase, ev.Detail.Version = PhaseDone, r.To
	}
	return ev
}

// newResolver builds the GitHub-backed resolver.
func newResolver() (*resolver.Resolver, error) {
	gh, err := github.New(github.Options{Version: ToolVersion, Token: github.TokenFromEnv()})
	if err != nil {
		return nil, err
	}
	return &resolver.Resolver{GH: gh}, nil
}

// installUpdate downloads and unpacks the plan's payload — a GitHub release
// asset through the resolver, a provider package through provider.Fetch, a
// source build composed locally through the packager pipeline — and installs
// it atomically through the installer, recording the new version, the
// upstream tag and the upstream changelog (the Tukui/Wago inline text
// precedent; never a signed download link) on the addon's existing state
// entry. The download and install phases are reported through slot; the
// terminal outcome is reported by Sync's outcome loop.
func installUpdate(ctx context.Context, res *resolver.Resolver, task syncTask, outcome resolveResult, slot progressSlot) error {
	dir, err := os.MkdirTemp("", "bfctl-sync-")
	if err != nil {
		return fmt.Errorf("create fetch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	slot.emit(PhaseDownloading, Detail{Version: outcome.plan.Version, Ref: outcome.plan.Ref})
	var root string
	if outcome.prov != nil {
		if err := outcome.prov.Fetch(ctx, task.addon.Source, dir); err != nil {
			slot.rateLimited(PhaseDownloading, err)
			return err
		}
		root = dir // provider.Fetch unpacks into dest itself
	} else if outcome.plan.Strategy == state.StrategyRelease {
		root, err = res.Fetch(ctx, outcome.plan, dir)
		if err != nil {
			slot.rateLimited(PhaseDownloading, err)
			return err
		}
	} else {
		// A source-pkgmeta plan is composed locally through the packager
		// pipeline (the resolver's Fetch only downloads release assets).
		ref, err := sourceRepo(task.addon)
		if err != nil {
			return err
		}
		if err := buildSourcePlan(ctx, outcome.plan, ref, task.flavor, dir); err != nil {
			return err
		}
		root = dir
	}
	rec := *task.addon
	rec.Strategy = outcome.plan.Strategy
	rec.InstalledVersion = versionOr(outcome.plan.Version, outcome.plan.Ref)
	rec.InstalledCommit = ""
	rec.ETag = outcome.plan.Version
	rec.Changelog = outcome.upstream.Changelog
	rec.UpdatedAt = time.Now().UTC()
	in, err := installerFor()
	if err != nil {
		return err
	}
	slot.emit(PhaseInstalling, Detail{Version: outcome.plan.Version, Ref: outcome.plan.Ref})
	return in.Install(ctx, installer.Request{Flavor: task.flavor, Addon: rec, SourceDir: root})
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
	iface  int // client interface version (the git-direct packager needs it)
	addon  *state.AddonState
}

// resolveResult is one concurrent resolution outcome.
type resolveResult struct {
	plan     resolver.Plan
	prov     provider.Provider // non-nil for provider-backed sources (wago, tukui, wowi, git)
	upstream provider.Upstream // provider-reported upstream (version, changelog)
	err      error
	skip     string // non-empty: nothing to resolve (manual entry from "bfctl adopt")
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
			tasks = append(tasks, syncTask{flavor: cs.Flavor, iface: cs.Interface, addon: cs.Addons[id]})
		}
	}
	return tasks
}

// resolveAll resolves every task with at most workers concurrent calls — the
// errgroup-with-limit pattern the plan calls for, kept on a semaphore plus a
// WaitGroup so the module stays dependency-free. Per-task failures are
// results rather than fatal errors: sync reports them and keeps going. GitHub
// sources go through the release resolver; every other source kind goes
// through its provider (Check now, Fetch at install time). The user config
// supplies the staleness window (staleness_days) and any per-addon strategy
// pin. Each unit reports a resolving event when its worker starts and an
// explicit rate-limit event when GitHub throttles it (risk R6); delivery
// is serialized through sink, so the callback is never invoked
// concurrently.
func resolveAll(ctx context.Context, res *resolver.Resolver, tasks []syncTask, workers int, cfg *state.Config, sink *progressSink) []resolveResult {
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
			p, err := provider.For(task.addon.Source.Kind, provider.Options{Flavor: task.flavor, Interface: task.iface})
			if err != nil {
				results[i].err = err
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, p provider.Provider) {
				defer wg.Done()
				defer func() { <-sem }()
				slot := progressSlot{p: sink, unit: Unit{Flavor: tasks[i].flavor, AddonID: tasks[i].addon.ID}, index: i + 1, total: len(tasks)}
				slot.emit(PhaseResolving, Detail{})
				up, err := p.Check(ctx, tasks[i].addon.Source)
				if err != nil {
					slot.rateLimited(PhaseResolving, err)
					results[i].err = fmt.Errorf("addon %s: %w", tasks[i].addon.ID, err)
					return
				}
				results[i].prov = p
				results[i].upstream = up
				results[i].plan = resolver.Plan{
					Strategy: up.Strategy,
					Version:  up.Version,
					URL:      up.URL,
					Ref:      up.Ref,
					Reason:   "resolved by the " + string(tasks[i].addon.Source.Kind) + " provider",
				}
			}(i, p)
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
			slot := progressSlot{p: sink, unit: Unit{Flavor: tasks[i].flavor, AddonID: tasks[i].addon.ID}, index: i + 1, total: len(tasks)}
			slot.emit(PhaseResolving, Detail{})
			pin, _ := cfg.PinnedStrategy(tasks[i].addon.ID, ref.Slug())
			plan, err := res.Resolve(ctx, resolver.Request{
				Owner:          ref.Owner,
				Repo:           ref.Repo,
				Flavor:         tasks[i].flavor,
				Tracking:       tasks[i].addon.Tracking,
				Staleness:      cfg.Staleness(),
				PinnedStrategy: pin,
			})
			slot.rateLimited(PhaseResolving, err)
			results[i].plan, results[i].err = plan, err
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
