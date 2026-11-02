package tui

import (
	"fmt"
	"strconv"
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
// separate zones. With no client configured it renders the guided-init
// screen instead of the table (A6). Every zone is sized by planScreen, so
// the composition always fits the terminal and the status and help lines
// can never overwrite each other.
func (m Model) render() string {
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
		return fitBlock(m.onboardingScreen(w, h), w, h)
	}

	a := m.selected()
	filterOn := m.filtering || m.filter != ""

	// Add-flow geometry: the input line, its validation hint (once there is
	// something to validate) and the target-flavor prompt.
	addH := m.addHeight()

	// The detail pane is planned like every other zone: open, it takes
	// the space the shrunken list leaves (the dedicated detail view).
	plan := planScreen(w, h, m.detail, filterOn, m.wait != "", addH)

	// Table geometry: responsive columns from the terminal width and the
	// visible content, then the rows styled and truncated to those widths.
	plain := make([][]string, 0, len(m.rows))
	classes := make([]statusClass, 0, len(m.rows))
	for _, addon := range m.rows {
		cells, class := m.plainCells(addon)
		plain = append(plain, cells)
		classes = append(classes, class)
	}
	// The active sort column's header carries the sort mark (sortSpecs
	// prefixes it and feeds the width planning, so it never clips).
	specs := m.sortSpecs()
	widths := columnWidths(tableAvail(plan.tableW, len(specs)), specs, naturalWidths(specs, plain))
	cols := make([]table.Column, len(specs))
	for i, spec := range specs {
		cols[i] = table.Column{Title: spec.title, Width: widths[i]}
	}
	m.table.SetColumns(cols)
	m.table.SetRows(m.styledRows(plain, classes, widths))
	m.table.SetHeight(plan.tableH)
	m.table.SetWidth(plan.tableW)

	body := fitBlock(m.table.View(), plan.tableW, plan.tableH)
	switch {
	case m.filter != "" && len(m.rows) == 0:
		// Zero matches shows the empty state in place of the grid: it
		// names the filter and suggests clearing it.
		body = fitBlock(m.filterEmptyState(), plan.tableW, plan.tableH)
	case m.filter == "" && m.addRow == nil && len(m.rows) == 0:
		// Configured but zero addons: the hint panel instead of an empty
		// grid (A6).
		body = fitBlock(m.zeroAddonState(), plan.tableW, plan.tableH)
	}

	var b strings.Builder
	b.WriteString(m.headerBar(w))
	b.WriteString("\n")
	b.WriteString(body)
	if plan.detailH > 0 {
		b.WriteString("\n")
		b.WriteString(m.detailPane(a, plan))
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

// plainCells renders one addon's six cells before truncation and styling:
// name, acquisition source, the two versions, the strategy and the status.
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
		sourceLabel(a),
		a.Installed,
		a.Upstream,
		service.StrategyLabel(state.Strategy(a.Strategy)),
		cell,
	}, class
}

// headerBar renders the styled header bar: flavor badge, game version,
// client root and the checked stamp ("checked 14:32", updating after each
// refresh or sync) on the left, the live addon counts on the right. The
// stamp belongs to a configured client: the guided-init screen has
// checked nothing, so it shows no stamp and the wall clock never enters
// the frame.
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
	if m.client.Flavor != "" {
		if !m.checkedAt.IsZero() {
			right = th.dim.Render("checked " + m.checkedAt.Format("15:04"))
		}
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

// addHeight is the vertical room the add flow's lines want right now: the
// input line, its validation hint (once there is something to validate) and
// the target-flavor prompt.
func (m Model) addHeight() int {
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
	return addH
}

// detailContent renders the selected addon's detail: every table value at
// full length (truncated cells reveal it here), the plain-language TOC
// verdict, the friendly acquisition source the Source column names, the
// owned directories, the last sync's error and plan reason, and the
// installed version's changelog — sanitized and clamped so upstream text
// cannot corrupt the pane (R7).
func (m Model) detailContent(a *service.AddonRow, width int) []string {
	th := m.theme
	if a == nil {
		return []string{th.dim.Render("no addon selected")}
	}
	word := m.statusText(*a)
	class := classifyStatus(word)
	fields := []detailField{
		{label: "Source", value: sourceLabel(*a), style: th.dim},
		{label: "Tracking", value: dash(a.Tracking), style: th.dim},
		{label: "Strategy", value: service.StrategyLabel(state.Strategy(a.Strategy)), style: th.dim},
		{label: "Installed", value: dash(a.Installed), style: th.dim},
		{label: "Upstream", value: dash(a.Upstream), style: th.dim},
		{label: "Status", value: th.statusGlyph(class) + " " + m.plainStatus(*a, word), style: th.statusStyle(class)},
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
	lines = append(lines, renderFields(fields, width, th)...)
	if notes := changelogText(a.Changelog, th.g.Ellipsis); notes != "" {
		lines = append(lines, "")
		lines = append(lines, th.title.Render("Changelog"))
		lines = append(lines, wrapText(notes, width)...)
	}
	return lines
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

// detailPane renders the expanded detail pane of the dedicated detail
// view (XD-56): a bordered box below the shrunken list, taking the whole
// remaining width and height so the selected addon's full values get the
// screen. Short content pads out inside the box, long content scrolls.
func (m Model) detailPane(a *service.AddonRow, plan screenPlan) string {
	th := m.theme
	inner := plan.detailH - 2
	if inner < 1 {
		return fitBlock(th.dim.Render("no room for the detail pane"), plan.width, maxInt(1, plan.detailH))
	}
	contentW := plan.detailW
	lines := fitLines(m.detailContent(a, contentW), m.detailScroll, inner, th.g.Ellipsis)
	for len(lines) < inner {
		lines = append(lines, "")
	}
	top := th.box.TopLeft + strings.Repeat(th.box.Top, contentW+2) + th.box.TopRight
	bottom := th.box.BottomLeft + strings.Repeat(th.box.Bottom, contentW+2) + th.box.BottomRight
	block := []string{top}
	for _, line := range lines {
		block = append(block, th.box.Left+" "+padLine(clip(line, contentW, ""), contentW)+" "+th.box.Right)
	}
	block = append(block, bottom)
	return fitBlock(strings.Join(block, "\n"), plan.width, plan.detailH)
}

// detailViewport reports the detail pane's scroll geometry at the current
// size: the selected addon's content lines and how many of them the pane
// shows at once. The scroll keys clamp against it and the page keys step
// it; render() plans the same geometry through planScreen.
func (m Model) detailViewport() (lines []string, visible int) {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	a := m.selected()
	plan := planScreen(w, h, m.detail, m.filtering || m.filter != "", m.wait != "", m.addHeight())
	return m.detailContent(a, plan.detailW), maxInt(1, plan.detailH-2)
}

// filterLine renders the active filter: the text being typed or applied
// with the input cursor while typing, and the match count beside it
// ("filter: dbm — 2 of 12") so the filter's state and reach are always
// visible while one is active.
func (m Model) filterLine(width int) string {
	th := m.theme
	line := th.dim.Render("filter: ") + clean(m.filter)
	if m.filtering {
		line += th.accent.Render(th.g.Cursor)
	}
	match, total := m.filterCounts()
	line += fmt.Sprintf(" %s %d of %d", th.g.Dash, match, total)
	return clip(line, width, th.g.Ellipsis)
}

// filterEmptyState renders the zero-match state in place of the grid: it
// names the filter that matched nothing and suggests clearing it (esc),
// instead of leaving a blank table.
func (m Model) filterEmptyState() string {
	th := m.theme
	return th.title.Render(fmt.Sprintf("no addons match \"%s\"", clean(m.filter))) + "\n" +
		th.dim.Render("press esc to clear the filter")
}

// zeroAddonState renders the configured-but-zero-addons state in place of
// the empty grid (A6): the hint panel that points at the keys that build
// the collection — "press a to add, u to sync".
func (m Model) zeroAddonState() string {
	th := m.theme
	return th.title.Render("no addons yet") + "\n" +
		"\n" +
		th.dim.Render("press a to add your first addon") + "\n" +
		th.dim.Render("press u to sync") + "\n" +
		th.dim.Render("refs: owner/repo, a wago.io or curseforge.com URL, or a git remote")
}

// onboardingScreen renders the guided-init screen shown instead of the
// table while no client is configured (A6): the detected installs as a
// picker — enter picks one and performs the init behind the scenes — or
// the inline path prompt with its live detection hint, then the status
// zone carrying the tip line and the onboarding help bar. With nothing
// detected the prompt is the screen from the start.
func (m Model) onboardingScreen(w, h int) string {
	th := m.theme
	lines := []string{m.headerBar(w), ""}
	if m.pathInput {
		lines = append(lines, th.title.Render("set up bfctl "+th.g.Dash+" enter your World of Warcraft flavor directory"))
		lines = append(lines, "")
		lines = append(lines, m.pathLine(w))
		if m.pathText != "" {
			lines = append(lines, m.pathHintLine(w))
		}
	} else {
		lines = append(lines, th.title.Render("set up bfctl "+th.g.Dash+" pick your World of Warcraft install"))
		lines = append(lines, "")
		entries := m.pickerLines(w)
		// With more entries than room the window follows the cursor, so
		// the selection never scrolls out of sight.
		room := maxInt(1, h-2-len(lines))
		if len(entries) > room {
			start := clampInt(m.pickerCursor-room+1, 0, len(entries)-room)
			entries = entries[start : start+room]
		}
		lines = append(lines, entries...)
	}
	// The status and help bars keep their own bottom lines whatever the
	// size: the content yields, never the bars.
	if over := len(lines) - maxInt(0, h-2); over > 0 {
		lines = lines[:maxInt(0, h-2)]
	}
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	lines = append(lines, m.statusBar(w))
	lines = append(lines, m.onboardingHelp(w))
	return strings.Join(lines, "\n")
}

// pickerLines renders the detected installs as picker rows — the flavor
// badge and root path of each — plus the manual-path entry the picker
// falls back to. The active row wears the accent and carries the "> "
// marker, so the selection never depends on the color alone.
func (m Model) pickerLines(width int) []string {
	th := m.theme
	lines := make([]string, 0, len(m.picker)+1)
	for i, inst := range m.picker {
		badge := inst.Badge()
		path := truncateMiddle(inst.FlavorDir, maxInt(8, width-6-lipgloss.Width(badge)), th.g.Ellipsis)
		lines = append(lines, pickerRow(i == m.pickerCursor, badge+" "+th.g.Dash+" "+path, th))
	}
	lines = append(lines, pickerRow(m.pickerCursor == len(m.picker), "type a flavor directory path manually", th))
	return lines
}

// pickerRow renders one picker row: the selection marker and the accent
// on the active row, plain text on the rest.
func pickerRow(active bool, label string, th theme) string {
	if active {
		return th.accent.Render("> " + label)
	}
	return "  " + label
}

// pathLine renders the manual path prompt: the flavor directory being
// typed with the input cursor.
func (m Model) pathLine(width int) string {
	th := m.theme
	line := th.dim.Render("path: ") + m.pathText + th.accent.Render(th.g.Cursor)
	return clip(line, width, th.g.Ellipsis)
}

// pathHintLine renders the prompt's live validation: the detected install
// the init will record, or the detection error — never ejecting the user
// to the shell to find out (A6).
func (m Model) pathHintLine(width int) string {
	th := m.theme
	switch {
	case m.pathErr != nil:
		return th.bad.Render(clip(clean(m.pathErr.Error()), width, th.g.Ellipsis))
	case m.pathInstall != nil:
		return th.dim.Render(clip(th.g.Arrow+" "+m.pathInstall.Badge(), width, th.g.Ellipsis))
	}
	return ""
}

// onboardingHelp renders the guided-init help line: only the keys the
// picker and the path prompt answer — the actionable controls, never the
// narrated typing and erasing every input speaks for — so the bar never
// offers a key that does nothing on this screen.
func (m Model) onboardingHelp(width int) string {
	th := m.theme
	var items []key.Binding
	if m.pathInput {
		esc := "back"
		if len(m.picker) == 0 {
			esc = "clear"
		}
		items = []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "set up")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", esc)),
		}
	} else {
		items = []key.Binding{
			key.NewBinding(key.WithKeys("up", "down"), key.WithHelp(th.g.UpDown, "pick")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "set up")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
	}
	return clip(m.help.View(helpSubset(items)), width, th.g.Ellipsis)
}

// addLine renders the add input line: the reference being typed with the
// input cursor at its editing position (the cursor follows the arrow and
// word-motion keys, so mid-line edits land where they should — XD-56).
func (m Model) addLine(width int) string {
	th := m.theme
	runes := []rune(m.addText)
	cur := clampInt(m.addCur, 0, len(runes))
	line := th.dim.Render("add: ") + string(runes[:cur]) + th.accent.Render(th.g.Cursor) + string(runes[cur:])
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
// run has come and how it is going. The tip line — the three most useful
// keys for the current state (A6) — rides beside whatever the bar has to
// say: whole tips drop one by one before any of them is cut, and the
// message stands alone only once no tip fits (the help bar below still
// lists the keys). It occupies exactly one line of its own.
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
	sep := " " + th.g.MidDot + " "
	for tips := m.tips(); len(tips) > 0; tips = tips[:len(tips)-1] {
		tip := th.dim.Render(strings.Join(tips, sep))
		if line == "" {
			return clip(tip, width, th.g.Ellipsis)
		}
		if lipgloss.Width(line)+lipgloss.Width(sep)+lipgloss.Width(tip) <= width {
			return clip(line+sep+tip, width, th.g.Ellipsis)
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
	rows = fitLines(rows, 0, h, ellipsis)
	for len(rows) < h {
		rows = append(rows, "")
	}
	return rows
}

// fitLines clips a detail pane's lines to h rows at the scroll offset,
// marking the cut with the ellipsis so the squeeze is visible. The offset
// clamps to the last full page, so the pane can never scroll past the end
// of the content (R7).
func fitLines(lines []string, scroll, h int, ellipsis string) []string {
	lines = lines[clampInt(scroll, 0, maxInt(0, len(lines)-h)):]
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

// sourceLabel names how the addon was acquired — the value the Source
// column shows and the detail pane's Source field repeats (XD-56): the
// friendly acquisition source, "github release" for a release asset,
// "github built" for a repository built from source, "curseforge" (never
// the internal cfwidget name), "wago", or the plain provider name (a
// generic git remote reads "git").
func sourceLabel(a service.AddonRow) string {
	switch state.SourceKind(a.SourceKind) {
	case state.SourceGitHub:
		switch state.Strategy(a.Strategy) {
		case state.StrategyRelease:
			return "github release"
		case state.StrategySourcePkgmeta:
			return "github built"
		}
		return "github"
	case state.SourceCfwidget:
		return "curseforge"
	case "":
		return "-"
	}
	return a.SourceKind
}

// plainStatus expands the bare TOC verdict jargon into the plain-language
// line the detail pane shows ("matches Forever 1.60.1", "built for 110002
// — may be outdated"). A live run's or a sync outcome's status word is
// already plain and passes through unchanged.
func (m Model) plainStatus(a service.AddonRow, word string) string {
	switch word {
	case "Match":
		return "matches " + m.clientLabel()
	case "MultiMatch":
		if len(a.BuiltFor) > 1 {
			return "matches " + m.clientLabel() + " (built for several versions)"
		}
		return "matches " + m.clientLabel()
	case "Mismatch":
		built := "an unknown interface"
		if len(a.BuiltFor) > 0 {
			built = joinInts(a.BuiltFor)
		}
		return "built for " + built + " " + m.theme.g.Dash + " may be outdated"
	case "NoToc":
		return "ships no .toc file"
	case "Missing":
		return "installed folder missing"
	case "Unknown":
		return "compatibility not checked"
	}
	return word
}

// clientLabel names the detected client the verdict is measured against:
// the flavor display name and the short game version — "Forever 1.60.1".
func (m Model) clientLabel() string {
	if label := strings.TrimSpace(m.client.FlavorName + " " + m.client.Version); label != "" {
		return label
	}
	return "the active client"
}

// joinInts renders interface numbers as one comma-separated list.
func joinInts(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

// maxChangelogRunes clamps an upstream changelog before it reaches the
// detail pane (R7): a huge release note cannot flood the pane — the cut is
// visible and the pane scrolls over the rest harmlessly.
const maxChangelogRunes = 2000

// changelogText prepares an upstream changelog for the detail pane (R7):
// line endings normalize to "\n", every other control byte is stripped —
// the sanitizeProgressText pattern, so unprintable upstream text can never
// smuggle an ANSI escape or a stray control rune into the pane — and the
// length is clamped with a visible cut marker.
func changelogText(s, ellipsis string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r < 0x20 || r == 0x7f:
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > maxChangelogRunes {
		s = strings.TrimSpace(string(runes[:maxChangelogRunes])) + ellipsis
	}
	return s
}

// plural renders count with its noun, pluralized the English way for the
// handful of one-word nouns the header uses.
func plural(count int, noun string) string {
	if count == 1 {
		return noun
	}
	return noun + "s"
}
