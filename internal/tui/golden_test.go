package tui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/bubbletea/v2"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/service"
)

// Golden-terminal regression suite (XD-41): every screen state the P0
// acceptance named — idle table, mid-sync frame (live rows + run frame +
// rate-hint line), failed row persistence, the expanded detail pane, the
// add flow's screens, and the ASCII / NO_COLOR fallbacks — is rendered
// into a golden frame and compared byte for byte, so unapproved visual
// drift fails the suite.
//
// Determinism for CI (no timing flakiness):
//
//   - Fake clock: the fixtures stamp the model with goldenTime instead of
//     time.Now, so the "checked 15:04" header stamp is part of the frame.
//   - Controlled spinner tick: spinner frames advance only through the
//     tick helper (one Update per spinner.TickMsg), so a frame's spinner
//     phase is a pure function of how many ticks the fixture sent.
//   - lipgloss renders styles from the style values alone, so the ANSI in
//     a frame depends only on the capabilities the fixture pins.
//
// Recording workflow (see testdata/golden/README.md): goldens are
// approved, never auto-generated. Record with
//
//	BF_GOLDEN_UPDATE=1 go test ./internal/tui
//
// then review and commit the .golden files. A missing golden fails the
// run with recording instructions; a drifted frame fails with both
// frames and re-records only when you approve it.

// goldenDir holds the golden frames, one .golden file per case.
const goldenDir = "testdata/golden"

// goldenUpdate reports whether the run records (approves) golden frames
// instead of comparing against them.
var goldenUpdate = os.Getenv("BF_GOLDEN_UPDATE") == "1"

// goldenTime is the frozen clock the fixtures stamp into the model: the
// "checked 15:04" stamp is rendered in the header bar, so the goldens pin
// it. The value carries a UTC location, so the stamp renders the same on
// every host regardless of its time zone.
var goldenTime = time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)

// goldenFrame compares one rendered screen frame against its golden file.
func goldenFrame(t *testing.T, name string, m Model) {
	t.Helper()
	goldenContent(t, name, m.View().Content)
}

// goldenContent compares one rendered artifact against its golden file —
// goldenFrame for whole screens, or any other deterministic render (the
// help page) the suite pins.
func goldenContent(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join(goldenDir, name+".golden")
	if goldenUpdate {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %q is not recorded yet — record it with BF_GOLDEN_UPDATE=1 go test ./internal/tui, review it and commit %s: %v",
			name, path, err)
	}
	if content != string(want) {
		t.Errorf("frame %q drifted from its golden %s.\n--- got ---\n%s\n--- want ---\n%s\n"+
			"If the change is intended, approve it with BF_GOLDEN_UPDATE=1 go test ./internal/tui and commit the updated golden.",
			name, path, content, want)
	}
}

// goldenBase builds the standard frame fixture at the given capabilities
// and size: the five display rows carrying the whole status vocabulary,
// their last sync outcomes, a full header bar (flavor, game version,
// root) and the frozen clock stamp.
func goldenBase(t *testing.T, c caps, w, h int) Model {
	t.Helper()
	m := viewTest(t, c, w, h, viewRows())
	m.results = viewResults()
	m.client.Version = "1.60.1"
	m.client.Root = "/games/World of Warcraft/_forever_"
	m.checkedAt = goldenTime
	return m
}

// syncLiveFrame arms the model mid-run the way the service stream would:
// two addons still working (one stalled behind a rate-limit wait, which
// surfaces the wait line), one done and one failed — then one controlled
// spinner tick samples the run state into the rows and the status bar.
func syncLiveFrame(t *testing.T, m Model) Model {
	t.Helper()
	m = beginSync(m)
	m.run.record(service.ProgressEvent{Unit: unit("r1"), Phase: service.PhaseResolving, Index: 1, Total: 5})
	m.run.record(service.ProgressEvent{Unit: unit("r2"), Phase: service.PhaseDownloading, Detail: service.Detail{Reason: "rate limited, retry after 5m"}, Index: 2, Total: 5})
	m.run.record(service.ProgressEvent{Unit: unit("r3"), Phase: service.PhaseDone, Detail: service.Detail{Version: "1.60.1.b2"}, Index: 3, Total: 5})
	m.run.recordResult(service.Result{AddonID: "r3", Kind: service.ResultUpdated, From: "1.60.1.b1", To: "1.60.1.b2"})
	m.run.record(service.ProgressEvent{Unit: unit("r4"), Phase: service.PhaseFailed, Detail: service.Detail{Err: errors.New("cannot resolve owner/weakauras")}, Index: 4, Total: 5})
	m.run.recordResult(service.Result{AddonID: "r4", Kind: service.ResultFailed, Err: errors.New("cannot resolve owner/weakauras")})
	return tick(t, m)
}

// syncFailedFrame is the frame after a run that failed two addons: the
// failed rows stay visible in the failure color with their errors, the
// detail pane carries the selected row's error and plan, and the status
// bar carries the run summary (failed row persistence).
func syncFailedFrame(t *testing.T, m Model) Model {
	t.Helper()
	m.results = map[string]service.Result{
		"r1": {AddonID: "r1", Kind: service.ResultUpToDate, To: "12.1.17"},
		"r2": {AddonID: "r2", Kind: service.ResultUpdated, From: "341", To: "342"},
		"r3": {AddonID: "r3", Kind: service.ResultFailed, Err: errors.New("download failed: 403 rate limited"),
			Plan: resolver.Plan{Reason: "matching release asset"}},
		"r4": {AddonID: "r4", Kind: service.ResultSkipped, Skip: "manual addon"},
		"r5": {AddonID: "r5", Kind: service.ResultFailed, Err: errors.New("no compatible asset for interface 16001")},
	}
	m.statusLine = "0 up to date, 1 updated, 1 manual skipped, 2 failed"
	m.table.SetCursor(2) // the failed row: its error shows in the detail pane
	return m
}

// cancelConfirmFrame is the frame a mid-sync "q" produces: the
// confirmation states that the in-flight install rolls back (P0-1's
// acceptance wording).
func cancelConfirmFrame(t *testing.T, m Model) Model {
	t.Helper()
	m = syncLiveFrame(t, m)
	return press(t, m, keyMsg('q'))
}

// addInputFrame is the add flow mid-typing: the inline input with its
// live validation hint (the parsed reference the add will install).
func addInputFrame(t *testing.T, m Model) Model {
	t.Helper()
	m = press(t, m, keyMsg('a'))
	return typeText(t, m, "https://github.com/owner/repo")
}

// addInvalidFrame is the add flow with an invalid reference: the specific
// parse error shows inline, never ejecting the user to the shell.
func addInvalidFrame(t *testing.T, m Model) Model {
	t.Helper()
	m = press(t, m, keyMsg('a'))
	return typeText(t, m, "not a ref!!")
}

// addLiveFrame is the add flow running: the provisional row carries the
// live-row treatment and the status bar frames the add run.
func addLiveFrame(t *testing.T, m Model) Model {
	t.Helper()
	m.adding = true
	m.cancelSync = func() {}
	m.addRow = &service.AddonRow{ID: "NewAddon", Name: "NewAddon", Installed: "-", Upstream: "-", Strategy: "", Status: "-"}
	m.run = newSyncRun()
	m.run.record(service.ProgressEvent{Unit: unit("NewAddon"), Phase: service.PhaseInstalling, Index: 1, Total: 1})
	return tick(t, m)
}

// addFlavorFrame is the target-flavor prompt with several configured
// clients (P0-4 acceptance): numbered choices with the cursor on the
// active one.
func addFlavorFrame(t *testing.T, m Model) Model {
	t.Helper()
	m = press(t, m, keyMsg('a'))
	m = typeText(t, m, "owner/repo")
	return press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// detailChangelogFrame is the completed detail pane (XD-42): one addon
// carrying the whole A3 story — changelog, plain-language TOC verdict,
// owned dirs and the acquisition source — shown in the expanded detail
// view selection opens (XD-56).
func detailChangelogFrame(t *testing.T, m Model) Model {
	t.Helper()
	row := detailRow()
	m.rows = []service.AddonRow{row}
	m.client.Addons = m.rows
	m.results = nil // no sync result: the TOC verdict is the pane's story
	m.detail = true // enter's dedicated detail view
	m.detailFocus = true
	return m
}

// onboardingBase builds the guided-init fixture (A6): no configured
// client and two detected installs in the picker, with the same pinned
// environment and capabilities as goldenBase. The clock is not pinned:
// the guided-init screen shows no checked stamp (headerBar stamps only a
// configured client), so the wall clock never enters the frame.
func onboardingBase(t *testing.T, c caps, w, h int) Model {
	t.Helper()
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("NO_COLOR", "")
	if !c.color {
		t.Setenv("NO_COLOR", "1")
	}
	m := testModelNoClients(t, Options{ASCII: !c.unicode}, testInstalls()...)
	m.width, m.height = w, h
	return m
}

// onboardingPathFrame is the manual path prompt mid-typing (A6): the
// inline input with its live detection hint — the detected install the
// behind-the-scenes init will record.
func onboardingPathFrame(t *testing.T, c caps, w, h int) Model {
	t.Helper()
	m := onboardingBase(t, c, w, h)
	m.pickerCursor = len(m.picker)
	m.pathInput = true
	m.pathText = "~/Games/World of Warcraft/_forever_"
	m.pathInstall = &client.ClientInstall{Flavor: client.FlavorForever,
		FlavorDir: "/root/Games/World of Warcraft/_forever_",
		Version:   client.Version{Major: 1, Minor: 60, Patch: 1, Build: 58900}, Interface: 16001}
	return m
}

// zeroAddonFrame is the configured-but-zero-addons state (A6): the hint
// panel in place of the empty grid.
func zeroAddonFrame(t *testing.T, m Model) Model {
	t.Helper()
	m.client.Addons = nil
	m.refreshRows()
	return m
}

// TestGoldenFrames pins every screen state above as a golden frame.
func TestGoldenFrames(t *testing.T) {
	color := caps{unicode: true, color: true}
	nocolor := caps{unicode: true, color: false}
	ascii := caps{unicode: false, color: true}
	cases := []struct {
		name      string
		caps      caps
		w, h      int
		twoClient bool
		build     func(t *testing.T, m Model) Model
	}{
		// Idle table and the detail pane: the full list at every width,
		// and the expanded detail view selection opens.
		{"idle-80", color, 80, 28, false, nil},
		{"idle-120", color, 120, 28, false, nil},
		{"detail-expand-80", color, 80, 28, false, func(t *testing.T, m Model) Model {
			m.detail = true
			return m
		}},

		// Mid-sync frame: live rows, run frame, rate-hint wait line;
		// failed row persistence after the run; the cancel wording.
		{"sync-live-100", color, 100, 28, false, syncLiveFrame},
		{"sync-failed-120", color, 120, 28, false, syncFailedFrame},
		{"cancel-confirm-100", color, 100, 28, false, cancelConfirmFrame},

		// The add flow's screens (P0-4).
		{"add-input-100", color, 100, 28, false, addInputFrame},
		{"add-invalid-100", color, 100, 28, false, addInvalidFrame},
		{"add-live-100", color, 100, 28, false, addLiveFrame},
		{"add-flavor-100", color, 100, 28, true, addFlavorFrame},

		// The P1 screens (XD-42 … XD-44), pinned as they land at the
		// central merge: the completed detail pane, the sort cycle, the
		// filter line with its match count and zero-match state, and the
		// full help overlay around the help page.
		{"detail-changelog-120", color, 120, 28, false, detailChangelogFrame},
		{"sort-status-100", color, 100, 28, false, func(t *testing.T, m Model) Model {
			return press(t, m, keyMsg('o'))
		}},
		{"filter-active-100", color, 100, 28, false, func(t *testing.T, m Model) Model {
			m.filter = "owner"
			m.refreshRows()
			return m
		}},
		{"filter-empty-100", color, 100, 28, false, func(t *testing.T, m Model) Model {
			m.filter = "zzz"
			m.refreshRows()
			return m
		}},
		{"help-overlay-100", color, 100, 28, false, func(t *testing.T, m Model) Model {
			m.helpOpen = true
			return m
		}},

		// The guided-init screens and the zero-addons hint panel (A6).
		// The build funcs replace the configured fixture with the
		// no-client one (onboardingBase), so the frame carries no
		// configured client at all.
		{"onboard-picker-100", color, 100, 28, false, func(t *testing.T, _ Model) Model {
			return onboardingBase(t, color, 100, 28)
		}},
		{"onboard-picker-80-ascii", ascii, 80, 28, false, func(t *testing.T, _ Model) Model {
			return onboardingBase(t, ascii, 80, 28)
		}},
		{"onboard-path-100", color, 100, 28, false, func(t *testing.T, _ Model) Model {
			return onboardingPathFrame(t, color, 100, 28)
		}},
		{"zero-addons-100", color, 100, 28, false, zeroAddonFrame},

		// NO_COLOR variants: no color codes, every word intact.
		{"idle-80-nocolor", nocolor, 80, 28, false, nil},
		{"sync-live-100-nocolor", nocolor, 100, 28, false, syncLiveFrame},

		// ASCII variants: the whole frame degrades to the ASCII set.
		{"idle-80-ascii", ascii, 80, 28, false, nil},
		{"detail-expand-80-ascii", ascii, 80, 28, false, func(t *testing.T, m Model) Model {
			m.detail = true
			return m
		}},
		{"sync-live-100-ascii", ascii, 100, 28, false, syncLiveFrame},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m Model
			if tc.twoClient {
				m = goldenTwoClients(t, tc.caps, tc.w, tc.h)
			} else {
				m = goldenBase(t, tc.caps, tc.w, tc.h)
			}
			if tc.build != nil {
				m = tc.build(t, m)
			}
			goldenFrame(t, tc.name, m)
		})
	}
}

// TestGoldenHelpPage pins the full help page — every keybinding grouped by
// context. This is the content contract the help overlay shows; the
// overlay around this page is pinned by the help-overlay-100 case in
// TestGoldenFrames (XD-44).
func TestGoldenHelpPage(t *testing.T) {
	m := goldenBase(t, caps{unicode: true, color: true}, 100, 28)
	m.help.SetWidth(100)
	goldenContent(t, "help-full-100", m.help.FullHelpView(m.keys.FullHelp()))
}

// goldenTwoClients builds a frame fixture with two configured clients
// (classic and forever) for the target-flavor prompt, with the same
// pinned environment and clock as goldenBase.
func goldenTwoClients(t *testing.T, c caps, w, h int) Model {
	t.Helper()
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("NO_COLOR", "")
	if !c.color {
		t.Setenv("NO_COLOR", "1")
	}
	m := testModelTwoClients(t)
	m.client.Root = "/games/World of Warcraft/_classic_"
	m.width, m.height = w, h
	m.checkedAt = goldenTime
	return m
}
