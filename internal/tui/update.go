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

// addDoneMsg delivers the outcome of one add run: what was installed, or
// its error.
type addDoneMsg struct {
	id     string
	flavor client.Flavor
	res    service.AddResult
	err    error
}

// addService runs the service add flow. It is a variable so tests can arm
// the model against a stub without touching the network.
var addService = service.Add

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
		if (m.syncing || m.adding) && m.run != nil {
			// Sample the running sync or add's state on the spinner tick:
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
	case addDoneMsg:
		m.adding = false
		m.cancelSync = nil
		m.wait = ""
		if msg.err != nil {
			m.results[msg.id] = service.Result{AddonID: msg.id, Kind: service.ResultFailed, Err: msg.err}
			m.statusLine = "add failed: " + clean(msg.err.Error())
		} else {
			m.results[msg.id] = service.Result{AddonID: msg.id, Kind: service.ResultUpdated, From: "-", To: msg.res.Version}
			m.statusLine = fmt.Sprintf("added %s %s", msg.id, clean(msg.res.Version))
			m.pendingFlavor = string(msg.flavor)
		}
		m.finishSync() // drops the live rows; the outcomes above land in the rows
		m.layout()
		if msg.err != nil {
			return m, nil
		}
		return m, loadStatus()
	case statusMsg:
		if msg.err != nil {
			m.statusLine = "error: " + msg.err.Error()
			return m, nil
		}
		m.status = msg.status
		m.pickClient()
		if m.pendingFlavor != "" {
			for i := range m.status.Clients {
				if m.status.Clients[i].Flavor == m.pendingFlavor {
					m.client = m.status.Clients[i]
					break
				}
			}
			m.pendingFlavor = ""
		}
		m.addRow = nil // the snapshot now carries the real row
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

// handleKey routes one key press through the mode stack (add input, flavor
// prompt, filter input, delete confirmation, normal mode).
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// ctrl+c works in every mode: it cancels a running sync first — through
	// the installer's rollback — and quits otherwise.
	if msg.String() == "ctrl+c" {
		return m.cancelOrQuit()
	}

	if m.addInput {
		switch msg.String() {
		case "enter":
			if m.addText == "" {
				m.statusLine = "add: enter a reference first"
				return m, nil
			}
			m.validateAdd()
			if m.addErr != nil {
				m.statusLine = "add: " + clean(m.addErr.Error())
				return m, nil
			}
			if len(m.status.Clients) > 1 {
				// The flavor prompt takes over from the input: closing
				// addInput keeps the modes mutually exclusive so the
				// prompt's keys reach the prompt.
				m.addInput = false
				m.addChoose = true
				m.addCursor = m.activeClientIndex()
				m.statusLine = ""
				m.layout()
				return m, nil
			}
			return m.startAdd(m.status.Clients[0])
		case "esc":
			// esc cancels the flow before anything happened.
			m.addInput = false
			m.addText = ""
			m.addParsed = service.AddRef{}
			m.addErr = nil
			m.statusLine = "add cancelled"
		case "backspace":
			if len(m.addText) > 0 {
				m.addText = m.addText[:len(m.addText)-1]
			}
			m.validateAdd()
		default:
			if msg.Text != "" {
				m.addText += msg.Text
				m.validateAdd()
			}
		}
		m.layout()
		return m, nil
	}

	if m.addChoose {
		switch msg.String() {
		case "enter":
			return m.startAdd(m.status.Clients[m.addCursor])
		case "esc":
			// esc cancels the flow with no side effects.
			m.addChoose = false
			m.addInput = false
			m.addText = ""
			m.addParsed = service.AddRef{}
			m.addErr = nil
			m.statusLine = "add cancelled"
		case "up", "k":
			if m.addCursor > 0 {
				m.addCursor--
			}
		case "down", "j":
			if m.addCursor < len(m.status.Clients)-1 {
				m.addCursor++
			}
		default:
			// Number keys pick a flavor and start right away.
			if msg.Text != "" && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
				if i := int(msg.Text[0] - '1'); i < len(m.status.Clients) {
					return m.startAdd(m.status.Clients[i])
				}
			}
		}
		m.layout()
		return m, nil
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
	case m.adding && msg.String() == "esc":
		// esc cancels the running add — the in-flight install rolls back.
		m.statusLine = "cancelling — the in-flight install rolls back"
		if m.cancelSync != nil {
			m.cancelSync()
			m.cancelSync = nil
		}
	case key.Matches(msg, m.keys.Quit):
		return m.cancelOrQuit()
	case key.Matches(msg, m.keys.Detail):
		m.detail = !m.detail
		m.layout()
	case key.Matches(msg, m.keys.Filter):
		m.filtering = true
		m.statusLine = "filter: type to narrow the list, enter applies, esc clears"
	case key.Matches(msg, m.keys.Add):
		switch {
		case m.syncing || m.adding:
			m.statusLine = "wait for the current run to finish"
			return m, nil
		case len(m.status.Clients) == 0:
			m.statusLine = "no client configured — run \"bfctl init\" first"
			return m, nil
		}
		m.addInput = true
		m.addText = ""
		m.addParsed = service.AddRef{}
		m.addErr = nil
		m.statusLine = "add: paste a ref — owner/repo, a wago.io or curseforge.com URL, or a git remote"
	case key.Matches(msg, m.keys.Update):
		switch {
		case m.syncing:
			m.statusLine = "sync already running"
			return m, nil
		case m.adding:
			m.statusLine = "add already running"
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

// cancelOrQuit is the quit key's behavior: a running sync or add is
// cancelled first — its in-flight install rolls back through the installer,
// which the confirmation states — and the TUI quits on the next press.
func (m Model) cancelOrQuit() (tea.Model, tea.Cmd) {
	if m.syncing || m.adding {
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
// the run reports back.
func (m Model) startSync() (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(m.ctx)
	run := newSyncRun()
	m.syncing = true
	m.cancelSync = cancel
	m.run = run
	m.live = liveState{}
	m.wait = ""
	m.statusLine = ""
	m.rt.wg.Add(1)
	go func() {
		defer m.rt.wg.Done()
		defer cancel()
		report, err := service.Sync(ctx, service.SyncOptions{}, run.recordResult, run.record)
		m.rt.send(syncDoneMsg{report: report, err: err})
	}()
	var spinCmd tea.Cmd
	m.spin, spinCmd = m.spin.Update(m.spin.Tick())
	return m, spinCmd
}

// startAdd runs service.Add — the same add flow "bfctl add" runs — for the
// parsed ref in the given client flavor, wired to the live run state
// exactly like startSync: the service layer's ProgressEvent stream writes
// the run state as the work happens (no per-event messages), and the
// spinner tick samples it into the provisional addon row and the status
// bar. The spinner keeps turning until the run reports back.
func (m Model) startAdd(target service.ClientRow) (tea.Model, tea.Cmd) {
	ref := m.addParsed
	flavor := client.Flavor(target.Flavor)
	ctx, cancel := context.WithCancel(m.ctx)
	run := newSyncRun()
	m.addInput = false
	m.addChoose = false
	m.adding = true
	m.cancelSync = cancel
	m.run = run
	m.live = liveState{}
	m.wait = ""
	m.statusLine = ""
	m.addRow = &service.AddonRow{ID: ref.ID(), Name: ref.ID(), Installed: "-", Upstream: "-", Strategy: ""}
	fn := addService
	m.rt.wg.Add(1)
	go func() {
		defer m.rt.wg.Done()
		defer cancel()
		res, err := fn(ctx, ref, flavor, service.AddOptions{Tracking: "stable", OnProgress: run.record})
		m.rt.send(addDoneMsg{id: ref.ID(), flavor: flavor, res: res, err: err})
	}()
	var spinCmd tea.Cmd
	m.spin, spinCmd = m.spin.Update(m.spin.Tick())
	return m, spinCmd
}

// finishSync folds the last of the finished run into the model: the
// decided outcomes land in the results map — where the rows and the
// detail pane keep them after the run — and the live rows are dropped.
func (m *Model) finishSync() {
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
