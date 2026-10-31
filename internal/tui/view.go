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
// size — header bar, table with its detail pane, optional filter line, add
// input and rate-limit wait line, then the status bar and the help bar as
// separate zones. Every zone is sized by planScreen, so the composition
// always fits the terminal and the status and help lines can never
// overwrite each other.
func (m Model) render() string {
	th := m.theme
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	// The help overlay is a screen of its own: every keybinding grouped by
	// context, closable with ? or esc.
	if m.helpOpen {
		return fitBlock(m.helpScreen(w, h), w, h)
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

	// Add-flow geometry: the input line, its validation hint (once there is
	// something to validate) and the target-flavor prompt.
	addH := 0
	if m.addInput {
		addH = 1
		if m.addText != "" {
			addH++ // the validation hint or error line
		}
	}
	if m.addChoose {
		addH++
	}

	// Detail pane geometry feeds the plan: the overlay needs its content
	// height up front, the side pane is sized by planScreen itself.
	detailW := maxInt(8, w-4)
	overlayNeed := 0
	if m.detail {
		overlayNeed = len(m.detailContent(a, detailW)) + 2 // border rows
	}
	plan := planScreen(w, h, m.detail, filterOn, m.wait != "", addH, overlayNeed)

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
	if m.addInput {
		b.WriteString("\n")
		b.WriteString(m.addLine(w))
		if m.addText != "" {
			b.WriteString("\n")
			b.WriteString(m.addHintLine(w))
		}
	}
	if m.addChoose {
		b.WriteString("\n")
		b.WriteString(m.flavorPrompt(w))
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

// addLine renders the add input line: the reference being typed with the
// input cursor.
func (m Model) addLine(width int) string {
	th := m.theme
	line := th.dim.Render("add: ") + m.addText + th.accent.Render(th.g.Cursor)
	return clip(line, width, th.g.Ellipsis)
}

// addHintLine renders the add input's live validation: the parse error in
// the failure color, or the parsed reference the way the add flow will
// install it.
func (m Model) addHintLine(width int) string {
	th := m.theme
	switch {
	case m.addErr != nil:
		return th.bad.Render(clip(clean(m.addErr.Error()), width, th.g.Ellipsis))
	case m.addParsed.Kind != "":
		return th.dim.Render(clip(th.g.Arrow+" "+addRefLabel(m.addParsed), width, th.g.Ellipsis))
	}
	return ""
}

// addRefLabel renders the parsed reference the way the add flow will
// install it: "github owner/repo", "wago <key>", "curseforge <key>" or
// "git <remote>".
func addRefLabel(ref service.AddRef) string {
	switch ref.Kind {
	case state.SourceGitHub:
		return "github " + ref.Repo.Slug()
	case state.SourceWago:
		return "wago " + ref.Key
	case state.SourceCfwidget:
		return "curseforge " + ref.Key
	default:
		return "git " + ref.URL
	}
}

// flavorPrompt renders the target-flavor question asked when several
// clients are configured: the flavors as numbered choices with the cursor
// on the active one — the default Enter accepts.
func (m Model) flavorPrompt(width int) string {
	th := m.theme
	parts := make([]string, 0, len(m.status.Clients))
	for i, c := range m.status.Clients {
		label := fmt.Sprintf("%d %s", i+1, c.FlavorName)
		if c.Flavor == m.client.Flavor {
			label += " (active)"
		}
		if i == m.addCursor {
			label = th.title.Render(label)
		}
		parts = append(parts, label)
	}
	return clip("add to flavor: "+strings.Join(parts, th.dim.Render(" "+th.g.MidDot+" "))+
		th.dim.Render(" "+th.g.Dash+" up/down picks, enter adds, esc cancels"), width, th.g.Ellipsis)
}

// waitLine renders the rate-limit wait line: the live reason one unit is
// stalled ("rate limited, retry after 5m") while the wait lasts.
func (m Model) waitLine(width int) string {
	return m.theme.warn.Render(clip(m.wait, width, m.theme.g.Ellipsis))
}

// statusBar renders the status zone: the pending quit confirmation while
// one is up — it states what quitting does to the in-flight install — then
// the run frame beside the spinner while a sync or add runs: how far the
// run has come and how it is going. It occupies exactly one line of its
// own.
func (m Model) statusBar(width int) string {
	th := m.theme
	if m.quitAsk != "" {
		return th.warn.Render(clip(m.quitAsk, width, th.g.Ellipsis))
	}
	line := m.statusLine
	if m.syncing || m.adding {
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
// too wide, entries drop off before the pinned tail (the help and quit
// keys), so the bar always fits and "? help" and "q quit" are always
// visible.
func (m Model) helpBar(width int) string {
	items := m.keys.ShortHelp()
	for {
		line := m.help.View(helpSubset(items))
		if lipgloss.Width(line) <= width || len(items) <= 2 {
			return clip(line, width, m.theme.g.Ellipsis)
		}
		// Drop the last entry before the pinned help and quit keys.
		items = append(items[:len(items)-3], items[len(items)-2:]...)
	}
}

// helpScreen renders the full help overlay: every keybinding grouped by
// the context it is active in (keys.helpSections), in a bordered box that
// fills the terminal. The closing keys live in the box title, so the
// content never competes with them for the last line, and the section
// separators are dropped before any key is squeezed off screen.
func (m Model) helpScreen(width, height int) string {
	th := m.theme
	contentW := maxInt(8, width-4)
	inner := height - 2 // the border rows
	if inner < 1 {
		return fitBlock(th.dim.Render("no room for the help overlay"), width, maxInt(1, height))
	}
	var rows []string
	for i, sec := range m.keys.helpSections() {
		if i > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, th.title.Render(sec.title))
		rows = append(rows, wrapHelpRows(sec.rows, contentW, th)...)
	}
	rows = fitHelpRows(rows, inner, th.g.Ellipsis)

	title := " help " + th.g.Dash + " ? or esc closes "
	top := th.box.TopLeft + th.box.Top + title +
		strings.Repeat(th.box.Top, maxInt(0, contentW+1-lipgloss.Width(title))) + th.box.TopRight
	bottom := th.box.BottomLeft + strings.Repeat(th.box.Bottom, contentW+2) + th.box.BottomRight
	block := []string{top}
	for _, line := range rows {
		block = append(block, th.box.Left+" "+padLine(clip(line, contentW, ""), contentW)+" "+th.box.Right)
	}
	block = append(block, bottom)
	return strings.Join(block, "\n")
}

// wrapHelpRows renders help rows as "key desc" items and wraps them to
// width, so the overlay fits narrow terminals without dropping keys.
func wrapHelpRows(rows []helpRow, width int, th theme) []string {
	items := make([]string, 0, len(rows))
	for _, r := range rows {
		items = append(items, th.title.Render(r.key)+" "+th.dim.Render(r.desc))
	}
	sep := th.dim.Render(" " + th.g.MidDot + " ")
	return wrapItems(items, sep, width)
}

// wrapItems packs items onto lines of at most width cells, joining the
// items sharing a line with sep. An item wider than a line gets a line of
// its own — it is clipped later, never split.
func wrapItems(items []string, sep string, width int) []string {
	sepW := lipgloss.Width(sep)
	var lines []string
	line := ""
	lineW := 0
	for _, item := range items {
		w := lipgloss.Width(item)
		switch {
		case line == "":
			line, lineW = item, w
		case lineW+sepW+w <= width:
			line += sep + item
			lineW += sepW + w
		default:
			lines = append(lines, line)
			line, lineW = item, w
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// fitHelpRows fits the overlay content into h rows: when it overflows, the
// blank section separators go first and only then does the squeeze mark
// rows off the end, so every key stays visible as long as possible.
func fitHelpRows(rows []string, h int, ellipsis string) []string {
	if len(rows) > h {
		compacted := make([]string, 0, len(rows))
		for _, r := range rows {
			if r != "" {
				compacted = append(compacted, r)
			}
		}
		rows = compacted
	}
	rows = fitLines(rows, h, ellipsis)
	for len(rows) < h {
		rows = append(rows, "")
	}
	return rows
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
