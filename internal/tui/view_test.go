package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nvrmndmnm/blingtronsfavor/internal/service"
)

// Screen-level acceptance tests for the visual language: the responsive
// layout, the expanded detail view, the color/glyph rules and the
// header/status/help zones.

// viewTest builds a model with the given terminal capabilities at the given
// size, injecting display rows so tests can pin exact values.
func viewTest(t *testing.T, c caps, w, h int, rows []service.AddonRow) Model {
	t.Helper()
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("NO_COLOR", "")
	if !c.color {
		t.Setenv("NO_COLOR", "1")
	}
	m := testModelOpts(t, Options{ASCII: !c.unicode})
	m.width, m.height = w, h
	if rows != nil {
		m.rows = rows
		m.client.Addons = rows
	}
	return m
}

// viewRows builds rows whose status words come from sync results, covering
// the whole status vocabulary.
func viewRows() []service.AddonRow {
	rows := []service.AddonRow{
		{ID: "r1", Name: "DeadlyBossMods", Installed: "12.1.17", Upstream: "12.1.17", Strategy: "release", Tracking: "stable", Status: "-", Source: "owner/repo", SourceKind: "github"},
		{ID: "r2", Name: "auctionator", Installed: "341", Upstream: "342", Strategy: "release", Tracking: "stable", Status: "-", Source: "owner/auction", SourceKind: "github"},
		{ID: "r3", Name: "rarescanner", Installed: "1.60.1.b1", Upstream: "1.60.1.b2", Strategy: "direct", Tracking: "stable", Status: "-", Source: "wago:rarescanner", SourceKind: "wago"},
		{ID: "r4", Name: "WeakAuras", Installed: "5.19.5", Upstream: "5.19.5", Strategy: "source-pkgmeta", Tracking: "stable", Status: "-", Source: "owner/weakauras", SourceKind: "github"},
		{ID: "r5", Name: "Details-Damage-Meter", Installed: "d1e8ad72", Upstream: "-", Strategy: "direct", Tracking: "branch: main", Status: "-", Source: "owner/details", SourceKind: "github"},
	}
	return rows
}

// viewResults pins one sync outcome per row so the status words are
// deterministic.
func viewResults() map[string]service.Result {
	return map[string]service.Result{
		"r1": {AddonID: "r1", Kind: service.ResultUpToDate},
		"r2": {AddonID: "r2", Kind: service.ResultAvailable, To: "342"},
		"r3": {AddonID: "r3", Kind: service.ResultFailed},
		"r4": {AddonID: "r4", Kind: service.ResultSkipped},
		"r5": {AddonID: "r5", Kind: service.ResultUpdated, From: "d1e8ad72", To: "f00ba421"},
	}
}

func TestLayoutHoldsAtAcceptanceSizes(t *testing.T) {
	for _, w := range []int{60, 80, 100, 120, 200} {
		m := viewTest(t, caps{unicode: true, color: true}, w, 28, viewRows())
		m.results = viewResults()
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) != 28 {
			t.Fatalf("width %d: frame has %d lines, want 28", w, len(lines))
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got > w {
				t.Fatalf("width %d: line %d is %d cells wide:\n%q", w, i, got, line)
			}
		}
	}
}

func TestDetailsHiddenByDefaultAndExpandedOnSelection(t *testing.T) {
	rows := viewRows()

	// On startup and idle the full list is on screen and the details are
	// hidden — at every width, wide terminals included (XD-56).
	for _, w := range []int{80, 120} {
		m := viewTest(t, caps{unicode: true, color: true}, w, 28, rows)
		if strings.Contains(m.View().Content, "Tracking") {
			t.Fatalf("width %d: the idle frame shows the detail pane", w)
		}
	}

	// Selecting an addon opens the dedicated detail view: the list keeps
	// only its top rows and the details take the remaining space.
	m := viewTest(t, caps{unicode: true, color: true}, 120, 28, rows)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	content := m.View().Content
	if !strings.Contains(content, "Tracking") {
		t.Fatal("enter should open the expanded detail pane")
	}
	lines := strings.Split(content, "\n")
	if !strings.Contains(lines[1], "Addon") {
		t.Fatalf("the list should keep its header row, got %q", lines[1])
	}
	boxTop := ansi.Strip(lines[1+detailListRows+1])
	if !strings.HasPrefix(boxTop, m.theme.box.TopLeft) {
		t.Fatalf("the detail box should start right below the list's top %d rows, got %q", detailListRows, boxTop)
	}

	// esc dismisses the pane and restores the full list.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if strings.Contains(m.View().Content, "Tracking") {
		t.Fatal("esc should restore the full list without the detail pane")
	}
}

func TestTruncatedCellsRevealFullValueInDetailPane(t *testing.T) {
	const longName = "VeryLongAddonNameThatOverflowsCells"
	const longVersion = "12.1.17-beta.9+build.20261030"
	rows := []service.AddonRow{
		{ID: "r1", Name: longName, Installed: longVersion, Upstream: longVersion, Strategy: "release", Tracking: "stable", Status: "-", Source: "owner/repo", SourceKind: "github"},
	}

	// Wide: the expanded detail pane holds the full values.
	m := viewTest(t, caps{unicode: true, color: true}, 120, 28, rows)
	m.detail = true
	content := m.View().Content
	if !strings.Contains(content, longName) {
		t.Fatal("detail pane should show the full addon name")
	}
	if n := strings.Count(content, longName); n != 1 {
		t.Fatalf("full name appears %d times, want 1 (the table cell should be truncated)", n)
	}
	if !strings.Contains(content, longVersion) {
		t.Fatal("detail pane should show the full installed version")
	}

	// Narrow: the same expanded pane holds the same full values.
	m = viewTest(t, caps{unicode: true, color: true}, 80, 28, rows)
	m.detail = true
	content = m.View().Content
	if !strings.Contains(content, longName) {
		t.Fatal("detail pane should show the full addon name")
	}
	if n := strings.Count(content, longName); n != 1 {
		t.Fatalf("full name appears %d times, want 1 (the table cell should be truncated)", n)
	}
	if !strings.Contains(content, longVersion) {
		t.Fatal("detail pane should show the full installed version")
	}
}

func TestASCIIModeDegradesAllGlyphs(t *testing.T) {
	assertASCIIOnly := func(t *testing.T, frame string) {
		t.Helper()
		for i, line := range strings.Split(frame, "\n") {
			for _, r := range line {
				if r > 127 {
					t.Fatalf("ASCII mode: line %d contains non-ASCII rune %q:\n%q", i, r, line)
				}
			}
		}
	}

	// The full list (details hidden) renders every row, and the ASCII
	// status glyphs carry their words.
	m := viewTest(t, caps{unicode: false, color: false}, 100, 28, viewRows())
	m.results = viewResults()
	content := m.View().Content
	assertASCIIOnly(t, content)
	for _, want := range []string{"+ up to date", "! update available", "x failed", "* skipped", "+ updated"} {
		if !strings.Contains(content, want) {
			t.Errorf("ASCII mode: frame is missing %q", want)
		}
	}

	// The expanded detail pane degrades to ASCII too — borders included.
	// (Only the list's top rows stay on screen here, so the status words
	// above are asserted on the full list only.)
	m.detail = true
	assertASCIIOnly(t, m.View().Content)
}

func TestColorNeverCarriesMeaningAlone(t *testing.T) {
	for _, c := range []caps{{unicode: true, color: true}, {unicode: true, color: false}} {
		m := viewTest(t, c, 100, 28, viewRows())
		m.results = viewResults()
		content := m.View().Content
		for _, want := range []string{"up to date", "update available", "failed", "skipped"} {
			if !strings.Contains(content, want) {
				t.Errorf("caps %+v: frame is missing the status word %q", c, want)
			}
		}
	}
}

func TestHeaderShowsFlavorVersionRootAndCounts(t *testing.T) {
	m := viewTest(t, caps{unicode: true, color: true}, 100, 28, viewRows())
	m.results = viewResults()
	m.client.Version = "1.60.1"
	m.client.Root = "/games/World of Warcraft/_forever_"
	header := strings.Split(m.View().Content, "\n")[0]
	for _, want := range []string{"Forever", "1.60.1", "_forever_", "5 addons", "1 update available"} {
		if !strings.Contains(header, want) {
			t.Errorf("header %q is missing %q", header, want)
		}
	}
}

func TestStatusAndHelpAreSeparateZones(t *testing.T) {
	m := viewTest(t, caps{unicode: true, color: true}, 100, 28, viewRows())
	m.statusLine = "STATUSZONE"
	lines := strings.Split(m.View().Content, "\n")
	if len(lines) != 28 {
		t.Fatalf("frame has %d lines, want 28", len(lines))
	}
	status, help := lines[len(lines)-2], lines[len(lines)-1]
	if !strings.Contains(status, "STATUSZONE") {
		t.Fatalf("status bar missing from its own line: %q", status)
	}
	if !strings.Contains(help, "quit") {
		t.Fatalf("help bar missing from its own line: %q", help)
	}
	if strings.Contains(help, "STATUSZONE") || strings.Contains(status, "quit") {
		t.Fatalf("status and help share lines:\nstatus: %q\nhelp:   %q", status, help)
	}
}

func TestStatusCellCarriesGlyphAndWord(t *testing.T) {
	m := viewTest(t, caps{unicode: true, color: true}, 120, 28, viewRows())
	m.results = viewResults()
	content := m.View().Content
	for _, want := range []string{"✓ up to date", "⚠ update available", "✗ failed", "… skipped"} {
		if !strings.Contains(content, want) {
			t.Errorf("frame is missing %q", want)
		}
	}
}

// detailRow builds one row whose detail pane carries the whole A3 story:
// the changelog, the TOC verdict facts, the owned directories and the
// acquisition source.
func detailRow() service.AddonRow {
	return service.AddonRow{
		ID:         "r1",
		Name:       "rarescanner",
		Installed:  "1.60.1.b1",
		Upstream:   "1.60.1.b2",
		Strategy:   "release",
		Tracking:   "stable",
		Status:     "Mismatch",
		BuiltFor:   []int{110002},
		Dirs:       []string{"RareScanner", "RareScanner_Data"},
		Source:     "https://github.com/o/r",
		SourceKind: "github",
		Changelog:  "- fixed the map overlay\n- updated the locales",
	}
}

func TestDetailPaneCompletesTheStory(t *testing.T) {
	row := detailRow()
	m := viewTest(t, caps{unicode: true, color: true}, 120, 28, []service.AddonRow{row})
	m.client.Version = "1.60.1"
	detail := strings.Join(m.detailContent(&row, 40), "\n")
	for _, want := range []string{
		"built for 110002",              // plain-language verdict …
		"outdated",                      // … in two wrapped parts
		"RareScanner, RareScanner_Data", // owned dirs
		"github release",                // the friendly acquisition source (XD-56)
		"Changelog",                     // the notes section …
		"- fixed the map overlay",       // … with the installed version's notes
		"- updated the locales",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail pane is missing %q:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "Mismatch") {
		t.Errorf("detail pane shows the bare verdict jargon:\n%s", detail)
	}
	// The screen shows the same story (the table keeps its short word).
	m.detail = true
	content := m.View().Content
	for _, want := range []string{"built for 110002", "RareScanner_Data", "github release", "- fixed the map overlay"} {
		if !strings.Contains(content, want) {
			t.Errorf("frame is missing %q", want)
		}
	}
}

func TestFilterLineShowsFilterAndCount(t *testing.T) {
	m := viewTest(t, caps{unicode: true, color: true}, 100, 28, viewRows())
	m.filter = "owner"
	m.refreshRows()
	content := m.View().Content
	// The filter line always carries the active filter and the match
	// count: 4 of the 5 fixture rows have an owner/… source.
	for _, want := range []string{"filter: ", "owner — 4 of 5"} {
		if !strings.Contains(content, want) {
			t.Fatalf("filter line is missing %q:\n%s", want, content)
		}
	}
}

func TestPlainStatusReplacesVerdictJargon(t *testing.T) {
	m := viewTest(t, caps{unicode: true, color: true}, 100, 28, nil)
	m.client.Version = "1.60.1"
	tests := []struct {
		row  service.AddonRow
		word string
		want string
	}{
		{service.AddonRow{Status: "Match"}, "Match", "matches Forever 1.60.1"},
		{service.AddonRow{Status: "MultiMatch", BuiltFor: []int{15000, 16001}}, "MultiMatch", "matches Forever 1.60.1 (built for several versions)"},
		{service.AddonRow{Status: "Mismatch", BuiltFor: []int{110002}}, "Mismatch", "built for 110002 — may be outdated"},
		{service.AddonRow{Status: "Mismatch"}, "Mismatch", "built for an unknown interface — may be outdated"},
		{service.AddonRow{Status: "NoToc"}, "NoToc", "ships no .toc file"},
		{service.AddonRow{Status: "Missing"}, "Missing", "installed folder missing"},
		{service.AddonRow{Status: "Unknown"}, "Unknown", "compatibility not checked"},
		{service.AddonRow{Status: "-"}, "-", "-"},
		// A sync outcome's word is already plain language and passes through.
		{service.AddonRow{Status: "-"}, "updated 1.0 → 1.1", "updated 1.0 → 1.1"},
	}
	for _, tt := range tests {
		if got := m.plainStatus(tt.row, tt.word); got != tt.want {
			t.Errorf("plainStatus(%q) = %q, want %q", tt.word, got, tt.want)
		}
	}
	// The dash follows the glyph set: ASCII mode gets "-".
	m = viewTest(t, caps{unicode: false, color: false}, 100, 28, nil)
	if got, want := m.plainStatus(service.AddonRow{BuiltFor: []int{110002}}, "Mismatch"), "built for 110002 - may be outdated"; got != want {
		t.Errorf("ASCII plainStatus = %q, want %q", got, want)
	}
	if got, want := m.plainStatus(service.AddonRow{}, "Match"), "matches Forever"; got != want {
		t.Errorf("plainStatus without a game version = %q, want %q", got, want)
	}
}

func TestChangelogTextSanitizesAndClamps(t *testing.T) {
	// Control bytes go to spaces, line endings normalize, text survives.
	if got, want := changelogText("\x01note one\r\nnote two\x1b[0m", "…"), "note one\nnote two [0m"; got != want {
		t.Fatalf("changelogText = %q, want %q", got, want)
	}
	// The clamp marks the cut and bounds the pane's content (R7).
	long := changelogText(strings.Repeat("x", maxChangelogRunes+100), "…")
	if n := len([]rune(long)); n != maxChangelogRunes+1 {
		t.Fatalf("clamped changelog is %d runes, want %d", n, maxChangelogRunes+1)
	}
	if !strings.HasSuffix(long, "…") {
		t.Fatal("the clamp must mark the cut")
	}
}

func TestHugeUnprintableChangelogNeverCorruptsLayout(t *testing.T) {
	row := detailRow()
	row.Changelog = strings.Repeat("## 2.0.0 \x1b[31mbold\x07\r\n- item with text\r\n", 200)
	rows := []service.AddonRow{row}
	for _, w := range []int{60, 80, 100, 120, 200} {
		for _, overlay := range []bool{false, true} {
			m := viewTest(t, caps{unicode: true, color: true}, w, 28, rows)
			m.detail = overlay
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != 28 {
				t.Fatalf("width %d: frame has %d lines, want 28", w, len(lines))
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got > w {
					t.Fatalf("width %d (overlay=%v): line %d is %d cells wide:\n%q", w, overlay, i, got, line)
				}
				// The theme's own styling may color the line; past it no
				// control byte from upstream text may survive (R7).
				for _, r := range ansi.Strip(line) {
					if r < 0x20 || r == 0x7f {
						t.Fatalf("width %d (overlay=%v): line %d carries control byte %q:\n%q", w, overlay, i, r, line)
					}
				}
			}
		}
	}
}

func TestZeroMatchesShowsEmptyState(t *testing.T) {
	m := viewTest(t, caps{unicode: true, color: true}, 100, 28, viewRows())
	m.filter = "zzz"
	m.refreshRows()
	content := m.View().Content
	// The empty state names the filter, suggests clearing it and keeps
	// the count visible — not a blank grid.
	for _, want := range []string{`no addons match "zzz"`, "press esc to clear the filter", "0 of 5"} {
		if !strings.Contains(content, want) {
			t.Fatalf("zero-match frame is missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "rarescanner") {
		t.Fatalf("zero-match frame still shows the grid:\n%s", content)
	}
}

// TestSourceLabelNamesAcquisition pins the Source column's vocabulary
// (XD-56): the friendly acquisition source — "github release" vs "github
// built" by strategy, "curseforge" for cfwidget, "wago", a generic git
// remote as "git" — in the column and in the detail pane's Source field.
func TestSourceLabelNamesAcquisition(t *testing.T) {
	tests := []struct {
		row  service.AddonRow
		want string
	}{
		{service.AddonRow{SourceKind: "github", Strategy: "release"}, "github release"},
		{service.AddonRow{SourceKind: "github", Strategy: "source-pkgmeta"}, "github built"},
		{service.AddonRow{SourceKind: "github", Strategy: "-"}, "github"},
		{service.AddonRow{SourceKind: "cfwidget", Strategy: "direct"}, "curseforge"},
		{service.AddonRow{SourceKind: "wago", Strategy: "direct"}, "wago"},
		{service.AddonRow{SourceKind: "git", Strategy: "direct"}, "git"},
		{service.AddonRow{SourceKind: "manual", Strategy: "manual"}, "manual"},
		{service.AddonRow{SourceKind: "tukui", Strategy: "direct"}, "tukui"},
		{service.AddonRow{}, "-"},
	}
	for _, tt := range tests {
		if got := sourceLabel(tt.row); got != tt.want {
			t.Errorf("sourceLabel(%q/%q) = %q, want %q", tt.row.SourceKind, tt.row.Strategy, got, tt.want)
		}
	}

	// The frame's Source column shows the same names — and never the
	// internal provider name.
	rows := []service.AddonRow{
		{ID: "r1", Name: "One", Strategy: "release", Source: "owner/one", SourceKind: "github", Status: "-"},
		{ID: "r2", Name: "Two", Strategy: "direct", Source: "8315", SourceKind: "cfwidget", Status: "-"},
		{ID: "r3", Name: "Three", Strategy: "direct", Source: "VBNBxKx5", SourceKind: "wago", Status: "-"},
	}
	m := viewTest(t, caps{unicode: true, color: true}, 120, 28, rows)
	content := m.View().Content
	for _, want := range []string{"github release", "curseforge", "wago"} {
		if !strings.Contains(content, want) {
			t.Errorf("frame is missing the Source column value %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "cfwidget") {
		t.Fatalf("frame shows the internal provider name cfwidget:\n%s", content)
	}

	// The detail pane's Source field matches the column's naming.
	detail := strings.Join(m.detailContent(&rows[1], 40), "\n")
	if !strings.Contains(detail, "Source") || !strings.Contains(detail, "curseforge") {
		t.Fatalf("detail pane Source field is missing the friendly name:\n%s", detail)
	}
}

// TestHeaderColumnsAlignWithCells pins the header/value alignment
// (XD-56): header titles, sort marks and column values start at the same
// cell of every column — the header cells draw exactly the cellGap
// padding the body rows draw — in every sort mode.
func TestHeaderColumnsAlignWithCells(t *testing.T) {
	m := viewTest(t, caps{unicode: true, color: true}, 120, 28, viewRows())
	m.results = viewResults()
	for _, mode := range []sortMode{sortByName, sortByStatus, sortByUpstream} {
		m.sort = mode
		m.refreshRows()
		lines := strings.Split(m.View().Content, "\n")
		header := []rune(ansi.Strip(lines[1]))
		row := []rune(ansi.Strip(lines[2]))

		plain := make([][]string, 0, len(m.rows))
		for _, addon := range m.rows {
			cells, _ := m.plainCells(addon)
			plain = append(plain, cells)
		}
		specs := m.sortSpecs()
		widths := columnWidths(tableAvail(120, len(specs)), specs, naturalWidths(specs, plain))

		off := 0
		for i := range widths {
			content := off + cellGap/2
			if content >= len(header) || content >= len(row) {
				t.Fatalf("sort %s: column %d runs past the frame", mode.label(), i)
			}
			if header[content] == ' ' || row[content] == ' ' {
				t.Errorf("sort %s: column %q: header %q and value %q do not start together at offset %d",
					mode.label(), specs[i].title, header[content], row[content], content)
			}
			if header[content-1] != ' ' || row[content-1] != ' ' {
				t.Errorf("sort %s: column %q: the header and body cells do not draw the same padding",
					mode.label(), specs[i].title)
			}
			off += widths[i] + cellGap
		}
	}
}
