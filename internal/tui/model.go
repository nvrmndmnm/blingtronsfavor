package tui

import (
	"context"
	"fmt"
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

	detail        bool               // detail pane open
	filtering     bool               // "/" filter input active
	filter        string             // case-insensitive name filter
	confirm       bool               // delete confirmation pending
	addInput      bool               // "a" add input active
	addText       string             // the reference being typed
	addParsed     service.AddRef     // live parse of addText
	addErr        error              // live parse error, nil while the ref parses
	addChoose     bool               // target-flavor prompt active (several clients)
	addCursor     int                // flavor prompt cursor
	adding        bool               // a service.Add is running
	addRow        *service.AddonRow  // provisional row for the addon being added
	syncing       bool               // a service.Sync is running
	cancelSync    context.CancelFunc // cancels the running sync or add (installer rollback)
	run           *syncRun           // the running sync's live state, nil when idle
	live          liveState          // last sample of the run state (rows, counters)
	wait          string             // rate-limit wait line while one lasts
	statusLine    string
	results       map[string]service.Result // last sync result per addon id
	pendingFlavor string                    // flavor to show once the next snapshot lands (after an add)
	quitting      bool
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

// activeClientIndex is the position of the active client in the status
// report — the target-flavor prompt's default (the one Enter accepts).
func (m Model) activeClientIndex() int {
	for i, c := range m.status.Clients {
		if c.Flavor == m.client.Flavor {
			return i
		}
	}
	return 0
}

// refreshRows rebuilds the visible rows (applying the filter) and the table
// rows on top of them. While an add runs, the addon being added gets a
// provisional row — the live-row treatment — until the refreshed snapshot
// carries the real one.
func (m *Model) refreshRows() {
	m.rows = nil
	for _, a := range m.client.Addons {
		if m.filter != "" && !containsFold(a.Name, m.filter) {
			continue
		}
		m.rows = append(m.rows, a)
	}
	if m.addRow != nil && containsFold(m.addRow.Name, m.filter) {
		seen := false
		for _, a := range m.rows {
			if a.ID == m.addRow.ID {
				seen = true
				break
			}
		}
		if !seen {
			a := *m.addRow
			if l, ok := m.live.rows[a.ID]; ok && l.detail.Version != "" {
				a.Upstream = clean(l.detail.Version)
			}
			m.rows = append(m.rows, a)
		}
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

// validateAdd re-parses the add input through the same parsers "bfctl add"
// uses, keeping the inline hint or error in step with the typing.
func (m *Model) validateAdd() {
	m.addParsed = service.AddRef{}
	m.addErr = nil
	if m.addText == "" {
		return
	}
	ref, err := service.ParseAddRef(m.addText)
	m.addParsed, m.addErr = ref, err
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
// "up to date", "updated 12.1.17 → 12.1.18", "failed: <reason>".
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
		return "update " + r.To
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

// runFrame renders the run frame the status bar shows while a sync or add
// runs: how far the run has come and how it is going — "syncing 2/4 · 1
// done · 1 failed".
func (m Model) runFrame() string {
	verb := "syncing"
	if m.adding {
		verb = "adding"
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
