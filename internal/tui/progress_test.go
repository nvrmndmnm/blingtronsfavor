package tui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbletea/v2"

	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/service"
)

// resize sends one window size through Update.
func resize(t *testing.T, m Model, width, height int) Model {
	t.Helper()
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return next.(Model)
}

// tick sends one spinner tick through Update — the moment a running sync
// samples its live state into the rows and the status bar.
func tick(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(spinner.TickMsg{})
	return next.(Model)
}

// beginSync arms the model for a sync run without starting any service
// work: the tests write the run state directly, exactly as the service
// layer's progress and result callbacks would.
func beginSync(m Model) Model {
	m.syncing = true
	m.run = newSyncRun()
	m.live = liveState{}
	m.wait = ""
	m.statusLine = ""
	return m
}

// unit names one progress event's unit.
func unit(id string) service.Unit {
	return service.Unit{AddonID: id}
}

// rowText returns the Status cell the addon's row shows.
func rowText(t *testing.T, m Model, id string) string {
	t.Helper()
	for _, a := range m.rows {
		if a.ID == id {
			return m.statusText(a)
		}
	}
	t.Fatalf("no visible row for %s", id)
	return ""
}

func TestSyncRowsShowLivePhasesWithSpinner(t *testing.T) {
	m := testModel(t)
	m = resize(t, m, 100, 40)
	m = beginSync(m)

	// Every addon row is live at once, each with its own spinner.
	m.run.record(service.ProgressEvent{Unit: unit("DemoAddon"), Phase: service.PhaseResolving, Index: 1, Total: 2})
	m.run.record(service.ProgressEvent{Unit: unit("OtherAddon"), Phase: service.PhaseResolving, Index: 2, Total: 2})
	m = tick(t, m)
	frame := strings.TrimSpace(m.spin.View())
	for _, id := range []string{"DemoAddon", "OtherAddon"} {
		cell := rowText(t, m, id)
		if !strings.Contains(cell, string(service.PhaseResolving)) {
			t.Fatalf("%s status cell = %q, want the live phase", id, cell)
		}
		if !strings.Contains(cell, frame) {
			t.Fatalf("%s status cell = %q, want the per-row spinner %q", id, cell, frame)
		}
	}

	// The row names the real work as the unit moves on: each phase
	// replaces the last in place.
	for _, phase := range []service.Phase{service.PhaseDownloading, service.PhaseInstalling} {
		m.run.record(service.ProgressEvent{Unit: unit("DemoAddon"), Phase: phase, Index: 1, Total: 2})
		m = tick(t, m)
		if cell := rowText(t, m, "DemoAddon"); !strings.Contains(cell, string(phase)) {
			t.Fatalf("status cell = %q, want live phase %q", cell, phase)
		}
	}
	content := m.View().Content
	if !strings.Contains(content, string(service.PhaseInstalling)) {
		t.Fatalf("view is missing the live phase:\n%s", content)
	}
	if strings.Contains(content, string(service.PhaseDownloading)) {
		t.Fatalf("the finished phase was left behind:\n%s", content)
	}
}

func TestSyncRowsFlipInPlaceToOutcomes(t *testing.T) {
	m := testModel(t)
	m = resize(t, m, 100, 40)
	m = beginSync(m)
	m.run.record(service.ProgressEvent{Unit: unit("DemoAddon"), Phase: service.PhaseResolving, Index: 1, Total: 2})
	m.run.record(service.ProgressEvent{Unit: unit("OtherAddon"), Phase: service.PhaseResolving, Index: 2, Total: 2})
	m = tick(t, m)

	// Outcomes land in the rows — in the CLI live block's words — as the
	// report order publishes them, in place.
	m.run.record(service.ProgressEvent{Unit: unit("DemoAddon"), Phase: service.PhaseDone, Detail: service.Detail{Version: "1.1.0"}, Index: 1, Total: 2})
	m.run.recordResult(service.Result{AddonID: "DemoAddon", Kind: service.ResultUpdated, From: "1.0.0", To: "1.1.0"})
	m.run.record(service.ProgressEvent{Unit: unit("OtherAddon"), Phase: service.PhaseDone, Detail: service.Detail{Version: "2.0.0"}, Index: 2, Total: 2})
	m.run.recordResult(service.Result{AddonID: "OtherAddon", Kind: service.ResultUpToDate, To: "2.0.0"})
	m = tick(t, m)

	if cell := rowText(t, m, "DemoAddon"); cell != "updated 1.0.0 → 1.1.0" {
		t.Fatalf("status cell = %q, want %q", cell, "updated 1.0.0 → 1.1.0")
	}
	if cell := rowText(t, m, "OtherAddon"); cell != "up to date" {
		t.Fatalf("status cell = %q, want %q", cell, "up to date")
	}

	// Rows never reorder mid-run.
	content := m.View().Content
	if di, oi := strings.Index(content, "DemoAddon"), strings.Index(content, "OtherAddon"); di < 0 || oi < 0 || di > oi {
		t.Fatalf("rows reordered: DemoAddon at %d, OtherAddon at %d", di, oi)
	}
}

func TestStatusBarShowsRunFrame(t *testing.T) {
	m := testModel(t)
	m = resize(t, m, 100, 40)
	m = beginSync(m)
	m.run.record(service.ProgressEvent{Unit: unit("DemoAddon"), Phase: service.PhaseDone, Detail: service.Detail{Version: "1.1.0"}, Index: 1, Total: 4})
	m.run.record(service.ProgressEvent{Unit: unit("OtherAddon"), Phase: service.PhaseFailed, Detail: service.Detail{Err: errors.New("boom")}, Index: 2, Total: 4})
	m = tick(t, m)
	if want := "syncing 2/4 · 1 done · 1 failed"; !strings.Contains(m.View().Content, want) {
		t.Fatalf("status bar is missing the run frame %q:\n%s", want, m.View().Content)
	}
}

func TestRateLimitWaitShowsHintLine(t *testing.T) {
	m := testModel(t)
	m = resize(t, m, 100, 40)
	m = beginSync(m)
	m.run.record(service.ProgressEvent{
		Unit:   unit("DemoAddon"),
		Phase:  service.PhaseResolving,
		Detail: service.Detail{Reason: "rate limited, retry after 5m", Err: errors.New("rate limited")},
		Index:  1,
		Total:  2,
	})
	m = tick(t, m)
	if want := "rate limited, retry after 5m"; !strings.Contains(m.View().Content, want) {
		t.Fatalf("view is missing the rate-limit wait line %q:\n%s", want, m.View().Content)
	}

	// The wait line goes away when the unit moves on.
	m.run.record(service.ProgressEvent{Unit: unit("DemoAddon"), Phase: service.PhaseDownloading, Index: 1, Total: 2})
	m = tick(t, m)
	if strings.Contains(m.View().Content, "retry after 5m") {
		t.Fatalf("the wait line outlived the wait:\n%s", m.View().Content)
	}
}

func TestFailedRowKeepsErrorAfterRun(t *testing.T) {
	m := testModel(t)
	m = resize(t, m, 100, 40)
	m = beginSync(m)
	boom := errors.New("release asset vanished")
	m.run.record(service.ProgressEvent{Unit: unit("DemoAddon"), Phase: service.PhaseFailed, Detail: service.Detail{Err: boom}, Index: 1, Total: 2})
	m.run.recordResult(service.Result{
		AddonID: "DemoAddon",
		Kind:    service.ResultFailed,
		Err:     boom,
		Plan:    resolver.Plan{Reason: "release asset picked"},
	})
	m = tick(t, m)
	if cell := rowText(t, m, "DemoAddon"); cell != "failed: release asset vanished" {
		t.Fatalf("status cell = %q, want the failure words", cell)
	}

	// The row stays visibly failed after the run, error and all.
	next, _ := m.Update(syncDoneMsg{report: service.Report{Failed: 1}})
	m = next.(Model)
	if cell := rowText(t, m, "DemoAddon"); cell != "failed: release asset vanished" {
		t.Fatalf("status cell after the run = %q, want the failure words", cell)
	}

	// The detail pane carries the error text and the plan reason.
	m.detail = true
	content := m.View().Content
	for _, want := range []string{"release asset vanished", "release asset picked"} {
		if !strings.Contains(content, want) {
			t.Fatalf("detail pane is missing %q:\n%s", want, content)
		}
	}
}

func TestCancelExplainsRollback(t *testing.T) {
	m := testModel(t)
	m.syncing = true
	m.cancelSync = func() {}
	next, _ := m.cancelOrQuit()
	m = next.(Model)
	if want := "cancelling — the in-flight install rolls back"; !strings.Contains(m.statusLine, want) {
		t.Fatalf("statusLine = %q, want the rollback explanation", m.statusLine)
	}
	if m.quitting {
		t.Fatal("ctrl+c quit instead of cancelling the sync")
	}
}
