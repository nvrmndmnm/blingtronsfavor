package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/github"
	"github.com/itp5b5/blessforge/internal/provider"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/state"
)

// fakeProvider is a provider.Provider stand-in: Check is unused here, Fetch
// unpacks one addon folder into dest the way the real providers do.
type fakeProvider struct {
	changelog string
}

func (f *fakeProvider) Check(context.Context, state.SourceRef) (provider.Upstream, error) {
	return provider.Upstream{Version: "2.0.0", Changelog: f.changelog, Strategy: state.StrategyDirect}, nil
}

func (f *fakeProvider) Fetch(_ context.Context, _ state.SourceRef, dest string) error {
	dir := filepath.Join(dest, "WagoAddOn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "WagoAddOn.toc"), []byte("## Interface: 16001\n"), 0o644)
}

// TestInstallUpdateProvider drives the provider install path: Fetch unpacks
// into the staging directory and the state entry keeps its identity while
// gaining the new version, ETag and the provider-reported changelog.
func TestInstallUpdateProvider(t *testing.T) {
	addon := &state.AddonState{
		ID:               "WagoAddOn",
		DisplayName:      "Wago Addon",
		Source:           state.SourceRef{Kind: state.SourceWago, Slug: "VBNBxKx5"},
		Strategy:         state.StrategyDirect,
		Tracking:         "stable",
		InstalledVersion: "1.9.0",
		ETag:             "1.9.0",
	}
	testState(t, addon)

	prov := &fakeProvider{changelog: "## 2.0.0\n\n- fixed a glitch"}
	task := syncTask{flavor: client.FlavorForever, iface: 16001, addon: addon}
	outcome := resolveResult{
		prov: prov,
		plan: resolver.Plan{Strategy: state.StrategyDirect, Version: "2.0.0", Reason: "resolved by the wago provider"},
		upstream: provider.Upstream{
			Version:   "2.0.0",
			Changelog: "## 2.0.0\n\n- fixed a glitch",
			Strategy:  state.StrategyDirect,
		},
	}
	if err := installUpdate(context.Background(), nil, task, outcome, progressSlot{}, &sync.Mutex{}); err != nil {
		t.Fatalf("installUpdate: %v", err)
	}

	st, err := LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	got := st.Client(client.FlavorForever).Addons["WagoAddOn"]
	if got == nil {
		t.Fatal("state lost the addon entry")
	}
	if got.InstalledVersion != "2.0.0" || got.ETag != "2.0.0" {
		t.Errorf("version/etag = %q/%q, want 2.0.0/2.0.0", got.InstalledVersion, got.ETag)
	}
	if got.Changelog != "## 2.0.0\n\n- fixed a glitch" {
		t.Errorf("changelog = %q, want the provider-reported changelog", got.Changelog)
	}
	if got.Source.Slug != "VBNBxKx5" || got.Strategy != state.StrategyDirect {
		t.Errorf("source/strategy = %+v %q, want the wago source kept", got.Source, got.Strategy)
	}
	root := filepath.Join(st.Client(client.FlavorForever).Root, "Interface", "AddOns", "WagoAddOn", "WagoAddOn.toc")
	if _, err := os.Stat(root); err != nil {
		t.Errorf("addon not installed at %s: %v", root, err)
	}
}

// TestResolveOneFailSoft pins the routing contract: per-task problems are
// results, never run failures — manual entries skip with their reason and a
// source kind no provider handles fails that task alone.
func TestResolveOneFailSoft(t *testing.T) {
	manual := &state.AddonState{
		ID: "ManualAddOn", Strategy: state.StrategyManual,
		Source: state.SourceRef{Kind: state.SourceManual, Slug: "ManualAddOn"},
	}
	orphan := &state.AddonState{
		ID:     "CurseAddOn",
		Source: state.SourceRef{Kind: state.SourceKind("curse"), Slug: "8315"},
	}
	skip := resolveOne(context.Background(), nil, syncTask{flavor: client.FlavorForever, addon: manual}, &state.Config{}, progressSlot{})
	if skip.skip == "" || skip.err != nil {
		t.Errorf("manual entry = %+v, want a skip", skip)
	}
	fail := resolveOne(context.Background(), nil, syncTask{flavor: client.FlavorForever, addon: orphan}, &state.Config{}, progressSlot{})
	if fail.err == nil || !strings.Contains(fail.err.Error(), "curse") {
		t.Errorf("orphan = %+v, want a per-addon unsupported-kind error", fail)
	}
}

// failingProvider is a provider whose fetch fails with a canned error.
type failingProvider struct{ err error }

func (f *failingProvider) Check(context.Context, state.SourceRef) (provider.Upstream, error) {
	return provider.Upstream{}, nil
}

func (f *failingProvider) Fetch(context.Context, state.SourceRef, string) error { return f.err }

// TestInstallUpdateProgress drives the provider install path with a
// collector: the unit reports its download and install phases in order
// with the workload frame intact. Terminal completion is decided in Sync's
// outcome loop and is not emitted here.
func TestInstallUpdateProgress(t *testing.T) {
	addon := &state.AddonState{
		ID:               "WagoAddOn",
		DisplayName:      "Wago Addon",
		Source:           state.SourceRef{Kind: state.SourceWago, Slug: "VBNBxKx5"},
		Strategy:         state.StrategyDirect,
		Tracking:         "stable",
		InstalledVersion: "1.9.0",
		ETag:             "1.9.0",
	}
	testState(t, addon)
	task := syncTask{flavor: client.FlavorForever, iface: 16001, addon: addon}
	outcome := resolveResult{
		prov:     &fakeProvider{},
		plan:     resolver.Plan{Strategy: state.StrategyDirect, Version: "2.0.0"},
		upstream: provider.Upstream{Version: "2.0.0", Strategy: state.StrategyDirect},
	}
	var got []ProgressEvent
	slot := progressSlot{
		p:     newProgressSink(func(ev ProgressEvent) { got = append(got, ev) }),
		unit:  Unit{Flavor: client.FlavorForever, AddonID: "WagoAddOn"},
		index: 2,
		total: 3,
	}
	if err := installUpdate(context.Background(), nil, task, outcome, slot, &sync.Mutex{}); err != nil {
		t.Fatalf("installUpdate: %v", err)
	}
	want := []Phase{PhaseDownloading, PhaseInstalling}
	if len(got) != len(want) {
		t.Fatalf("got %d events (%+v), want %d", len(got), got, len(want))
	}
	for i, ev := range got {
		if ev.Phase != want[i] {
			t.Errorf("event %d phase = %q, want %q", i, ev.Phase, want[i])
		}
		if ev.Unit != slot.unit || ev.Index != 2 || ev.Total != 3 {
			t.Errorf("event %d frame = %+v, want %v 2/3", i, ev, slot.unit)
		}
	}
}

// TestInstallUpdateRateLimitEvent pins risk R6 in the download phase: a
// rate-limited fetch reports an explicit "rate limited, …" event instead
// of falling silent, before the terminal failure is decided.
func TestInstallUpdateRateLimitEvent(t *testing.T) {
	addon := &state.AddonState{
		ID:       "WagoAddOn",
		Source:   state.SourceRef{Kind: state.SourceWago, Slug: "VBNBxKx5"},
		Strategy: state.StrategyDirect,
		Tracking: "stable",
	}
	testState(t, addon)
	task := syncTask{flavor: client.FlavorForever, iface: 16001, addon: addon}
	outcome := resolveResult{
		prov: &failingProvider{err: &github.RateLimitError{RetryAfter: time.Minute}},
		plan: resolver.Plan{Strategy: state.StrategyDirect, Version: "2.0.0"},
	}
	var got []ProgressEvent
	slot := progressSlot{
		p:     newProgressSink(func(ev ProgressEvent) { got = append(got, ev) }),
		unit:  Unit{Flavor: client.FlavorForever, AddonID: "WagoAddOn"},
		index: 1,
		total: 1,
	}
	if err := installUpdate(context.Background(), nil, task, outcome, slot, &sync.Mutex{}); err == nil {
		t.Fatal("installUpdate succeeded with a failing fetch")
	}
	if len(got) != 2 {
		t.Fatalf("got %d events (%+v), want downloading + rate-limit", len(got), got)
	}
	if got[0].Phase != PhaseDownloading {
		t.Errorf("event 0 phase = %q, want downloading", got[0].Phase)
	}
	if got[1].Detail.Reason == "" || !strings.Contains(got[1].Detail.Reason, "rate limited") {
		t.Errorf("event 1 reason = %q, want the rate-limit guidance", got[1].Detail.Reason)
	}
	if !github.IsRateLimited(got[1].Detail.Err) {
		t.Errorf("event 1 error = %v, want the rate-limit error", got[1].Detail.Err)
	}
}

// TestCompletionEvent pins the terminal-event mapping: skip and failure
// carry their reason or error, and every completed install reports done
// with the installed version.
func TestCompletionEvent(t *testing.T) {
	tests := []struct {
		name  string
		r     Result
		phase Phase
		check func(t *testing.T, ev ProgressEvent)
	}{
		{"skipped", Result{Kind: ResultSkipped, Skip: "installed by a foreign tool"}, PhaseSkipped, func(t *testing.T, ev ProgressEvent) {
			if ev.Detail.Reason != "installed by a foreign tool" {
				t.Errorf("reason = %q", ev.Detail.Reason)
			}
		}},
		{"failed", Result{Kind: ResultFailed, Err: errors.New("boom")}, PhaseFailed, func(t *testing.T, ev ProgressEvent) {
			if ev.Detail.Err == nil || ev.Detail.Err.Error() != "boom" {
				t.Errorf("error = %v", ev.Detail.Err)
			}
		}},
		{"updated", Result{Kind: ResultUpdated, To: "2.0.0"}, PhaseDone, func(t *testing.T, ev ProgressEvent) {
			if ev.Detail.Version != "2.0.0" {
				t.Errorf("version = %q", ev.Detail.Version)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.r.Flavor, tt.r.AddonID = client.FlavorForever, "Demo"
			ev := completionEvent(tt.r, 3, 7)
			if ev.Phase != tt.phase {
				t.Errorf("phase = %q, want %q", ev.Phase, tt.phase)
			}
			if ev.Unit != (Unit{Flavor: client.FlavorForever, AddonID: "Demo"}) {
				t.Errorf("unit = %+v", ev.Unit)
			}
			if ev.Index != 3 || ev.Total != 7 {
				t.Errorf("index/total = %d/%d, want 3/7", ev.Index, ev.Total)
			}
			tt.check(t, ev)
		})
	}
}

// TestResolveOneResolvingEvent pins the per-unit resolve emission: a unit
// that starts resolving reports exactly one resolving event with its
// workload slot, and a skipped entry reports nothing here (its terminal
// event comes from Sync's outcome loop).
func TestResolveOneResolvingEvent(t *testing.T) {
	gh, err := github.New(github.Options{Offline: true, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("github client: %v", err)
	}
	res := &resolver.Resolver{GH: gh}
	var got []ProgressEvent
	sink := newProgressSink(func(ev ProgressEvent) { got = append(got, ev) })
	task := syncTask{flavor: client.FlavorForever, addon: &state.AddonState{ID: "AddOnA", Source: state.SourceRef{Kind: state.SourceGitHub, Slug: "acme/AddOnA"}}}
	slot := progressSlot{p: sink, unit: Unit{Flavor: client.FlavorForever, AddonID: "AddOnA"}, index: 3, total: 7}
	resolveOne(context.Background(), res, task, &state.Config{}, slot)
	if len(got) != 1 || got[0].Phase != PhaseResolving {
		t.Fatalf("got %d events (%+v), want one resolving event", len(got), got)
	}
	if got[0].Unit != slot.unit || got[0].Index != 3 || got[0].Total != 7 {
		t.Errorf("frame = %+v, want %v 3/7", got[0], slot.unit)
	}
	manual := syncTask{flavor: client.FlavorForever, addon: &state.AddonState{ID: "ManualAddOn", Strategy: state.StrategyManual, Source: state.SourceRef{Kind: state.SourceManual, Slug: "ManualAddOn"}}}
	got = nil
	resolveOne(context.Background(), res, manual, &state.Config{}, progressSlot{p: sink, unit: Unit{Flavor: client.FlavorForever, AddonID: "ManualAddOn"}, index: 1, total: 1})
	if len(got) != 0 {
		t.Errorf("skipped entry reported %d events (%+v), want none", len(got), got)
	}
}

// TestSyncTerminalEventBeforeResult pins the per-unit presentation order:
// a unit's terminal progress event is delivered before its result
// callback, so a terminal renderer finishes the unit's row ("done: 1/1
// …") before the printed result line replaces it.
func TestSyncTerminalEventBeforeResult(t *testing.T) {
	manual := &state.AddonState{
		ID:       "ManualAddOn",
		Strategy: state.StrategyManual,
		Source:   state.SourceRef{Kind: state.SourceManual, Slug: "ManualAddOn"},
	}
	testState(t, manual)
	var order []string
	if _, err := Sync(context.Background(), SyncOptions{},
		func(r Result) { order = append(order, "result:"+string(r.Kind)) },
		func(ev ProgressEvent) { order = append(order, "event:"+string(ev.Phase)) },
	); err != nil {
		t.Fatalf("sync: %v", err)
	}
	want := []string{"event:skipped", "result:skipped"}
	if len(order) != len(want) {
		t.Fatalf("got %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("got %v, want %v", order, want)
		}
	}
}

// TestSyncResultsReportedInReportOrder pins that concurrent units never
// reorder the outcomes: whatever order the workers finish in, onResult is
// called and rep.Results is filled strictly in the deterministic task
// order — the frozen per-addon result lines stream in report order even
// though the work runs in parallel.
func TestSyncResultsReportedInReportOrder(t *testing.T) {
	manual := func(id string) *state.AddonState {
		return &state.AddonState{
			ID:       id,
			Strategy: state.StrategyManual,
			Source:   state.SourceRef{Kind: state.SourceManual, Slug: id},
		}
	}
	testState(t, manual("ManualC"), manual("ManualA"), manual("ManualB"))
	var order []string
	rep, err := Sync(context.Background(), SyncOptions{},
		func(r Result) { order = append(order, r.AddonID) },
		nil,
	)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	want := []string{"ManualA", "ManualB", "ManualC"} // collectTasks sorts by addon id
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("onResult order = %v, want %v", order, want)
	}
	if len(rep.Results) != len(want) {
		t.Fatalf("report has %d results, want %d", len(rep.Results), len(want))
	}
	for i, r := range rep.Results {
		if r.AddonID != want[i] {
			t.Errorf("rep.Results[%d] = %s, want %s", i, r.AddonID, want[i])
		}
	}
}

// TestSyncSkippedProgress drives Sync end to end over a manual entry: no
// unit ever resolves (nothing is downloaded or installed), and the outcome
// loop reports the terminal skipped event with its reason and frame.
func TestSyncSkippedProgress(t *testing.T) {
	manual := &state.AddonState{
		ID:       "ManualAddOn",
		Strategy: state.StrategyManual,
		Source:   state.SourceRef{Kind: state.SourceManual, Slug: "ManualAddOn"},
	}
	testState(t, manual)
	var got []ProgressEvent
	rep, err := Sync(context.Background(), SyncOptions{}, nil, func(ev ProgressEvent) { got = append(got, ev) })
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if rep.Skipped != 1 {
		t.Fatalf("report = %+v, want one skipped unit", rep)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events (%+v), want one terminal event", len(got), got)
	}
	ev := got[0]
	if ev.Phase != PhaseSkipped || ev.Detail.Reason == "" {
		t.Errorf("event = %+v, want skipped with its reason", ev)
	}
	if ev.Unit != (Unit{Flavor: client.FlavorForever, AddonID: "ManualAddOn"}) || ev.Index != 1 || ev.Total != 1 {
		t.Errorf("frame = %+v, want the manual unit 1 of 1", ev)
	}
}

// TestFilterTasksRestrictsRunToOneUnit checks the "update selected addon"
// scope: only the tasks the unit names survive, and a unit that names
// nothing in the workload yields an empty run.
func TestFilterTasksRestrictsRunToOneUnit(t *testing.T) {
	tasks := []syncTask{
		{flavor: client.FlavorForever, addon: &state.AddonState{ID: "Alpha"}},
		{flavor: client.FlavorForever, addon: &state.AddonState{ID: "Beta"}},
		{flavor: client.FlavorClassic, addon: &state.AddonState{ID: "Alpha"}},
	}
	got := filterTasks(tasks, Unit{Flavor: client.FlavorForever, AddonID: "Alpha"})
	if len(got) != 1 || got[0].flavor != client.FlavorForever || got[0].addon.ID != "Alpha" {
		t.Fatalf("filterTasks = %+v, want just Forever/Alpha", got)
	}
	if all := filterTasks(tasks, Unit{}); len(all) != 0 {
		t.Fatalf("filterTasks(zero unit) = %d tasks, want none", len(all))
	}
}

// TestSyncOnlyRunsSelectedUnit drives Sync with Only set over two manual
// entries: exactly the named unit runs (it skips, like any manual entry),
// and its workload frame is 1 of 1 — the other addon is never touched.
func TestSyncOnlyRunsSelectedUnit(t *testing.T) {
	testState(t,
		&state.AddonState{ID: "Alpha", Strategy: state.StrategyManual, Source: state.SourceRef{Kind: state.SourceManual, Slug: "Alpha"}},
		&state.AddonState{ID: "Beta", Strategy: state.StrategyManual, Source: state.SourceRef{Kind: state.SourceManual, Slug: "Beta"}},
	)
	var got []ProgressEvent
	rep, err := Sync(context.Background(), SyncOptions{Only: &Unit{Flavor: client.FlavorForever, AddonID: "Beta"}}, nil,
		func(ev ProgressEvent) { got = append(got, ev) })
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if rep.Skipped != 1 || len(rep.Results) != 1 {
		t.Fatalf("report = %+v, want exactly one skipped unit", rep)
	}
	if r := rep.Results[0]; r.AddonID != "Beta" {
		t.Fatalf("result = %+v, want Beta", r)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events (%+v), want one terminal event", len(got), got)
	}
	if ev := got[0]; ev.Unit != (Unit{Flavor: client.FlavorForever, AddonID: "Beta"}) || ev.Index != 1 || ev.Total != 1 {
		t.Errorf("frame = %+v, want Beta 1 of 1", ev)
	}
}
