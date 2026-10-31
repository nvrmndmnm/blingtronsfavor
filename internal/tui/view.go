package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
)

var (
	headerStyle = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	detailStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	statusStyle = lipgloss.NewStyle().Faint(true)
	failedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

const (
	// cellPad is what the table style's Padding(0, 1) adds to each cell.
	cellPad = 2
	// minStatusWidth keeps the live row and the outcome words readable:
	// "updated 12.1.17 → 12.1.18" fits without truncation.
	minStatusWidth = 26
)

// columnsFor sizes the table columns for the terminal width. The first
// four columns are fixed; Status absorbs whatever is left (never below
// minStatusWidth), so the live row and its outcome stay readable on wide
// terminals without ever being worse on narrow ones.
func columnsFor(width int) []table.Column {
	cols := []table.Column{
		{Title: "Addon", Width: 22},
		{Title: "Installed", Width: 11},
		{Title: "Upstream", Width: 12},
		{Title: "Strategy", Width: 11},
	}
	used := cellPad * (len(cols) + 1)
	for _, c := range cols {
		used += c.Width
	}
	status := width - 2 - used
	if status < minStatusWidth {
		status = minStatusWidth
	}
	return append(cols, table.Column{Title: "Status", Width: status})
}

// layout fits the table into the terminal: the header, status line and
// help bar take fixed rows, and the detail pane, filter line and
// rate-limit wait line open and close. It is a pure function of the model
// state and the terminal size (risk R1) — recompute it whenever either
// changes — and it sizes the table columns the same way.
func (m *Model) layout() {
	overhead := 5 // header (2) + status (1) + help (1) + margin
	if m.detail {
		overhead += lipgloss.Height(m.detailPane())
	}
	if m.filtering || m.filter != "" {
		overhead++
	}
	if m.wait != "" {
		overhead++
	}
	h := m.height - overhead
	if h < 3 {
		h = 3
	}
	m.table.SetHeight(h)
	m.table.SetColumns(columnsFor(m.width))
	m.table.SetWidth(m.width - 2)
}

// View satisfies tea.Model.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render composes the whole screen: header, table, optional detail pane,
// status line and the help bar.
func (m Model) render() string {
	if len(m.status.Clients) == 0 {
		return strings.Join([]string{
			headerStyle.Render("bfctl"),
			"",
			"no addons configured — run \"bfctl init --path <root>\" first",
			"",
			m.helpBar(),
		}, "\n")
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render(m.header()))
	b.WriteString("\n\n")
	b.WriteString(m.table.View())
	b.WriteString("\n")
	if m.detail {
		b.WriteString(m.detailPane())
		b.WriteString("\n")
	}
	if m.filtering || m.filter != "" {
		fmt.Fprintf(&b, "filter: %s▌\n", m.filter)
	}
	if m.wait != "" {
		b.WriteString(m.wait)
		b.WriteString("\n")
	}
	b.WriteString(m.statusBar())
	b.WriteString("\n")
	b.WriteString(m.helpBar())
	return b.String()
}

// header renders the flavor badge — "Forever 1.60.1" — the client root and
// the checked stamp ("checked 14:32"), which updates after each refresh or
// sync.
func (m Model) header() string {
	badge := m.client.FlavorName
	if m.client.Version != "" {
		badge += " " + m.client.Version
	}
	h := badge + "  " + dimStyle.Render(m.client.Root)
	if !m.checkedAt.IsZero() {
		h += "  " + dimStyle.Render("checked "+m.checkedAt.Format("15:04"))
	}
	return h
}

// detailPane renders the selected addon's detail: source, tracking, the
// error and plan reason of the last sync and the owned directories.
func (m Model) detailPane() string {
	a := m.selected()
	if a == nil {
		return detailStyle.Render("no addon selected")
	}
	lines := []string{
		headerStyle.Render(a.Name),
		fmt.Sprintf("Source    %s (%s)", a.Source, a.SourceKind),
		fmt.Sprintf("Tracking  %s", a.Tracking),
		fmt.Sprintf("Strategy  %s", service.StrategyLabel(state.Strategy(a.Strategy))),
		fmt.Sprintf("Installed %s", a.Installed),
		fmt.Sprintf("Status    %s", a.Status),
	}
	if len(a.Dirs) > 0 {
		lines = append(lines, "Dirs      "+strings.Join(a.Dirs, ", "))
	}
	if r, ok := m.resultFor(a.ID); ok {
		if r.Err != nil {
			lines = append(lines, "Error     "+clean(r.Err.Error()))
		}
		if r.Plan.Reason != "" {
			lines = append(lines, "Plan      "+r.Plan.Reason)
		}
	}
	width := m.width - 4
	if width < 20 {
		width = 20
	}
	return detailStyle.Width(width).Render(strings.Join(lines, "\n"))
}

// statusBar renders the status line: the sync run frame — "syncing 2/4 ·
// 1 done · 1 failed" — beside the spinner while a sync runs (any message
// the user's keys set shows next to it), and the plain status line
// otherwise.
func (m Model) statusBar() string {
	if m.syncing {
		line := m.spin.View() + m.runFrame()
		if m.statusLine != "" {
			line += "  " + statusStyle.Render(m.statusLine)
		}
		return line
	}
	return statusStyle.Render(m.statusLine)
}

// helpBar renders the compact key help.
func (m Model) helpBar() string {
	return m.help.View(m.keys)
}
