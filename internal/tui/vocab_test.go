package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/itp5b5/blessforge/internal/service"
)

// The display vocabulary audit (XD-48): the TUI labels progress states
// with the shared service.Phase words and settled outcomes with the shared
// result words — the same labels the CLI live block prints — so the two
// surfaces can never drift apart. These tests pin the vocabulary the UX
// contract in docs/bfctl.1 freezes.

// TestPhaseLabelsAreTheServicePhaseWords pins the progress vocabulary:
// the terminal event's label is the service.Phase word itself ("done",
// "failed", "skipped" — the same strings the CLI live block and the pipe
// lines print), and the live cell leads with the in-flight phase word.
func TestPhaseLabelsAreTheServicePhaseWords(t *testing.T) {
	for _, p := range []service.Phase{service.PhaseDone, service.PhaseFailed, service.PhaseSkipped} {
		if got := terminalText(liveRow{phase: p}); got != string(p) {
			t.Errorf("terminalText(%s) = %q, want the phase word %q", p, got, p)
		}
		l := liveRow{phase: p, detail: service.Detail{Version: "12.1.18", Reason: "manual addon", Err: errors.New("boom")}}
		if got := terminalText(l); !strings.HasPrefix(got, string(p)+": ") {
			t.Errorf("terminalText(%s) = %q, want the phase word %q as its label", p, got, p)
		}
	}
	m := beginSync(testModel(t))
	for _, p := range []service.Phase{service.PhaseResolving, service.PhaseDownloading, service.PhaseInstalling} {
		m.live.rows["DemoAddon"] = liveRow{phase: p}
		a := service.AddonRow{ID: "DemoAddon"}
		if got := m.statusText(a); !strings.HasSuffix(got, string(p)) {
			t.Errorf("statusText(%s) = %q, want the phase word %q as its label", p, got, p)
		}
	}
}

// TestResultLabelsAreTheSharedResultWords pins the settled vocabulary:
// one label per outcome, the words the frozen result lines carry — "up to
// date", "updated <from> <arrow> <to>", "installed <to>", "update
// available", "available to install", "skipped[: <reason>]", "failed:
// <error>".
func TestResultLabelsAreTheSharedResultWords(t *testing.T) {
	m := testModel(t)
	cases := []struct {
		r    service.Result
		want string
	}{
		{service.Result{Kind: service.ResultUpToDate, To: "12.1.18"}, "up to date"},
		{service.Result{Kind: service.ResultUpdated, From: "12.1.17", To: "12.1.18"}, "updated 12.1.17 → 12.1.18"},
		{service.Result{Kind: service.ResultUpdated, From: "-", To: "342"}, "installed 342"},
		{service.Result{Kind: service.ResultAvailable, From: "341", To: "342"}, "update available"},
		{service.Result{Kind: service.ResultAvailable, From: "-", To: "2.0.0"}, "available to install"},
		{service.Result{Kind: service.ResultSkipped, Skip: "manual addon"}, "skipped: manual addon"},
		{service.Result{Kind: service.ResultSkipped}, "skipped"},
		{service.Result{Kind: service.ResultFailed, Err: errors.New("boom")}, "failed: boom"},
		{service.Result{Kind: service.ResultFailed}, "failed"},
	}
	for _, tc := range cases {
		if got := m.outcomeText(tc.r); got != tc.want {
			t.Errorf("outcomeText(%s) = %q, want %q", tc.r.Kind, got, tc.want)
		}
	}
}
