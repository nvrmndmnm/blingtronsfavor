package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
)

// View satisfies tea.Model.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render composes the whole screen from the model state and the terminal
// size — header bar, table with its detail pane, optional filter line and
// rate-limit wait line, then the status bar and the help bar as separate
// zones. Every zone is sized by planScreen, so the composition always fits
// the terminal and the status and help lines can never overwrite each
// other.
func (m Model) render() string {
	th := m.theme
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	if len(m.status.Clients) == 0 {
		lines := []string{
			m.headerBar(w),
			"",
			th.dim.Render(fmt.Sprintf("no addons configured %s run \"bfctl init --path <root>\" first", th.g.Dash)),
		}
		for len(lines) < h-1 {
			lines = append(lines, "")
		}
		lines = append(lines, m.helpBar(w))
		return fitBlock(strings.Join(lines, "\n"), w, h)
	}

	a := m.selected()
	filterOn := m.filtering || m.filter != ""

	// Detail pane geometry feeds the plan: the overlay needs its content
	// height up front, the side pane is sized by planScreen itself.
	detailW := maxInt(8, w-4)
	overlayNeed := 0
	if m.detail {
		overlayNeed = len(m.detailContent(a, detailW)) + 2 // border rows
	}
	plan := planScreen(w, h, m.detail, filterOn, m.wait != "", overlayNeed)

	// Table geometry: responsive columns from the terminal width and the
	// visible content, then the rows styled and truncated to those widths.
	plain := make([][]string, 0, len(m.rows))
	classes := make([]statusClass, 0, len(m.rows))
	for _, addon := range m.rows {
		cells, class := m.plainCells(addon)
		plain = append(plain, cells)
		classes = append(classes, class)
	}
	widths := columnWidths(tableAvail(plan.tableW, len(columnSpecs)), columnSpecs, naturalWidths(columnSpecs, plain))
	cols := make([]table.Column, len(columnSpecs))
	for i, spec := range columnSpecs {
		cols[i] = table.Column{Title: spec.title, Width: widths[i]}
	}
	m.table.SetColumns(cols)
	m.table.SetRows(m.styledRows(plain, classes, widths))
	m.table.SetHeight(plan.tableH)
	m.table.SetWidth(plan.tableW)

	body := fitBlock(m.table.View(), plan.tableW, plan.tableH)
	if plan.side {
		body = joinBlocks(body, m.detailBlock(a, plan.detailW, plan.tableH))
	}

	var b strings.Builder
	b.WriteString(m.headerBar(w))
	b.WriteString("\n")
	b.WriteString(body)
	if plan.overlayH > 0 {
		b.WriteString("\n")
		b.WriteString(m.detailOverlay(a, plan))
	}
	if plan.filterH > 0 {
		b.WriteString("\n")
		b.WriteString(m.filterLine(w))
	}
	if plan.waitH > 0 {
		b.WriteString("\n")
		b.WriteString(m.waitLine(w))
	}
	b.WriteString("\n")
	b.WriteString(m.statusBar(w))
	b.WriteString("\n")
	b.WriteString(m.helpBar(w))
	return b.String()
}

// styledRows turns the plain cells into table rows at the planned widths:
// every cell is truncated with the theme's ellipsis (the full value stays
// visible in the detail pane), the selected row wears the accent, and the
// status cell keeps its semantic color in every case.
func (m Model) styledRows(plain [][]string, classes []statusClass, widths []int) []table.Row {
	th := m.theme
	cursor := m.table.Cursor()
	rows := make([]table.Row, len(plain))
	for i, cells := range plain {
		row := make(table.Row, len(cells))
		for j, raw := range cells {
			cell := clip(raw, widths[j], th.g.Ellipsis)
			switch {
			case j == len(cells)-1:
				style := th.statusStyle(classes[i])
				if i == cursor {
					style = style.Bold(true)
				}
				row[j] = style.Render(cell)
			case i == cursor:
				row[j] = th.accent.Render(cell)
			default:
				row[j] = cell
			}
		}
		rows[i] = row
	}
	return rows
}

// plainCells renders one addon's five cells before truncation and styling.
// The status cell always carries its glyph and its word, so meaning never
// depends on the glyph or the color alone — except a live row, whose
// per-row spinner is already its marker while the run decides the outcome.
func (m Model) plainCells(a service.AddonRow) ([]string, statusClass) {
	word := m.statusText(a)
	class := classifyStatus(word)
	cell := m.theme.statusGlyph(class) + " " + word
	if l, ok := m.live.rows[a.ID]; ok && l.result == nil {
		cell = word
		class = statusNeutral
	}
	return []string{
		a.Name,
		a.Installed,
		a.Upstream,
		service.StrategyLabel(state.Strategy(a.Strategy)),
		cell,
	}, class
}

// headerBar renders the styled header bar: flavor badge, game version,
// client root and the checked stamp ("checked 14:32", updating after each
// refresh or sync) on the left, the live addon counts on the right.
func (m Model) headerBar(width int) string {
	th := m.theme
	left := th.accent.Render("bfctl")
	if m.client.Flavor != "" {
		left = th.accent.Render(m.client.FlavorName)
		if m.client.Version != "" {
			left += " " + th.title.Render(m.client.Version)
		}
	}
	right := ""
	if !m.checkedAt.IsZero() {
		right = th.dim.Render("checked " + m.checkedAt.Format("15:04"))
	}
	if m.client.Flavor != "" {
		total, updates := m.counts()
		counts := fmt.Sprintf("%d %s", total, plural(total, "addon"))
		if updates > 0 {
			counts += " " + th.g.MidDot + " " +
				th.warn.Render(fmt.Sprintf("%d %s available", updates, plural(updates, "update")))
		}
		if right != "" {
			right += " " + th.g.MidDot + " "
		}
		right += counts
	}

	root := ""
	room := width - lipgloss.Width(left) - lipgloss.Width(right) - 4
	if m.client.Root != "" && room >= 4 {
		root = th.g.MidDot + " " + th.dim.Render(truncateMiddle(m.client.Root, room, th.g.Ellipsis)) + " "
	}
	gap := width - lipgloss.Width(left) - 1 - lipgloss.Width(root) - lipgloss.Width(right)
	return clip(left+" "+root+strings.Repeat(" ", maxInt(0, gap))+right, width, th.g.Ellipsis)
}

// detailContent renders the selected addon's detail: every table value at
// full length (truncated cells reveal it here), plus source, tracking, the
// last sync's error and plan reason and the owned directories.
func (m Model) detailContent(a *service.AddonRow, width int) []string {
	th := m.theme
	if a == nil {
		return []string{th.dim.Render("no addon selected")}
	}
	word := m.statusText(*a)
	fields := []detailField{
		{label: "Source", value: sourceText(a), style: th.dim},
		{label: "Tracking", value: dash(a.Tracking), style: th.dim},
		{label: "Strategy", value: service.StrategyLabel(state.Strategy(a.Strategy)), style: th.dim},
		{label: "Installed", value: dash(a.Installed), style: th.dim},
		{label: "Upstream", value: dash(a.Upstream), style: th.dim},
		{label: "Status", value: th.statusGlyph(classifyStatus(word)) + " " + word, style: th.statusStyle(classifyStatus(word))},
	}
	if len(a.Dirs) > 0 {
		fields = append(fields, detailField{label: "Dirs", value: strings.Join(a.Dirs, ", "), style: th.dim})
	}
	if r, ok := m.resultFor(a.ID); ok {
		if r.Err != nil {
			fields = append(fields, detailField{label: "Error", value: clean(r.Err.Error()), style: th.bad})
		}
		if r.Plan.Reason != "" {
			fields = append(fields, detailField{label: "Plan", value: r.Plan.Reason, style: th.dim})
		}
	}

	lines := make([]string, 0, len(fields)+2)
	for _, line := range wrapText(a.Name, width) {
		lines = append(lines, th.accent.Render(line))
	}
	lines = append(lines, "")
	return append(lines, renderFields(fields, width, th)...)
}

// detailField is one label/value pair in the detail pane.
type detailField struct {
	label string
	value string
	style lipgloss.Style
}

// renderFields renders the fields at width cells per line: values wrap so
// the full value is visible even in a narrow pane.
func renderFields(fields []detailField, width int, th theme) []string {
	labelW := 0
	for _, f := range fields {
		labelW = maxInt(labelW, lipgloss.Width(f.label))
	}
	valueW := maxInt(8, width-labelW-2)
	var lines []string
	for _, f := range fields {
		for i, chunk := range wrapText(f.value, valueW) {
			label := strings.Repeat(" ", labelW+2)
			if i == 0 {
				label = th.dim.Render(padLine(f.label, labelW)) + "  "
			}
			lines = append(lines, label+f.style.Render(chunk))
		}
	}
	return lines
}

// detailBlock renders the persistent side detail pane: a vertical rule and
// the selected addon's full values beside the table.
func (m Model) detailBlock(a *service.AddonRow, contentW, height int) string {
	lines := fitLines(m.detailContent(a, contentW), height, m.theme.g.Ellipsis)
	block := make([]string, 0, height)
	for i := 0; i < height; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		block = append(block, m.theme.rule+" "+padLine(clip(line, contentW, ""), contentW))
	}
	return strings.Join(block, "\n")
}

// detailOverlay renders the toggle-overlay detail pane for narrow
// terminals: the same full values in a bordered box below the table.
func (m Model) detailOverlay(a *service.AddonRow, plan screenPlan) string {
	th := m.theme
	contentW := maxInt(8, plan.width-4)
	inner := plan.overlayH - 2
	if inner < 1 {
		return fitBlock(th.dim.Render("no room for the detail pane"), plan.width, maxInt(1, plan.overlayH))
	}
	lines := fitLines(m.detailContent(a, contentW), inner, th.g.Ellipsis)
	top := th.box.TopLeft + strings.Repeat(th.box.Top, contentW+2) + th.box.TopRight
	bottom := th.box.BottomLeft + strings.Repeat(th.box.Bottom, contentW+2) + th.box.BottomRight
	block := []string{top}
	for _, line := range lines {
		block = append(block, th.box.Left+" "+padLine(clip(line, contentW, ""), contentW)+" "+th.box.Right)
	}
	block = append(block, bottom)
	return strings.Join(block, "\n")
}

// filterLine renders the active filter input line.
func (m Model) filterLine(width int) string {
	th := m.theme
	line := th.dim.Render("filter: ") + m.filter
	if m.filtering {
		line += th.accent.Render(th.g.Cursor)
	}
	return clip(line, width, th.g.Ellipsis)
}

// waitLine renders the rate-limit wait line: the live reason one unit is
// stalled ("rate limited, retry after 5m") while the wait lasts.
func (m Model) waitLine(width int) string {
	return m.theme.warn.Render(clip(m.wait, width, m.theme.g.Ellipsis))
}

// statusBar renders the status zone: the sync run frame beside the spinner
// while a sync runs — how far the run has come and how it is going — then
// the current message. It occupies exactly one line of its own.
func (m Model) statusBar(width int) string {
	th := m.theme
	line := m.statusLine
	if m.syncing {
		line = strings.TrimRight(m.spin.View(), " ") + " " + m.runFrame()
		if m.statusLine != "" {
			line += " " + th.g.MidDot + " " + th.dim.Render(m.statusLine)
		}
	}
	return clip(line, width, th.g.Ellipsis)
}

// helpSubset is a reduced key list for the compact help bar when the full
// list is too wide for the terminal.
type helpSubset []key.Binding

func (s helpSubset) ShortHelp() []key.Binding  { return s }
func (s helpSubset) FullHelp() [][]key.Binding { return nil }

// helpBar renders the help zone: the compact key help on its own line,
// styled apart from the status bar above it. When the full compact list is
// too wide, entries drop off from the end (keeping the quit key), so the
// bar always fits and "q quit" is always visible.
func (m Model) helpBar(width int) string {
	items := m.keys.ShortHelp()
	for {
		line := m.help.View(helpSubset(items))
		if lipgloss.Width(line) <= width || len(items) <= 2 {
			return clip(line, width, m.theme.g.Ellipsis)
		}
		// Drop the second-to-last entry: the quit key is never dropped.
		items = append(items[:len(items)-2], items[len(items)-1])
	}
}

// joinBlocks concatenates two blocks line by line. Both are pre-fitted to
// exact widths, so the columns line up.
func joinBlocks(left, right string) string {
	l := strings.Split(left, "\n")
	r := strings.Split(right, "\n")
	for i := range l {
		if i < len(r) {
			l[i] += r[i]
		}
	}
	return strings.Join(l, "\n")
}

// fitLines clips a detail pane's lines to h rows, marking the cut with the
// ellipsis so the squeeze is visible.
func fitLines(lines []string, h int, ellipsis string) []string {
	if len(lines) <= h {
		return lines
	}
	lines = append([]string(nil), lines[:h]...)
	if h > 0 {
		lines[h-1] = ellipsis
	}
	return lines
}

// dash renders a possibly empty value as "-" when there is nothing to say.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// sourceText renders the addon's source for the detail pane.
func sourceText(a *service.AddonRow) string {
	src := dash(a.Source)
	if a.SourceKind != "" {
		src += " (" + a.SourceKind + ")"
	}
	return src
}

// plural renders count with its noun, pluralized the English way for the
// handful of one-word nouns the header uses.
func plural(count int, noun string) string {
	if count == 1 {
		return noun
	}
	return noun + "s"
}
