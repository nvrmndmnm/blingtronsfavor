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
	// CheckOnly resolves and reports only: no downloads, no installs.
	CheckOnly bool
	// Flavor, when non-empty, restricts the run to one client flavor's
	// addons — sync is per-client: the TUI runs against the client flavor
	// its table shows. Empty runs every configured client, the CLI's
	// unscoped "bfctl sync".
	Flavor client.Flavor
	// Only, when non-nil, restricts the run to the one named unit — the
	// TUI's "update selected addon" — instead of every managed addon.
	Only *Unit
}

// defaultSyncWorkers is how many addons a Sync run works on at once. The
// units run fully in parallel — resolve, download and install — so a slow
// addon never stalls the others; only the local state machinery inside the
// install step is serialized (see installUpdate).
const defaultSyncWorkers = 4

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

// Sync resolves every managed addon against its upstream and — unless
// CheckOnly is set — installs the available updates atomically. With
// opts.Only set it works on just that one unit instead of every managed
// addon. It never
// prompts: it either succeeds or reports per-addon failures, so a launch
// script can rely on the caller's exit-code mapping. Units of work run
// concurrently (defaultSyncWorkers at a time) in the deterministic task
// order: each addon resolves, downloads and installs on its own goroutine,
// so every addon reports its phases live while the others work. Outcomes
// are decided whenever each addon finishes but reported in the task order:
// onResult, when non-nil, is called in report order as soon as each
// outcome is decided and its predecessors are reported, and rep.Results
// keeps that order too — so the frozen per-addon result lines always
// stream in report order even though the work is parallel. onProgress,
// when non-nil, receives a ProgressEvent for every phase of every unit as
// the work happens; the unit's terminal event lands right before its
// result callback (see ProgressFunc for the delivery contract).
func Sync(ctx context.Context, opts SyncOptions, onResult func(Result), onProgress ProgressFunc) (Report, error) {
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
	tasks := filterTasksByFlavor(collectTasks(st), opts.Flavor)
	if opts.Only != nil {
		tasks = filterTasks(tasks, *opts.Only)
	}
	sink := newProgressSink(onProgress)

	// The worker pool runs whole units concurrently; ready[i] is closed
	// once unit i's outcome is decided and its terminal event delivered.
	// installMu serializes the local state machinery inside installUpdate.
	results := make([]Result, len(tasks))
	ready := make([]chan struct{}, len(tasks))
	for i := range ready {
		ready[i] = make(chan struct{})
	}
	workers := min(defaultSyncWorkers, max(len(tasks), 1))
	jobs := make(chan int, len(tasks))
	var wg sync.WaitGroup
	var installMu sync.Mutex
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = syncUnit(ctx, res, tasks[i], cfg, opts, sink, i, len(tasks), &installMu)
				sink.emit(completionEvent(results[i], i+1, len(tasks)))
				close(ready[i])
			}
		}()
	}
	for i := range tasks {
		jobs <- i
	}
	close(jobs)

	// Report in the deterministic task order: an outcome is published the
	// moment it and every predecessor are ready, so the frozen result
	// lines and rep.Results stay ordered while the work stays parallel.
	rep := Report{CheckOnly: opts.CheckOnly}
	for i := range tasks {
		<-ready[i]
		r := results[i]
		rep.Results = append(rep.Results, r)
		switch r.Kind {
		case ResultSkipped:
			rep.Skipped++
		case ResultFailed:
			rep.Failed++
			if h, ok := github.RateLimitHint(r.Err); ok && rep.RateHint == "" {
				rep.RateHint = h
			}
		case ResultAvailable:
			rep.Updates++
		case ResultUpdated:
			rep.Updated++
		default:
			rep.UpToDate++
		}
		if onResult != nil {
			onResult(r)
		}
	}
	wg.Wait()
	return rep, nil
}

// syncUnit runs one addon's whole unit of work — resolve, decide, install
// — and returns its outcome. Failures are results rather than fatal
// errors: sync reports them and keeps going. The unit's progress events
// stream through sink as the phases happen (the terminal event is emitted
// by the pool, right after the outcome is stored).
func syncUnit(ctx context.Context, res *resolver.Resolver, task syncTask, cfg *state.Config, opts SyncOptions, sink *progressSink, i, total int, installMu *sync.Mutex) Result {
	slot := progressSlot{p: sink, unit: Unit{Flavor: task.flavor, AddonID: task.addon.ID}, index: i + 1, total: total}
	outcome := resolveOne(ctx, res, task, cfg, slot)
	r := Result{Flavor: task.flavor, AddonID: task.addon.ID, From: versionOr(task.addon.InstalledVersion, task.addon.InstalledCommit)}
	switch {
	case outcome.skip != "":
		r.Kind, r.Skip = ResultSkipped, outcome.skip
	case outcome.err != nil:
		r.Kind, r.Err = ResultFailed, outcome.err
	default:
		plan := outcome.plan
		r.Plan, r.To = plan, versionOr(plan.Version, plan.Ref)
		switch {
		case plan.Version != "" && (plan.Version == task.addon.InstalledVersion || plan.Version == task.addon.ETag):
			r.Kind = ResultUpToDate
		case opts.CheckOnly:
			r.Kind = ResultAvailable
		default:
			if err := installUpdate(ctx, res, task, outcome, slot, installMu); err != nil {
				r.Kind, r.Err = ResultFailed, err
			} else {
				r.Kind = ResultUpdated
			}
		}
	}
	return r
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
// terminal outcome is reported by the caller right after the result is
// stored. Downloads run in
// parallel across units; installMu serializes the local state machinery —
// the source build and the atomic installer — so concurrent units can
// never race on the state file or the packager's git work.
func installUpdate(ctx context.Context, res *resolver.Resolver, task syncTask, outcome resolveResult, slot progressSlot, installMu *sync.Mutex) error {
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
		installMu.Lock()
		err = buildSourcePlan(ctx, outcome.plan, ref, task.flavor, dir)
		installMu.Unlock()
		if err != nil {
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
	installMu.Lock()
	defer installMu.Unlock()
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

// filterTasks restricts a run to the tasks the unit names (same flavor and
// addon id) — the "update selected addon" scope. A unit that names nothing
// in the workload yields an empty run.
func filterTasks(tasks []syncTask, unit Unit) []syncTask {
	var out []syncTask
	for _, t := range tasks {
		if t.flavor == unit.Flavor && t.addon.ID == unit.AddonID {
			out = append(out, t)
		}
	}
	return out
}

// filterTasksByFlavor restricts a run to one client flavor's tasks — the
// TUI's per-client scope. An empty flavor keeps every task (the CLI's
// unscoped run).
func filterTasksByFlavor(tasks []syncTask, flavor client.Flavor) []syncTask {
	if flavor == "" {
		return tasks
	}
	var out []syncTask
	for _, t := range tasks {
		if t.flavor == flavor {
			out = append(out, t)
		}
	}
	return out
}

// resolveOne resolves a single task against its upstream — the unit of work
// one "bfctl sync" step reports. Failures are results rather than fatal
// errors: sync reports them and keeps going. GitHub sources go through the
// release resolver; every other source kind goes through its provider (Check
// now, Fetch at install time). The user config supplies the staleness window
// (staleness_days) and any per-addon strategy pin. The unit reports a
// resolving event when its resolution starts and an explicit rate-limit
// event when GitHub throttles it (risk R6); skipped entries and tasks that
// fail before resolution report nothing here (their terminal event comes
// from Sync's outcome loop).
func resolveOne(ctx context.Context, res *resolver.Resolver, task syncTask, cfg *state.Config, slot progressSlot) resolveResult {
	if task.addon.Source.Kind == state.SourceManual || task.addon.Strategy == state.StrategyManual {
		return resolveResult{skip: "installed by a foreign tool (re-bind with \"bfctl adopt --bind\" to manage it)"}
	}
	if task.addon.Source.Kind != state.SourceGitHub {
		p, err := provider.For(task.addon.Source.Kind, provider.Options{Flavor: task.flavor, Interface: task.iface})
		if err != nil {
			return resolveResult{err: err}
		}
		slot.emit(PhaseResolving, Detail{})
		up, err := p.Check(ctx, task.addon.Source)
		if err != nil {
			slot.rateLimited(PhaseResolving, err)
			return resolveResult{err: fmt.Errorf("addon %s: %w", task.addon.ID, err)}
		}
		return resolveResult{
			prov:     p,
			upstream: up,
			plan: resolver.Plan{
				Strategy: up.Strategy,
				Version:  up.Version,
				URL:      up.URL,
				Ref:      up.Ref,
				Reason:   "resolved by the " + string(task.addon.Source.Kind) + " provider",
			},
		}
	}
	ref, err := sourceRepo(task.addon)
	if err != nil {
		return resolveResult{err: err}
	}
	slot.emit(PhaseResolving, Detail{})
	pin, _ := cfg.PinnedStrategy(task.addon.ID, ref.Slug())
	plan, err := res.Resolve(ctx, resolver.Request{
		Owner:          ref.Owner,
		Repo:           ref.Repo,
		Flavor:         task.flavor,
		Tracking:       task.addon.Tracking,
		Staleness:      cfg.Staleness(),
		PinnedStrategy: pin,
	})
	slot.rateLimited(PhaseResolving, err)
	return resolveResult{plan: plan, err: err}
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
