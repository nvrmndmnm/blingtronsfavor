package tui

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"sync"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbletea/v2"

	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
)

// Run opens the TUI. The context is signal-aware: SIGINT cancels an
// in-flight sync through the installer's rollback before anything else.
// Run waits for in-flight service calls to finish before returning, so
// nothing is left half-done behind the program.
func Run(opts Options) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	rt := &runtime{}
	program := tea.NewProgram(NewModel(ctx, opts, rt))
	rt.setProgram(program)
	_, err := program.Run()
	rt.wg.Wait()
	return err
}

// Options configures the TUI entry point.
type Options struct {
	// Version is the tool version reported to upstreams; the service layer
	// owns the actual user agent.
	Version string
}

// runtime is the machinery shared between the program harness and the
// model: the program handle goroutines report through, and the wait group
// that keeps the process alive until in-flight service calls finish.
type runtime struct {
	mu      sync.Mutex
	program *tea.Program
	wg      sync.WaitGroup
}

// send delivers a message from a goroutine to the program's event loop.
func (rt *runtime) send(msg tea.Msg) {
	rt.mu.Lock()
	p := rt.program
	rt.mu.Unlock()
	if p != nil {
		p.Send(msg)
	}
}

// setProgram records the program goroutines report through.
func (rt *runtime) setProgram(p *tea.Program) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.program = p
}

// Model is the TUI model: the addon table with the header, detail pane and
// status line around it. It holds no business logic — every action is a call
// into internal/service, the same functions the CLI uses.
type Model struct {
	ctx context.Context
	rt  *runtime

	status service.Status
	client service.ClientRow
	rows   []service.AddonRow // visible rows, in table order
	table  table.Model
	spin   spinner.Model
	help   help.Model
	keys   keyMap

	width, height int

	detail     bool   // detail pane open
	filtering  bool   // "/" filter input active
	filter     string // case-insensitive name filter
	confirm    bool   // delete confirmation pending
	syncing    bool   // a service.Sync is running
	cancelSync context.CancelFunc
	statusLine string
	results    map[string]service.Result // last sync result per addon id
	quitting   bool
}

// NewModel builds the initial model and loads the status synchronously so
// the first frame already has rows.
func NewModel(ctx context.Context, opts Options, rt *runtime) Model {
	if rt == nil {
		rt = &runtime{}
	}
	spin := spinner.New(spinner.WithSpinner(spinner.Dot))
	cols := []table.Column{
		{Title: "Addon", Width: 24},
		{Title: "Installed", Width: 12},
		{Title: "Upstream", Width: 14},
		{Title: "Strategy", Width: 13},
		{Title: "Status", Width: 18},
	}
	tbl := table.New(table.WithColumns(cols), table.WithHeight(10), table.WithFocused(true))
	m := Model{
		ctx:     ctx,
		rt:      rt,
		table:   tbl,
		spin:    spin,
		help:    help.New(),
		keys:    newKeyMap(),
		results: map[string]service.Result{},
	}
	if st, err := service.LoadStatus(); err == nil {
		m.status = st
		m.statusLine = "ready — q quits, u updates all"
	} else {
		m.statusLine = "error: " + err.Error()
	}
	m.pickClient()
	m.refreshRows()
	return m
}

// pickClient selects the client the table shows: the first recorded client
// (sorted by flavor). With no client the table is empty and the view shows
// the empty state.
func (m *Model) pickClient() {
	if len(m.status.Clients) == 0 {
		m.client = service.ClientRow{}
		return
	}
	m.client = m.status.Clients[0]
}

// refreshRows rebuilds the visible rows (applying the filter) and the table
// rows on top of them.
func (m *Model) refreshRows() {
	m.rows = nil
	for _, a := range m.client.Addons {
		if m.filter != "" && !containsFold(a.Name, m.filter) {
			continue
		}
		m.rows = append(m.rows, a)
	}
	rows := make([]table.Row, 0, len(m.rows))
	for _, a := range m.rows {
		rows = append(rows, table.Row{a.Name, a.Installed, a.Upstream, service.StrategyLabel(state.Strategy(a.Strategy)), m.statusText(a)})
	}
	m.table.SetRows(rows)
}

// statusText renders the Status column: the last sync outcome for the addon
// when one exists, otherwise its TOC verdict.
func (m *Model) statusText(a service.AddonRow) string {
	r, ok := m.results[a.ID]
	if !ok {
		return a.Status
	}
	switch r.Kind {
	case service.ResultUpdated:
		return "updated " + r.From + " → " + r.To
	case service.ResultUpToDate:
		return "up to date"
	case service.ResultAvailable:
		return "update " + r.To
	case service.ResultSourceBuild:
		return "source build"
	case service.ResultSkipped:
		return "manual"
	case service.ResultFailed:
		return "failed"
	default:
		return a.Status
	}
}

// selected returns the addon the table cursor points at, or nil.
func (m *Model) selected() *service.AddonRow {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.rows) {
		return nil
	}
	return &m.rows[i]
}

// containsFold reports whether haystack contains needle,
// case-insensitively.
func containsFold(haystack, needle string) bool {
	return needle == "" || strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
