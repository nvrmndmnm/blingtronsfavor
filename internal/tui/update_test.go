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

func TestCancelOrQuitCancelsSyncFirst(t *testing.T) {
	m := testModel(t)
	cancelled := false
	m.syncing = true
	m.cancelSync = func() { cancelled = true }

	next, _ := m.cancelOrQuit()
	m = next.(Model)
	if !cancelled {
		t.Fatal("quit key did not cancel the running sync")
	}
	if m.quitting {
		t.Fatal("quit key quit while a sync was still rolling back")
	}

	m.syncing = false
	next, _ = m.cancelOrQuit()
	m = next.(Model)
	if !m.quitting {
		t.Fatal("quit key did not quit once no sync was running")
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

// firstNote reports the number of the topmost changelog "line-NN" marker
// visible in the frame — the detail pane's scroll position in disguise.
func firstNote(t *testing.T, m Model) int {
	t.Helper()
	const prefix = "line-"
	frame := m.View().Content
	i := strings.Index(frame, prefix)
	if i < 0 {
		t.Fatalf("no changelog line is visible:\n%s", frame)
	}
	n := 0
	for _, r := range frame[i+len(prefix):] {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// TestEnterFocusesDetailPaneAndScrolls pins the A3 interaction: enter
// focuses the detail pane, the scroll keys drive it (the table cursor
// stays put) and clamp at the end of the content, a cursor move resets the
// scroll, and enter or esc returns to the list.
func TestEnterFocusesDetailPaneAndScrolls(t *testing.T) {
	notes := ""
	for i := 1; i <= 40; i++ {
		notes += "line-" + string(rune('0'+i/10)) + string(rune('0'+i%10)) + "\n"
	}
	rows := []service.AddonRow{
		{ID: "r1", Name: "LongNotes", Installed: "1.0.0", Upstream: "1.0.0", Strategy: "direct", Tracking: "stable", Status: "-", Changelog: notes},
		{ID: "r2", Name: "Other", Installed: "2.0.0", Upstream: "2.0.0", Strategy: "direct", Tracking: "stable", Status: "-"},
	}
	m := viewTest(t, caps{unicode: true, color: true}, 120, 28, rows)

	// Without focus the navigation keys move the table cursor as always.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.table.Cursor() != 1 {
		t.Fatalf("cursor = %d, want 1", m.table.Cursor())
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})

	// enter focuses the pane.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.detailFocus || !m.detail {
		t.Fatal("enter should focus the detail pane")
	}

	// down scrolls one content line and leaves the cursor alone.
	first := firstNote(t, m)
	for i := 0; i < 5; i++ {
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.table.Cursor() != 0 {
		t.Fatalf("cursor = %d, want 0 — the scroll keys must not move the table", m.table.Cursor())
	}
	if m.detailScroll != 5 {
		t.Fatalf("detailScroll = %d, want 5", m.detailScroll)
	}
	if got := firstNote(t, m); got != first+5 {
		t.Fatalf("topmost changelog line = %d, want %d", got, first+5)
	}

	// pgdn steps a page and clamps at the end of the content.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyPgDn})
	if m.detailScroll != m.detailScrollMax() {
		t.Fatalf("detailScroll = %d, want the clamp at %d", m.detailScroll, m.detailScrollMax())
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyPgDn})
	if m.detailScroll != m.detailScrollMax() {
		t.Fatalf("detailScroll = %d, scrolling ran past the content", m.detailScroll)
	}

	// enter returns to the list; the pane closes with the focus.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.detailFocus || m.detail {
		t.Fatal("enter should return to the table")
	}

	// A cursor move resets the scroll for the next addon; unfocusing keeps
	// the place until then.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // focus (scroll back to 0)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})  // scroll = 1
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // back to the list
	if m.detailScroll != 1 {
		t.Fatalf("detailScroll = %d, want the place kept while unfocused", m.detailScroll)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown}) // the cursor moves
	if m.detailScroll != 0 {
		t.Fatalf("detailScroll = %d, want 0 after the cursor moved", m.detailScroll)
	}

	// esc drops the detail engagement like enter's toggle.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.detailFocus || m.detail {
		t.Fatal("esc should drop the detail pane")
	}
}
