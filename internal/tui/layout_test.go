package tui

import "testing"

// The layout is a pure function of state + size (R1): these tests pin the
// geometry at every terminal size the acceptance criteria name.

func TestPlanScreenHoldsAtAcceptanceWidths(t *testing.T) {
	for _, w := range []int{60, 80, 100, 120, 200} {
		plan := planScreen(w, 28, false, false, false, 0)
		if got, want := headerZoneH+plan.tableH+plan.overlayH+plan.filterH+statusZoneH+helpZoneH, 28; got != want {
			t.Errorf("width %d: zones sum to %d lines, want %d", w, got, want)
		}
		if plan.side != (w >= sideDetailMinWidth) {
			t.Errorf("width %d: side pane = %v, want %v", w, plan.side, w >= sideDetailMinWidth)
		}
		if plan.side {
			if got, want := plan.tableW+plan.detailW+2, w; got != want {
				t.Errorf("width %d: tableW %d + detailW %d + rule = %d, want %d", w, plan.tableW, plan.detailW, got, want)
			}
		} else if plan.tableW != w {
			t.Errorf("width %d: tableW = %d, want the full width", w, plan.tableW)
		}
		if plan.tableH < 1 {
			t.Errorf("width %d: tableH = %d, want at least 1", w, plan.tableH)
		}
	}
}

func TestPlanScreenReservesStatusAndHelpZones(t *testing.T) {
	// With the filter line and the detail overlay both wanting room, the
	// status and help bars still keep their own lines.
	plan := planScreen(80, 24, true, true, false, 40)
	if plan.filterH != 1 {
		t.Fatalf("filterH = %d, want 1", plan.filterH)
	}
	if plan.overlayH >= 40 {
		t.Fatalf("overlayH = %d, want it squeezed to the free space", plan.overlayH)
	}
	got := headerZoneH + plan.tableH + plan.overlayH + plan.filterH + statusZoneH + helpZoneH
	if got != 24 {
		t.Fatalf("zones sum to %d lines, want 24", got)
	}
	if plan.tableH < 1 {
		t.Fatalf("tableH = %d, want at least 1", plan.tableH)
	}
}

func TestPlanScreenOverlayGivesWayFirst(t *testing.T) {
	// A short terminal keeps the table (at least one row) and both bars;
	// the overlay shrinks to whatever space is left.
	plan := planScreen(80, 6, true, false, false, 20)
	if plan.overlayH != 2 {
		t.Fatalf("overlayH = %d, want 2 (the space above the squeezed table)", plan.overlayH)
	}
	if plan.tableH != 1 {
		t.Fatalf("tableH = %d, want 1", plan.tableH)
	}
	if got := headerZoneH + plan.tableH + plan.overlayH + statusZoneH + helpZoneH; got != 6 {
		t.Fatalf("zones sum to %d lines, want 6", got)
	}
}

func TestColumnWidthsMinimumsAtNarrow(t *testing.T) {
	// 60 columns: 50 cells of content plus 10 cells of padding fit the
	// minimum widths exactly.
	widths := columnWidths(50, columnSpecs, naturalWidths(columnSpecs, nil))
	for i, s := range columnSpecs {
		if widths[i] != s.min {
			t.Errorf("column %s = %d, want the minimum %d", s.title, widths[i], s.min)
		}
	}
}

func TestColumnWidthsSumAndBounds(t *testing.T) {
	huge := []int{200, 200, 200, 200, 200}
	for _, avail := range []int{30, 50, 55, 68, 90, 144, 200} {
		widths := columnWidths(avail, columnSpecs, huge)
		sum := 0
		for i, w := range widths {
			sum += w
			if avail >= 50 && w < columnSpecs[i].min {
				t.Errorf("avail %d: column %s = %d below its minimum %d", avail, columnSpecs[i].title, w, columnSpecs[i].min)
			}
			if w < minColWidth {
				t.Errorf("avail %d: column %s = %d below the floor %d", avail, columnSpecs[i].title, w, minColWidth)
			}
		}
		if avail >= 50 && sum != avail {
			t.Errorf("avail %d: widths sum to %d", avail, sum)
		}
	}
}

func TestColumnWidthsProportionalSlack(t *testing.T) {
	// Slack is shared out by content demand: Installed has no demand above
	// its minimum here, so it stays put while the long status grows.
	naturals := []int{20, 9, 12, 8, 25}
	widths := columnWidths(60, columnSpecs, naturals)
	if sum := widths[0] + widths[1] + widths[2] + widths[3] + widths[4]; sum != 60 {
		t.Fatalf("widths %v sum to %d, want 60", widths, sum)
	}
	if widths[1] != columnSpecs[1].min {
		t.Errorf("Installed = %d, want to stay at its minimum %d", widths[1], columnSpecs[1].min)
	}
	if widths[4] <= columnSpecs[4].min {
		t.Errorf("Status = %d, want it to grow past its minimum %d", widths[4], columnSpecs[4].min)
	}
}

func TestColumnWidthsContentFirstThenAddon(t *testing.T) {
	// With room to spare every column shows its content in full and the
	// first column absorbs the rest.
	naturals := []int{20, 10, 12, 12, 15}
	widths := columnWidths(144, columnSpecs, naturals)
	for i := 1; i < len(naturals); i++ {
		if widths[i] != naturals[i] {
			t.Errorf("column %s = %d, want its natural width %d", columnSpecs[i].title, widths[i], naturals[i])
		}
	}
	if widths[0] <= naturals[0] {
		t.Errorf("Addon = %d, want it to absorb the slack above %d", widths[0], naturals[0])
	}
}

func TestWrapTextFullValueVisible(t *testing.T) {
	lines := wrapText("some very long addon name with many words", 12)
	joined := ""
	for _, l := range lines {
		if w := len([]rune(l)); w > 12 {
			t.Fatalf("line %q is %d runes, want at most 12", l, w)
		}
		joined += l + " "
	}
	for _, word := range []string{"some", "very", "long", "addon", "name", "with", "many", "words"} {
		if !containsFold(joined, word) {
			t.Errorf("wrapped output lost %q: %q", word, joined)
		}
	}
}

func TestWrapTextHardCutsOversizedWords(t *testing.T) {
	lines := wrapText("Supercalifragilisticexpialidocious", 10)
	if len(lines) < 3 {
		t.Fatalf("got %d lines, want the word hard-cut across several", len(lines))
	}
	joined := ""
	for _, l := range lines {
		joined += l
	}
	if joined != "Supercalifragilisticexpialidocious" {
		t.Fatalf("hard-cut lost characters: %q", joined)
	}
}
