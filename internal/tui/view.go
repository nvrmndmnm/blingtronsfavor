package tui

import (
	"fmt"
	"strings"

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
)

// layout fits the table into the terminal: the header, status line and help
// bar take fixed rows, the detail pane opens and closes.
func (m *Model) layout() {
	overhead := 5 // header (2) + status (1) + help (1) + margin
	if m.detail {
		overhead += 7
	}
	h := m.height - overhead
	if h < 3 {
		h = 3
	}
	m.table.SetHeight(h)
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
	b.WriteString(m.statusBar())
	b.WriteString("\n")
	b.WriteString(m.helpBar())
	return b.String()
}

// header renders the flavor badge — "Forever 1.60.1" — and the client root.
func (m Model) header() string {
	badge := m.client.FlavorName
	if m.client.Version != "" {
		badge += " " + m.client.Version
	}
	return badge + "  " + dimStyle.Render(m.client.Root)
}

// detailPane renders the selected addon's detail: source, tracking, plan
// reason of the last sync and the owned directories.
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
	if r, ok := m.results[a.ID]; ok && r.Plan.Reason != "" {
		lines = append(lines, "Plan      "+r.Plan.Reason)
	}
	width := m.width - 4
	if width < 20 {
		width = 20
	}
	return detailStyle.Width(width).Render(strings.Join(lines, "\n"))
}

// statusBar renders the status line, with the spinner while a sync runs.
func (m Model) statusBar() string {
	if m.syncing {
		return m.spin.View() + " " + m.statusLine
	}
	return statusStyle.Render(m.statusLine)
}

// helpBar renders the compact key help.
func (m Model) helpBar() string {
	return m.help.View(m.keys)
}
