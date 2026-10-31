package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/itp5b5/blessforge/internal/service"
)

// Screen-level acceptance tests for the visual language: the responsive
// layout, the persistent side detail pane, the color/glyph rules and the
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

func TestSideDetailPaneIsPersistentAtWideWidths(t *testing.T) {
	rows := viewRows()

	// At 120 columns the detail pane is beside the table without any key.
	m := viewTest(t, caps{unicode: true, color: true}, 120, 28, rows)
	if !strings.Contains(m.View().Content, "Tracking") {
		t.Fatal("at 120 columns the detail pane should be persistent beside the table")
	}

	// Below 120 columns it stays the toggle overlay: closed at first.
	m = viewTest(t, caps{unicode: true, color: true}, 100, 28, rows)
	if strings.Contains(m.View().Content, "Tracking") {
		t.Fatal("below 120 columns the detail pane should start closed")
	}
	m.detail = true
	if !strings.Contains(m.View().Content, "Tracking") {
		t.Fatal("the detail toggle should open the overlay below 120 columns")
	}
}

func TestTruncatedCellsRevealFullValueInDetailPane(t *testing.T) {
	const longName = "VeryLongAddonNameThatOverflowsCells"
	const longVersion = "12.1.17-beta.9+build.20261030"
	rows := []service.AddonRow{
		{ID: "r1", Name: longName, Installed: longVersion, Upstream: longVersion, Strategy: "release", Tracking: "stable", Status: "-", Source: "owner/repo", SourceKind: "github"},
	}

	// Wide: the side pane is persistent and holds the full values.
	m := viewTest(t, caps{unicode: true, color: true}, 120, 28, rows)
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

	// Narrow: the toggle overlay holds the same full values.
	m = viewTest(t, caps{unicode: true, color: true}, 80, 28, rows)
	m.detail = true
	content = m.View().Content
	if !strings.Contains(content, longName) {
		t.Fatal("detail overlay should show the full addon name")
	}
	if n := strings.Count(content, longName); n != 1 {
		t.Fatalf("full name appears %d times, want 1 (the table cell should be truncated)", n)
	}
	if !strings.Contains(content, longVersion) {
		t.Fatal("detail overlay should show the full installed version")
	}
}

func TestASCIIModeDegradesAllGlyphs(t *testing.T) {
	m := viewTest(t, caps{unicode: false, color: false}, 100, 28, viewRows())
	m.results = viewResults()
	m.detail = true
	for i, line := range strings.Split(m.View().Content, "\n") {
		for _, r := range line {
			if r > 127 {
				t.Fatalf("ASCII mode: line %d contains non-ASCII rune %q:\n%q", i, r, line)
			}
		}
	}
	// The ASCII status glyphs carry their words.
	for _, want := range []string{"+ up to date", "! update 342", "x failed", "* manual", "+ updated"} {
		if !strings.Contains(m.View().Content, want) {
			t.Errorf("ASCII mode: frame is missing %q", want)
		}
	}
}

func TestColorNeverCarriesMeaningAlone(t *testing.T) {
	for _, c := range []caps{{unicode: true, color: true}, {unicode: true, color: false}} {
		m := viewTest(t, c, 100, 28, viewRows())
		m.results = viewResults()
		content := m.View().Content
		for _, want := range []string{"up to date", "update 342", "failed", "manual"} {
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
	for _, want := range []string{"✓ up to date", "⚠ update 342", "✗ failed", "… manual"} {
		if !strings.Contains(content, want) {
			t.Errorf("frame is missing %q", want)
		}
	}
}

// detailRow builds one row whose detail pane carries the whole A3 story:
// the changelog, the TOC verdict facts, the owned directories and the
// source URL.
func detailRow() service.AddonRow {
	return service.AddonRow{
		ID:         "r1",
		Name:       "rarescanner",
		Installed:  "1.60.1.b1",
		Upstream:   "1.60.1.b2",
		Strategy:   "direct",
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
		"https://github.com/o/r",        // source URL, in full
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
	content := m.View().Content
	for _, want := range []string{"built for 110002", "RareScanner_Data", "https://github.com/o/r", "- fixed the map overlay"} {
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
