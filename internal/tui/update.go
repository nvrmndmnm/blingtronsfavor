package tui

import (
	"context"
	"fmt"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbletea/v2"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/service"
)

// syncResultMsg delivers one per-addon sync outcome while the sync runs.
type syncResultMsg struct{ result service.Result }

// syncDoneMsg delivers the final sync report (or its error).
type syncDoneMsg struct {
	report service.Report
	err    error
}

// statusMsg carries a refreshed status snapshot.
type statusMsg struct {
	status service.Status
	err    error
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
func (m Model) Init() (tea.Model, tea.Cmd) {
	return m, nil
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
		return m, cmd
	case syncResultMsg:
		m.results[msg.result.AddonID] = msg.result
		m.refreshRows()
		return m, nil
	case syncDoneMsg:
		m.syncing = false
		m.cancelSync = nil
		if msg.err != nil {
			m.statusLine = "sync failed: " + msg.err.Error()
		} else {
			m.statusLine = msg.report.Summary()
		}
		return m, loadStatus()
	case statusMsg:
		if msg.err != nil {
			m.statusLine = "error: " + msg.err.Error()
			return m, nil
		}
		m.status = msg.status
		m.pickClient()
		m.refreshRows()
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
	case key.Matches(msg, m.keys.Filter):
		m.filtering = true
		m.statusLine = "filter: type to narrow the list, enter applies, esc clears"
	case key.Matches(msg, m.keys.Update):
		if m.syncing {
			m.statusLine = "sync already running"
			return m, nil
		}
		return m.startSync()
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
// first — its in-flight install rolls back through the installer — and the
// TUI quits on the next press.
func (m Model) cancelOrQuit() (tea.Model, tea.Cmd) {
	if m.syncing {
		m.statusLine = "sync cancelled — rolling back (press q again to quit)"
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
// sync" makes — and keeps the spinner turning until it reports back.
func (m Model) startSync() (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(m.ctx)
	m.syncing = true
	m.cancelSync = cancel
	m.statusLine = "syncing…"
	m.rt.wg.Add(1)
	go func() {
		defer m.rt.wg.Done()
		defer cancel()
		report, err := service.Sync(ctx, service.SyncOptions{}, func(r service.Result) {
			m.rt.send(syncResultMsg{result: r})
		})
		m.rt.send(syncDoneMsg{report: report, err: err})
	}()
	var spinCmd tea.Cmd
	m.spin, spinCmd = m.spin.Update(m.spin.Tick())
	return m, spinCmd
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
