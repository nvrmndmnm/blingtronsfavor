package tui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

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
	// syncFn runs the service sync work; it is a field so tests can start
	// runs and inspect their options without touching the network.
	// Production always uses service.Sync.
	syncFn func(context.Context, service.SyncOptions, func(service.Result), service.ProgressFunc) (service.Report, error)

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
	checkOnly  bool   // the running sync is the check-only preview ("c")
	cancelSync context.CancelFunc
	run        *syncRun  // the running sync's live state, nil when idle
	live       liveState // last sample of the run state (rows, counters)
	wait       string    // rate-limit wait line while one lasts
	statusLine string
	results    map[string]service.Result // last sync result per addon id
	checkedAt  time.Time                 // when the snapshot was last loaded ("checked 14:32")
	quitting   bool
}

// NewModel builds the initial model and loads the status synchronously so
// the first frame already has rows.
func NewModel(ctx context.Context, opts Options, rt *runtime) Model {
	if rt == nil {
		rt = &runtime{}
	}
	spin := spinner.New(spinner.WithSpinner(spinner.Dot))
	tbl := table.New(table.WithColumns(columnsFor(0)), table.WithHeight(10), table.WithFocused(true))
	m := Model{
		ctx:     ctx,
		rt:      rt,
		syncFn:  service.Sync,
		table:   tbl,
		spin:    spin,
		help:    help.New(),
		keys:    newKeyMap(),
		results: map[string]service.Result{},
	}
	if st, err := service.LoadStatus(); err == nil {
		m.status = st
		m.statusLine = "ready — q quits, u updates all"
		m.checkedAt = time.Now()
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
		cells := table.Row{a.Name, a.Installed, a.Upstream, service.StrategyLabel(state.Strategy(a.Strategy)), m.statusText(a)}
		if m.failed(a.ID) {
			// A failed row stays visibly failed after the run (red).
			for i := range cells {
				cells[i] = failedStyle.Render(cells[i])
			}
		}
		rows = append(rows, cells)
	}
	m.table.SetRows(rows)
}

// statusText renders the Status cell — the addon's live row while a sync
// runs: the per-row spinner and the phase its unit is working through (in
// the CLI live block's words), then the outcome it flips in place to,
// then — with no sync on record — its TOC verdict.
func (m *Model) statusText(a service.AddonRow) string {
	if l, ok := m.live.rows[a.ID]; ok && l.result == nil {
		if livePhase(l.phase) {
			s := m.spin.View() + string(l.phase)
			if l.detail.Reason != "" {
				s += ": " + clean(l.detail.Reason)
			}
			return s
		}
		return terminalText(l)
	}
	if r, ok := m.resultFor(a.ID); ok {
		if s := outcomeText(r); s != "" {
			return s
		}
	}
	return a.Status
}

// outcomeText renders one decided outcome in the CLI live block's words:
// "up to date", "updated 12.1.17 → 12.1.18", "failed: <reason>". A
// check-only preview renders what would change — "update 12.1.17 →
// 12.1.18" ("update → 12.1.18" with no known installed version) — and
// touches nothing.
func outcomeText(r service.Result) string {
	switch r.Kind {
	case service.ResultUpdated:
		if r.From == "-" {
			return "installed " + r.To
		}
		return "updated " + r.From + " → " + r.To
	case service.ResultUpToDate:
		return "up to date"
	case service.ResultAvailable:
		if r.From == "" || r.From == "-" {
			return "update → " + r.To
		}
		return "update " + r.From + " → " + r.To
	case service.ResultSkipped:
		return "manual"
	case service.ResultFailed:
		s := "failed"
		if r.Err != nil {
			s += ": " + clean(r.Err.Error())
		}
		return s
	}
	return ""
}

// terminalText renders a terminal event in the CLI live block's words —
// "done: 12.1.18", "failed: <error>", "skipped: <reason>" — until the
// decided outcome lands in the row.
func terminalText(l liveRow) string {
	switch l.phase {
	case service.PhaseFailed:
		if l.detail.Err != nil {
			return "failed: " + clean(l.detail.Err.Error())
		}
		return "failed"
	case service.PhaseSkipped:
		if l.detail.Reason != "" {
			return "skipped: " + clean(l.detail.Reason)
		}
		return "skipped"
	default:
		if l.detail.Version != "" {
			return "done: " + clean(l.detail.Version)
		}
		return "done"
	}
}

// resultFor returns the addon's sync outcome: the running sync's decided
// outcome first, then the last recorded one.
func (m Model) resultFor(id string) (service.Result, bool) {
	if l, ok := m.live.rows[id]; ok && l.result != nil {
		return *l.result, true
	}
	r, ok := m.results[id]
	return r, ok
}

// failed reports whether the addon's outcome is a failure — the row that
// must stay visibly failed after the run.
func (m Model) failed(id string) bool {
	r, ok := m.resultFor(id)
	return ok && r.Kind == service.ResultFailed
}

// runFrame renders the sync run frame the status bar shows while a run
// goes: how far the run has come and how it is going — "syncing 2/4 · 1
// done · 1 failed". The check-only preview ("c") frames itself as
// "checking" — it resolves and reports, but installs nothing.
func (m Model) runFrame() string {
	verb := "syncing"
	if m.checkOnly {
		verb = "checking"
	}
	if m.live.total == 0 {
		return verb + "…"
	}
	s := fmt.Sprintf("%s %d/%d · %d done · %d failed",
		verb, m.live.done+m.live.failed+m.live.skipped, m.live.total, m.live.done, m.live.failed)
	if m.live.skipped > 0 {
		s += fmt.Sprintf(" · %d skipped", m.live.skipped)
	}
	return s
}

// clean strips control bytes from text carried through events and errors,
// so a reason can never smuggle a line break or an escape into a cell.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
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
