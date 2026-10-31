package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbletea/v2"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
)

// testModel points bfctl's config and cache at fresh temp directories with
// one forever client and two addons, and builds the initial model.
func testModel(t *testing.T) Model {
	return testModelOpts(t, Options{})
}

// testModelOpts is testModel with explicit TUI options (the --ascii flag
// path). The locale is pinned to UTF-8 so the theme's glyph set is
// deterministic regardless of the host environment; the --ascii flag (and
// viewTest's own env setup) exercises the fallbacks.
func testModelOpts(t *testing.T, opts Options) Model {
	t.Helper()
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	if err := paths.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	path, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	st := state.New()
	st.Clients[client.FlavorForever] = &state.ClientState{
		Flavor: client.FlavorForever,
		Root:   filepath.Join(t.TempDir(), "_forever_"),
		Addons: map[string]*state.AddonState{
			"DemoAddon":  {ID: "DemoAddon", Source: state.SourceRef{Kind: state.SourceGitHub, Slug: "owner/repo"}, Tracking: "stable", InstalledVersion: "1.0.0"},
			"OtherAddon": {ID: "OtherAddon", Source: state.SourceRef{Kind: state.SourceGitHub, Slug: "owner/other"}, Tracking: "stable", InstalledVersion: "2.0.0"},
		},
	}
	if err := state.Open(path).Save(st); err != nil {
		t.Fatal(err)
	}
	return NewModel(context.Background(), opts, &runtime{})
}

// keyMsg builds one printable key press.
func keyMsg(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Text: string(code)}
}

// escKey builds the esc press.
func escKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEsc}
}

// ctrlCKey builds the ctrl+c press.
func ctrlCKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
}

// press sends one key through Update and returns the resulting model.
func press(t *testing.T, m Model, msg tea.KeyPressMsg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

func TestFilterModeCapturesKeys(t *testing.T) {
	m := testModel(t)
	if len(m.rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(m.rows))
	}
	m = press(t, m, keyMsg('/'))
	if !m.filtering {
		t.Fatal("pressing / did not enter filter mode")
	}
	for _, r := range "dem" {
		m = press(t, m, keyMsg(r))
	}
	if m.confirm {
		t.Fatal("typing in filter mode triggered the delete confirmation")
	}
	if len(m.rows) != 1 || m.rows[0].ID != "DemoAddon" {
		t.Fatalf("rows = %+v, want only DemoAddon", m.rows)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.filtering || m.filter != "" {
		t.Fatalf("esc did not clear the filter (filtering=%v filter=%q)", m.filtering, m.filter)
	}
}

func TestDeleteConfirmFlow(t *testing.T) {
	m := testModel(t)
	m = press(t, m, keyMsg('d'))
	if !m.confirm {
		t.Fatal("pressing d did not ask for confirmation")
	}
	m = press(t, m, keyMsg('n'))
	if m.confirm {
		t.Fatal("n did not cancel the confirmation")
	}
	st, err := service.LoadStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Addons) != 2 {
		t.Fatalf("addons after cancelled delete = %d, want 2", len(st.Addons))
	}

	m = press(t, m, keyMsg('d'))
	m = press(t, m, keyMsg('y'))
	if m.confirm {
		t.Fatal("y did not confirm the delete")
	}
	// The delete itself runs as a service command; the model only dispatches.
	if !strings.Contains(m.statusLine, "deleting") {
		t.Fatalf("statusLine = %q, want a deleting message", m.statusLine)
	}
}

func TestQuitKeyQuits(t *testing.T) {
	m := testModel(t)
	m = press(t, m, keyMsg('q'))
	if !m.quitting {
		t.Fatal("q did not quit")
	}
}

func TestQuitMidSyncAsksFirstAndStatesRollback(t *testing.T) {
	m := testModel(t)
	cancelled := false
	m.syncing = true
	m.cancelSync = func() { cancelled = true }

	m = press(t, m, keyMsg('q'))
	if cancelled {
		t.Fatal("q cancelled the running sync without asking")
	}
	if m.quitting {
		t.Fatal("q quit while a sync was in flight")
	}
	if !strings.Contains(m.quitAsk, "rolls back") {
		t.Fatalf("quitAsk = %q, want the rollback stated", m.quitAsk)
	}

	// esc withdraws the question and leaves the sync running.
	m = press(t, m, escKey())
	if m.quitAsk != "" || cancelled {
		t.Fatal("esc did not withdraw the quit question cleanly")
	}

	// Asking again and confirming quits — and only then rolls the sync back.
	m = press(t, m, keyMsg('q'))
	m = press(t, m, keyMsg('y'))
	if !cancelled {
		t.Fatal("y did not cancel the sync to roll the in-flight install back")
	}
	if !m.quitting {
		t.Fatal("y did not quit after the confirmation")
	}
}

func TestCtrlCMidSyncCancelsThenOffersQuit(t *testing.T) {
	m := testModel(t)
	cancelled := false
	m.syncing = true
	m.cancelSync = func() { cancelled = true }

	m = press(t, m, ctrlCKey())
	if !cancelled {
		t.Fatal("ctrl+c did not cancel the running sync")
	}
	if m.quitting {
		t.Fatal("ctrl+c quit without offering")
	}
	if !strings.Contains(m.quitAsk, "rolls back") {
		t.Fatalf("quitAsk = %q, want the rollback stated", m.quitAsk)
	}

	m = press(t, m, keyMsg('y'))
	if !m.quitting {
		t.Fatal("y did not accept the quit offer")
	}
}

func TestCtrlCWhileIdleQuits(t *testing.T) {
	m := testModel(t)
	m = press(t, m, ctrlCKey())
	if !m.quitting {
		t.Fatal("ctrl+c while idle did not quit")
	}
}

func TestHelpKeyOpensAndClosesOverlay(t *testing.T) {
	m := testModel(t)
	m = press(t, m, keyMsg('?'))
	if !m.helpOpen {
		t.Fatal("? did not open the help overlay")
	}
	m = press(t, m, keyMsg('?'))
	if m.helpOpen {
		t.Fatal("? did not close the help overlay")
	}
	m = press(t, m, keyMsg('?'))
	m = press(t, m, escKey())
	if m.helpOpen {
		t.Fatal("esc did not close the help overlay")
	}
}

func TestHelpOverlayHandsQuitToTheQuitKeys(t *testing.T) {
	// Idle, q quits straight from the overlay.
	m := testModel(t)
	m = press(t, m, keyMsg('?'))
	m = press(t, m, keyMsg('q'))
	if !m.quitting || m.helpOpen {
		t.Fatalf("q in the help overlay: quitting=%v helpOpen=%v", m.quitting, m.helpOpen)
	}

	// Mid-sync, q closes the overlay and asks first — the question has to
	// be visible on the normal screen.
	m = testModel(t)
	cancelled := false
	m.syncing = true
	m.cancelSync = func() { cancelled = true }
	m = press(t, m, keyMsg('?'))
	m = press(t, m, keyMsg('q'))
	if m.helpOpen || m.quitAsk == "" || cancelled || m.quitting {
		t.Fatalf("q over the help mid-sync: helpOpen=%v quitAsk=%q cancelled=%v quitting=%v",
			m.helpOpen, m.quitAsk, cancelled, m.quitting)
	}
}

func TestTrackKeyStillToggles(t *testing.T) {
	m := testModel(t)
	next, cmd := m.Update(keyMsg('s'))
	if cmd == nil {
		t.Fatal("s did not route to the tracking toggle")
	}
	if n := next.(Model); n.confirm || n.quitAsk != "" || n.helpOpen || n.filtering {
		t.Fatal("s opened a mode instead of toggling tracking")
	}
}

func TestViewRendersBadgeAndColumns(t *testing.T) {
	m := testModel(t)
	content := m.View().Content
	for _, want := range []string{"Forever", "Addon", "Installed", "Upstream", "Strategy", "Status"} {
		if !strings.Contains(content, want) {
			t.Fatalf("view is missing %q:\n%s", want, content)
		}
	}
}
