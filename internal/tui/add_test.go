package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbletea/v2"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
)

// testModelTwoClients is testModel with two configured clients (classic and
// forever), for the target-flavor prompt.
func testModelTwoClients(t *testing.T) Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	if err := paths.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	path, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	st := state.New()
	for _, flavor := range []client.Flavor{client.FlavorClassic, client.FlavorForever} {
		st.Clients[flavor] = &state.ClientState{
			Flavor: flavor,
			Root:   filepath.Join(t.TempDir(), "_client_"),
			Addons: map[string]*state.AddonState{},
		}
	}
	if err := state.Open(path).Save(st); err != nil {
		t.Fatal(err)
	}
	return NewModel(context.Background(), Options{}, &runtime{})
}

// stubAdd arms the add flow against a stub service.Add that resolves
// immediately and never touches the network.
func stubAdd(t *testing.T) {
	t.Helper()
	old := addService
	addService = func(context.Context, service.AddRef, client.Flavor, service.AddOptions) (service.AddResult, error) {
		return service.AddResult{ID: "new", Version: "1.2.3", Strategy: state.StrategyRelease}, nil
	}
	t.Cleanup(func() { addService = old })
}

// typeText sends one string as printable key presses.
func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m = press(t, m, keyMsg(r))
	}
	return m
}

func TestAddInputValidatesLive(t *testing.T) {
	m := testModel(t)
	m = press(t, m, keyMsg('a'))
	if !m.addInput {
		t.Fatal("pressing a did not open the add input")
	}
	m = typeText(t, m, "wago:eqol")
	if m.addErr != nil {
		t.Fatalf("addErr = %v, want nil for a valid wago ref", m.addErr)
	}
	if m.addParsed.Kind != state.SourceWago || m.addParsed.Key != "eqol" {
		t.Fatalf("addParsed = %+v, want the wago eqol ref", m.addParsed)
	}
	if !strings.Contains(m.View().Content, "wago eqol") {
		t.Fatalf("view is missing the parsed-ref hint:\n%s", m.View().Content)
	}
	// esc cancels the flow with no side effects.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.addInput || m.addText != "" {
		t.Fatalf("esc did not cancel the add input (addInput=%v addText=%q)", m.addInput, m.addText)
	}
}

func TestAddInputRejectsBadRefInline(t *testing.T) {
	m := testModel(t)
	m = press(t, m, keyMsg('a'))
	m = typeText(t, m, "nope")
	if m.addErr == nil {
		t.Fatal("an invalid ref parsed without an error")
	}
	if !strings.Contains(m.View().Content, "owner/repo") {
		t.Fatalf("view is missing the actionable error:\n%s", m.View().Content)
	}
	// Enter keeps the user in the input — never ejecting to the shell.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.addInput {
		t.Fatal("enter on an invalid ref closed the add input")
	}
	if !strings.Contains(m.statusLine, "add:") {
		t.Fatalf("statusLine = %q, want the parse error", m.statusLine)
	}
}

func TestAddFlavorPromptDefaultsToActive(t *testing.T) {
	stubAdd(t)
	m := testModelTwoClients(t)
	m = press(t, m, keyMsg('a'))
	m = typeText(t, m, "owner/repo")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.addChoose {
		t.Fatal("enter with several clients did not ask for the target flavor")
	}
	if m.addCursor != m.activeClientIndex() {
		t.Fatalf("flavor cursor = %d, want the active client %d", m.addCursor, m.activeClientIndex())
	}
	if !strings.Contains(m.View().Content, "add to flavor") {
		t.Fatalf("view is missing the flavor prompt:\n%s", m.View().Content)
	}
	// The cursor moves but enter still accepts the default first.
	m = press(t, m, keyMsg('j'))
	if m.addCursor == m.activeClientIndex() {
		t.Fatal("down did not move the flavor cursor")
	}
	// esc cancels the whole flow with no side effects.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.addChoose || m.addInput {
		t.Fatal("esc did not cancel the flavor prompt")
	}
	// A number key picks a flavor and starts right away.
	m = press(t, m, keyMsg('a'))
	m = typeText(t, m, "owner/repo")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, keyMsg('2'))
	if !m.adding || m.addChoose {
		t.Fatal("number key did not pick a flavor and start the add")
	}
}

func TestAddRunShowsLiveRowThenOutcome(t *testing.T) {
	stubAdd(t)
	m := testModel(t)
	m = press(t, m, keyMsg('a'))
	m = typeText(t, m, "owner/new")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.adding {
		t.Fatal("enter did not start the add")
	}
	if m.addRow == nil || m.addRow.ID != "new" {
		t.Fatalf("addRow = %+v, want the provisional row for new", m.addRow)
	}
	// The provisional row is in the table with the live-row treatment.
	m.run.record(service.ProgressEvent{Unit: unit("new"), Phase: service.PhaseDownloading, Detail: service.Detail{Version: "1.2.3"}, Index: 1, Total: 1})
	m = tick(t, m)
	if got := rowText(t, m, "new"); !strings.Contains(got, "downloading") {
		t.Fatalf("provisional row status = %q, want the live downloading phase", got)
	}

	// The run's outcome flips the row in place, in the CLI live block's words.
	next, _ := m.Update(addDoneMsg{id: "new", flavor: client.FlavorForever, res: service.AddResult{ID: "new", Version: "1.2.3"}})
	m = next.(Model)
	if m.adding {
		t.Fatal("addDoneMsg left the add running")
	}
	if got := rowText(t, m, "new"); got != "installed 1.2.3" {
		t.Fatalf("row status = %q, want %q", got, "installed 1.2.3")
	}
	if !strings.Contains(m.statusLine, "added new 1.2.3") {
		t.Fatalf("statusLine = %q, want the added confirmation", m.statusLine)
	}
}

func TestEscCancelsRunningAdd(t *testing.T) {
	m := testModel(t)
	cancelled := false
	m.adding = true
	m.cancelSync = func() { cancelled = true }
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if !cancelled {
		t.Fatal("esc did not cancel the running add")
	}
	if !strings.Contains(m.statusLine, "rolls back") {
		t.Fatalf("statusLine = %q, want the rollback explanation", m.statusLine)
	}
}
