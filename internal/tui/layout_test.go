package tui

import "testing"

// The layout is a pure function of state + size (R1): these tests pin the
// geometry at every terminal size the acceptance criteria name.

func TestPlanScreenHoldsAtAcceptanceWidths(t *testing.T) {
	for _, w := range []int{60, 80, 100, 120, 200} {
		for _, detail := range []bool{false, true} {
			plan := planScreen(w, 28, detail, false, false, 0)
			if got, want := headerZoneH+plan.tableH+plan.detailH+plan.filterH+statusZoneH+helpZoneH, 28; got != want {
				t.Errorf("width %d detail=%v: zones sum to %d lines, want %d", w, detail, got, want)
			}
			if plan.tableW != w {
				t.Errorf("width %d detail=%v: tableW = %d, want the full width", w, detail, plan.tableW)
			}
			if plan.tableH < 1 {
				t.Errorf("width %d detail=%v: tableH = %d, want at least 1", w, detail, plan.tableH)
			}
			if detail {
				// The dedicated detail view (XD-56): the list keeps only
				// its top rows and the details take the rest of the height.
				if plan.tableH != detailListRows+1 {
					t.Errorf("width %d: tableH = %d, want %d (the header plus %d rows)",
						w, plan.tableH, detailListRows+1, detailListRows)
				}
				if plan.detailH < detailMinH {
					t.Errorf("width %d: detailH = %d, want at least %d", w, plan.detailH, detailMinH)
				}
				if plan.detailW != w-4 {
					t.Errorf("width %d: detailW = %d, want %d", w, plan.detailW, w-4)
				}
			} else if plan.detailH != 0 {
				t.Errorf("width %d: detailH = %d, want 0 while the pane is closed", w, plan.detailH)
			}
		}
	}
}

func TestPlanScreenReservesStatusAndHelpZones(t *testing.T) {
	// With the filter line and the detail pane both wanting room, the
	// status and help bars still keep their own lines.
	plan := planScreen(80, 24, true, true, false, 0)
	if plan.filterH != 1 {
		t.Fatalf("filterH = %d, want 1", plan.filterH)
	}
	got := headerZoneH + plan.tableH + plan.detailH + plan.filterH + statusZoneH + helpZoneH
	if got != 24 {
		t.Fatalf("zones sum to %d lines, want 24", got)
	}
	if plan.tableH != detailListRows+1 {
		t.Fatalf("tableH = %d, want the list's top %d rows plus its header", plan.tableH, detailListRows)
	}
	if plan.detailH != 16 {
		t.Fatalf("detailH = %d, want the space the shrunken list leaves (16)", plan.detailH)
	}
}

func TestPlanScreenListYieldsBeforeTheDetails(t *testing.T) {
	// A short terminal takes rows away from the list before it squeezes
	// the details out of existence — the details are the point of the
	// dedicated view.
	plan := planScreen(80, 9, true, false, false, 0)
	if plan.tableH != detailListRows || plan.detailH != detailMinH {
		t.Fatalf("h=9: tableH = %d detailH = %d, want the list at %d rows and the pane at its minimum %d",
			plan.tableH, plan.detailH, detailListRows, detailMinH)
	}
	if got := headerZoneH + plan.tableH + plan.detailH + statusZoneH + helpZoneH; got != 9 {
		t.Fatalf("zones sum to %d lines, want 9", got)
	}

	// Tiny: the list keeps one line and the pane takes what is left.
	plan = planScreen(80, 6, true, false, false, 0)
	if plan.tableH != 1 || plan.detailH != 2 {
		t.Fatalf("h=6: tableH = %d detailH = %d, want 1 and 2", plan.tableH, plan.detailH)
	}
	if got := headerZoneH + plan.tableH + plan.detailH + statusZoneH + helpZoneH; got != 6 {
		t.Fatalf("zones sum to %d lines, want 6", got)
	}
}

func TestColumnWidthsMinimumsAtNarrow(t *testing.T) {
	// 60 columns: 48 cells of content plus 12 cells of padding (six
	// columns, cellGap each) fit the minimum widths exactly.
	widths := columnWidths(tableAvail(60, len(columnSpecs)), columnSpecs, naturalWidths(columnSpecs, nil))
	for i, s := range columnSpecs {
		if widths[i] != s.min {
			t.Errorf("column %s = %d, want the minimum %d", s.title, widths[i], s.min)
		}
	}
}

func TestColumnWidthsHoldMinsAndMaxes(t *testing.T) {
	huge := make([]int, len(columnSpecs))
	for i := range huge {
		huge[i] = 200
	}
	maxSum := 0
	for _, s := range columnSpecs {
		maxSum += s.max
	}
	for _, avail := range []int{30, 48, 53, 66, 88, 132, 188} {
		widths := columnWidths(avail, columnSpecs, huge)
		sum := 0
		for i, w := range widths {
			sum += w
			if w < minColWidth {
				t.Errorf("avail %d: column %s = %d below the floor %d", avail, columnSpecs[i].title, w, minColWidth)
			}
			if w > columnSpecs[i].max {
				t.Errorf("avail %d: column %s = %d above its maximum %d", avail, columnSpecs[i].title, w, columnSpecs[i].max)
			}
			if avail >= 48 && w < columnSpecs[i].min {
				t.Errorf("avail %d: column %s = %d below its minimum %d", avail, columnSpecs[i].title, w, columnSpecs[i].min)
			}
		}
		// The widths always fill the room offered up to every column's
		// max — and never beyond it, however wide the terminal is. (Below
		// the minimum sum the proportional squeeze rounds down slightly.)
		if want := minInt(avail, maxSum); avail >= 48 && sum != want {
			t.Errorf("avail %d: widths sum to %d, want %d", avail, sum, want)
		}
	}
}

func TestColumnWidthsProportionalSlack(t *testing.T) {
	// Slack is shared out by content demand: Installed and Strategy have
	// no demand above their minimums here, so they stay put while the
	// long name and status grow.
	naturals := []int{20, 14, 9, 12, 8, 25}
	widths := columnWidths(60, columnSpecs, naturals)
	if sum := widths[0] + widths[1] + widths[2] + widths[3] + widths[4] + widths[5]; sum != 60 {
		t.Fatalf("widths %v sum to %d, want 60", widths, sum)
	}
	if widths[2] != columnSpecs[2].min {
		t.Errorf("Installed = %d, want to stay at its minimum %d", widths[2], columnSpecs[2].min)
	}
	if widths[4] != columnSpecs[4].min {
		t.Errorf("Strategy = %d, want to stay at its minimum %d", widths[4], columnSpecs[4].min)
	}
	if widths[5] <= columnSpecs[5].min {
		t.Errorf("Status = %d, want it to grow past its minimum %d", widths[5], columnSpecs[5].min)
	}
}

func TestColumnWidthsShowContentWithoutStretching(t *testing.T) {
	// With room to spare every column shows its content in full and
	// nothing stretches to swallow the terminal's spare room — the name
	// column especially no longer consumes the screen (XD-56).
	naturals := []int{20, 14, 9, 12, 11, 16}
	widths := columnWidths(132, columnSpecs, naturals)
	for i := range naturals {
		if widths[i] != naturals[i] {
			t.Errorf("column %s = %d, want its natural width %d", columnSpecs[i].title, widths[i], naturals[i])
		}
	}

	// The caps hold even for oversized content: the Addon column stops at
	// its max instead of absorbing the whole terminal.
	widths = columnWidths(188, columnSpecs, []int{200, 200, 200, 200, 200, 200})
	for i := range widths {
		if widths[i] != columnSpecs[i].max {
			t.Errorf("column %s = %d, want its maximum %d", columnSpecs[i].title, widths[i], columnSpecs[i].max)
		}
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
