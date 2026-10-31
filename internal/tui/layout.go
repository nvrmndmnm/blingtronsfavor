package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Layout is a pure function of state + terminal size (R1): nothing in this
// file touches the model, the terminal or the environment, so the geometry
// is testable at every size the acceptance criteria name (60, 80, 100, 120,
// 200 columns).

const (
	headerZoneH = 1 // styled header bar
	statusZoneH = 1 // status bar
	helpZoneH   = 1 // help bar — its own zone, never sharing lines with status

	// sideDetailMinWidth is the terminal width from which the detail pane
	// becomes persistent beside the table instead of a toggle overlay.
	sideDetailMinWidth = 120

	minColWidth = 3 // hard floor per column when the terminal is tiny
)

// screenPlan is the zone geometry of one screen.
type screenPlan struct {
	width, height  int
	tableW, tableH int
	side           bool // persistent side detail pane
	detailW        int  // detail content width (side pane)
	overlayH       int  // toggle-overlay detail height, 0 when absent
	filterH        int  // filter line height, 0 or 1
	waitH          int  // rate-limit wait line height, 0 or 1
	addH           int  // add input + hint + flavor prompt heights, 0–3
}

// planScreen computes the screen geometry for one terminal size and view
// state. wantDetail is the detail toggle state (only meaningful below the
// side-pane width), addH is the height the add flow's input and prompt
// lines want, and overlayNeed is the height the overlay would like to have
// (content plus its border).
func planScreen(w, h int, wantDetail, filterOn, waitOn bool, addH, overlayNeed int) screenPlan {
	p := screenPlan{width: w, height: h}
	fixed := headerZoneH + statusZoneH + helpZoneH
	if filterOn {
		p.filterH = 1
		fixed++
	}
	if waitOn {
		p.waitH = 1
		fixed++
	}
	p.addH = clampInt(addH, 0, 3)
	fixed += p.addH
	if w >= sideDetailMinWidth {
		p.side = true
		p.detailW = clampInt(w/3, 28, 44)
		p.tableW = w - p.detailW - 2 // 2 = vertical rule + gap
	} else {
		p.tableW = w
		if wantDetail {
			p.overlayH = overlayNeed
		}
	}
	// The status and help bars always get their own line; the table keeps
	// at least one row; a squeezed overlay shrinks to fit the free space.
	if fixed+1+p.overlayH > h {
		p.overlayH = maxInt(0, h-fixed-1)
	}
	p.tableH = maxInt(1, h-fixed-p.overlayH)
	return p
}

// colSpec is one column's sizing contract.
type colSpec struct {
	title string
	min   int // floor: the header word must fit
	max   int // cap for content-proportional slack shares
}

// columnSpecs fixes the five columns and their sizing bounds. The minimums
// sum to 50 so the layout still holds at 60 columns (50 cells plus the
// per-cell padding).
var columnSpecs = []colSpec{
	{title: "Addon", min: 12, max: 34},
	{title: "Installed", min: 9, max: 14},
	{title: "Upstream", min: 8, max: 18},
	{title: "Strategy", min: 8, max: 14},
	{title: "Status", min: 13, max: 28},
}

// cellGap is the horizontal padding the table draws around each cell.
const cellGap = 2

// tableAvail converts a table zone width into the sum available to the
// column widths themselves.
func tableAvail(tableW, cols int) int {
	return tableW - cellGap*cols
}

// columnWidths computes each column's width for avail cells: every column
// starts at its minimum and the slack is shared out proportionally to each
// column's content demand (its natural width above the minimum, capped at
// the column max). When the slack outgrows every demand the remainder
// widens the first column — names benefit most from spare room. Below the
// minimum sum the columns shrink proportionally instead of dropping any.
func columnWidths(avail int, specs []colSpec, naturals []int) []int {
	widths := make([]int, len(specs))
	minSum := 0
	for i, s := range specs {
		widths[i] = s.min
		minSum += s.min
	}
	if avail <= minSum {
		// Degraded terminal: share what there is proportionally, keeping
		// a small floor so cells never collapse entirely.
		if avail <= 0 {
			return widths
		}
		for i, s := range specs {
			widths[i] = maxInt(minColWidth, s.min*avail/minSum)
		}
		return widths
	}
	slack := avail - minSum
	demand := make([]int, len(specs))
	total := 0
	for i, s := range specs {
		n := clampInt(naturals[i], s.min, s.max)
		demand[i] = n - s.min
		total += demand[i]
	}
	if total == 0 {
		widths[0] += slack
		return widths
	}
	if slack >= total {
		for i := range widths {
			widths[i] += demand[i]
		}
		widths[0] += slack - total
		return widths
	}
	// Proportional shares, with the rounding remainder handed out in
	// column order so the widths stay a deterministic function of input.
	used := 0
	for i := range widths {
		share := slack * demand[i] / total
		widths[i] += share
		used += share
	}
	for i := 0; used < slack; i = (i + 1) % len(widths) {
		if demand[i] > 0 {
			widths[i]++
			used++
		}
	}
	return widths
}

// naturalWidths measures the widest content per column over the given rows
// (plain, untruncated cells), including the column titles.
func naturalWidths(specs []colSpec, rows [][]string) []int {
	naturals := make([]int, len(specs))
	for i, s := range specs {
		naturals[i] = lipgloss.Width(s.title)
	}
	for _, row := range rows {
		for i := range naturals {
			if i < len(row) {
				naturals[i] = maxInt(naturals[i], lipgloss.Width(row[i]))
			}
		}
	}
	return naturals
}

// clip truncates s to at most w visible cells, marking the cut with tail.
func clip(s string, w int, tail string) string {
	return ansi.Truncate(s, w, tail)
}

// padLine pads s with spaces to exactly w visible cells (already clipped).
func padLine(s string, w int) string {
	if gap := w - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// fitBlock clips a rendered block to exactly w x h: every line is clipped
// and padded to w and the overflow is dropped (or padded out with blank
// lines), so the screen composition always adds up to the plan.
func fitBlock(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i := range lines {
		lines[i] = padLine(clip(lines[i], w, ""), w)
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return strings.Join(lines, "\n")
}

// wrapText wraps s to w cells per line, hard-cutting words that are longer
// than a line. Used by the detail pane so a truncated cell's full value is
// actually visible.
func wrapText(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := ""
		flush := func() {
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
		}
		for _, word := range words {
			for lipgloss.Width(word) > w { // hard-cut oversized words
				var head string
				head, word = splitAt(word, w)
				flush()
				lines = append(lines, head)
			}
			switch {
			case line == "":
				line = word
			case lipgloss.Width(line)+1+lipgloss.Width(word) <= w:
				line += " " + word
			default:
				flush()
				line = word
			}
		}
		flush()
	}
	return lines
}

// splitAt cuts s into a head of at most w visible cells and the remaining
// tail. The first rune is always taken, so a rune wider than w cannot stall
// the caller.
func splitAt(s string, w int) (head, rest string) {
	width := 0
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if width+rw > w && width > 0 {
			return s[:i], s[i:]
		}
		width += rw
	}
	return s, ""
}

// truncateMiddle shortens s to w cells keeping both ends, marking the cut
// with tail — the root path in the header keeps its identifying tail.
func truncateMiddle(s string, w int, tail string) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	tw := lipgloss.Width(tail)
	if w <= tw {
		return clip(tail, w, "")
	}
	keep := w - tw
	head := clip(s, (keep+1)/2, "")
	tailPart := keep / 2
	if tailPart <= 0 {
		return head + tail
	}
	runes := []rune(s)
	// Walk from the end by visible width instead of by rune count.
	end := len(runes)
	width := 0
	for end > 0 && width < tailPart {
		end--
		width += lipgloss.Width(string(runes[end]))
	}
	return head + tail + string(runes[end:])
}

func clampInt(v, low, high int) int {
	if high < low {
		high = low
	}
	return minInt(maxInt(v, low), high)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
