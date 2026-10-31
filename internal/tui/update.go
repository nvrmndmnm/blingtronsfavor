package tui

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbletea/v2"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/service"
)

// syncDoneMsg delivers the final sync report (or its error).
type syncDoneMsg struct {
	report service.Report
	err    error
}

// statusMsg carries a refreshed status snapshot. refreshed marks the
// user's own refresh (the "r" key), so the status line can acknowledge it.
type statusMsg struct {
	status    service.Status
	err       error
	refreshed bool
}

// trackMsg is the outcome of a tracking toggle.
type trackMsg struct {
	id       string
	tracking string
	err      error
}

// deleteMsg is the outcome of a delete.
type deleteMsg struct {
	id  string
	err error
}

// Init satisfies tea.Model.
func (m Model) Init() tea.Cmd {
	return nil
}

// Update satisfies tea.Model. Service work runs as commands; goroutines
// report back through the runtime so the loop stays unidirectional.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if m.syncing && m.run != nil {
			// Sample the running sync's state on the spinner tick:
			// redraws are rate-limited to the tick (risk R3), so a
			// burst of events can never flicker or spew as a batch.
			m.live = m.run.sample()
			m.wait = m.live.wait
			m.refreshRows()
			m.layout()
		}
		return m, cmd
	case syncDoneMsg:
		m.syncing = false
		m.cancelSync = nil
		m.finishSync()
		m.wait = ""
		if msg.err != nil {
			m.statusLine = "sync failed: " + msg.err.Error()
		} else {
			m.statusLine = msg.report.Summary()
			if msg.report.RateHint != "" {
				m.wait = "rate limited, " + msg.report.RateHint
			}
		}
		m.layout()
		return m, loadStatus()
	case statusMsg:
		if msg.err != nil {
			m.statusLine = "error: " + msg.err.Error()
			return m, nil
		}
		m.status = msg.status
		m.checkedAt = time.Now()
		m.pickClient()
		m.refreshRows()
		if msg.refreshed {
			m.statusLine = "refreshed — checked " + m.checkedAt.Format("15:04")
		}
		return m, nil
	case trackMsg:
		if msg.err != nil {
			m.statusLine = "error: " + msg.err.Error()
			return m, nil
		}
		if msg.tracking == "stable" {
			m.statusLine = msg.id + ": tracking releases"
		} else {
			m.statusLine = msg.id + ": tracking branch " + msg.tracking
		}
		return m, loadStatus()
	case deleteMsg:
		if msg.err != nil {
			m.statusLine = "error: " + msg.err.Error()
			return m, nil
		}
		m.statusLine = msg.id + " removed"
		return m, loadStatus()
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// handleKey routes one key press through the mode stack (filter input,
// delete confirmation, normal mode).
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// ctrl+c works in every mode: it cancels a running sync first — through
	// the installer's rollback — and quits otherwise.
	if msg.String() == "ctrl+c" {
		return m.cancelOrQuit()
	}

	if m.filtering {
		switch msg.String() {
		case "enter":
			m.filtering = false
		case "esc":
			m.filtering = false
			m.filter = ""
		case "backspace":
			if len(m.filter) > 0 {
				m.filter = m.filter[:len(m.filter)-1]
			}
		default:
			if msg.Text != "" {
				m.filter += msg.Text
			}
		}
		m.refreshRows()
		m.layout()
		return m, nil
	}

	if m.confirm {
		switch {
		case key.Matches(msg, m.keys.Confirm):
			return m.doDelete()
		case key.Matches(msg, m.keys.Cancel):
			m.confirm = false
			m.statusLine = "delete cancelled"
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.cancelOrQuit()
	case key.Matches(msg, m.keys.Detail):
		m.detail = !m.detail
		m.layout()
	case key.Matches(msg, m.keys.Filter):
		m.filtering = true
		m.statusLine = "filter: type to narrow the list, enter applies, esc clears"
	case key.Matches(msg, m.keys.Update):
		if m.syncing {
			m.statusLine = "sync already running"
			return m, nil
		}
		return m.startSync(service.SyncOptions{})
	case key.Matches(msg, m.keys.Check):
		if m.syncing {
			m.statusLine = "sync already running"
			return m, nil
		}
		return m.startSync(service.SyncOptions{CheckOnly: true})
	case key.Matches(msg, m.keys.UpdateOne):
		if m.syncing {
			m.statusLine = "sync already running"
			return m, nil
		}
		a := m.selected()
		if a == nil {
			m.statusLine = "no addon selected"
			return m, nil
		}
		unit := service.Unit{Flavor: client.Flavor(m.client.Flavor), AddonID: a.ID}
		return m.startSync(service.SyncOptions{Only: &unit})
	case key.Matches(msg, m.keys.Refresh):
		m.statusLine = "refreshing…"
		return m, refreshStatus()
	case key.Matches(msg, m.keys.Track):
		return m.doToggle()
	case key.Matches(msg, m.keys.Delete):
		if a := m.selected(); a != nil {
			m.confirm = true
			m.statusLine = fmt.Sprintf("delete %s? y confirms, esc cancels", a.Name)
		}
	default:
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	}
	return m, nil
}

// cancelOrQuit is the quit key's behavior: a running sync is cancelled
// first — its in-flight install rolls back through the installer, which
// the confirmation states — and the TUI quits on the next press.
func (m Model) cancelOrQuit() (tea.Model, tea.Cmd) {
	if m.syncing {
		m.statusLine = "cancelling — the in-flight install rolls back"
		if m.cancelSync != nil {
			m.cancelSync()
			m.cancelSync = nil
		}
		return m, nil
	}
	m.quitting = true
	return m, tea.Quit
}

// startSync kicks off service.Sync in a goroutine — the same call "bfctl
// sync" makes — wired to the live run state: the service layer's
// ProgressEvent stream and its result callback write the run state as the
// work happens (no per-event messages), and the spinner tick samples it
// into the addon rows and the status bar. The spinner keeps turning until
// the run reports back. opts scopes the run: the plain update-all ("u"),
// the check-only preview ("c") or the selected addon only ("U") — every
// run reports through the same live-row stream.
func (m Model) startSync(opts service.SyncOptions) (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(m.ctx)
	run := newSyncRun()
	m.syncing = true
	m.checkOnly = opts.CheckOnly
	m.cancelSync = cancel
	m.run = run
	m.live = liveState{}
	m.wait = ""
	m.statusLine = ""
	m.rt.wg.Add(1)
	go func() {
		defer m.rt.wg.Done()
		defer cancel()
		report, err := m.syncFn(ctx, opts, run.recordResult, run.record)
		m.rt.send(syncDoneMsg{report: report, err: err})
	}()
	var spinCmd tea.Cmd
	m.spin, spinCmd = m.spin.Update(m.spin.Tick())
	return m, spinCmd
}

// finishSync folds the last of the running sync into the model: the
// decided outcomes land in the results map — where the rows and the
// detail pane keep them after the run — and the live rows are dropped.
func (m *Model) finishSync() {
	m.checkOnly = false
	if m.run != nil {
		m.live = m.run.sample()
		m.run = nil
	}
	for id, row := range m.live.rows {
		if row.result != nil {
			m.results[id] = *row.result
		}
	}
	m.live = liveState{}
	m.refreshRows()
}

// doToggle flips the selected addon between release tracking and branch
// tracking through the service layer.
func (m Model) doToggle() (tea.Model, tea.Cmd) {
	a := m.selected()
	if a == nil {
		m.statusLine = "no addon selected"
		return m, nil
	}
	id, flavor := a.ID, client.Flavor(m.client.Flavor)
	return m, func() tea.Msg {
		tracking, err := service.ToggleTracking(m.ctx, flavor, id)
		return trackMsg{id: id, tracking: tracking, err: err}
	}
}

// doDelete removes the confirmed addon through the service layer.
func (m Model) doDelete() (tea.Model, tea.Cmd) {
	a := m.selected()
	m.confirm = false
	if a == nil {
		return m, nil
	}
	id, flavor := a.ID, client.Flavor(m.client.Flavor)
	m.statusLine = "deleting " + id + "…"
	return m, func() tea.Msg {
		err := service.RemoveAddon(m.ctx, flavor, id)
		return deleteMsg{id: id, err: err}
	}
}

// loadStatus refreshes the status snapshot as a command.
func loadStatus() tea.Cmd {
	return func() tea.Msg {
		st, err := service.LoadStatus()
		return statusMsg{status: st, err: err}
	}
}

// refreshStatus is loadStatus for the user's "r" key: the snapshot is
// re-loaded — the same call the initial load makes — and the reply is
// marked so the status line can acknowledge the refresh.
func refreshStatus() tea.Cmd {
	return func() tea.Msg {
		st, err := service.LoadStatus()
		return statusMsg{status: st, err: err, refreshed: true}
	}
}
