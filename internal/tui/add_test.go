package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/paths"
	"github.com/nvrmndmnm/blingtronsfavor/internal/service"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
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

// The add input's terminal editing (XD-56): clipboard paste and the
// standard readline navigation and editing shortcuts, all cursor-aware.

// ctrlKey builds a ctrl+letter press.
func ctrlKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl}
}

// altKey builds an alt+letter press. Terminals may deliver the letter as
// the key's text along with the alt modifier, so the fixture does too.
func altKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: tea.ModAlt, Text: string(code)}
}

// addInputModel opens the add input and types s into it.
func addInputModel(t *testing.T, s string) Model {
	t.Helper()
	m := testModel(t)
	m = press(t, m, keyMsg('a'))
	return typeText(t, m, s)
}

func TestAddInputInsertsAtTheCursor(t *testing.T) {
	m := addInputModel(t, "ab")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	m = press(t, m, keyMsg('X'))
	if m.addText != "aXb" {
		t.Fatalf("addText = %q, want %q — typing must land at the cursor", m.addText, "aXb")
	}
	if m.addCur != 2 {
		t.Fatalf("addCur = %d, want 2 (after the inserted rune)", m.addCur)
	}

	// backspace erases before the cursor, delete erases at it.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.addText != "ab" {
		t.Fatalf("addText = %q, want %q", m.addText, "ab")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDelete})
	if m.addText != "a" {
		t.Fatalf("addText = %q, want %q", m.addText, "a")
	}
}

func TestAddInputWordEditing(t *testing.T) {
	// ctrl+w deletes the word before the cursor, whitespace-delimited.
	m := addInputModel(t, "owner/repo other")
	m = press(t, m, ctrlKey('w'))
	if m.addText != "owner/repo " {
		t.Fatalf("ctrl+w: addText = %q, want %q", m.addText, "owner/repo ")
	}
	m = press(t, m, ctrlKey('w'))
	if m.addText != "" {
		t.Fatalf("ctrl+w: addText = %q, want it empty", m.addText)
	}

	// alt+b / alt+f move by word in both directions.
	m = addInputModel(t, "aa bb cc")
	m = press(t, m, altKey('b'))
	if m.addCur != 6 {
		t.Fatalf("alt+b: addCur = %d, want 6 (before the last word)", m.addCur)
	}
	m = press(t, m, altKey('f'))
	if m.addCur != 8 {
		t.Fatalf("alt+f: addCur = %d, want 8 (end of the line)", m.addCur)
	}
	// alt+left / alt+right answer the same motion.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt})
	if m.addCur != 6 {
		t.Fatalf("alt+left: addCur = %d, want 6", m.addCur)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModAlt})
	if m.addCur != 8 {
		t.Fatalf("alt+right: addCur = %d, want 8", m.addCur)
	}

	// ctrl+u clears from the cursor back to the start of the line.
	m = addInputModel(t, "abcdef")
	for i := 0; i < 2; i++ {
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	}
	m = press(t, m, ctrlKey('u'))
	if m.addText != "ef" || m.addCur != 0 {
		t.Fatalf("ctrl+u: addText = %q addCur = %d, want %q and 0", m.addText, m.addCur, "ef")
	}
}

func TestAddInputLineMotion(t *testing.T) {
	// home / ctrl+a jump to the start, end / ctrl+e to the end.
	m := addInputModel(t, "abc")
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyHome}, ctrlKey('a'),
	} {
		m = press(t, m, key)
		if m.addCur != 0 {
			t.Fatalf("%v: addCur = %d, want 0", key, m.addCur)
		}
	}
	m = press(t, m, keyMsg('X'))
	if m.addText != "Xabc" {
		t.Fatalf("typing at the line start: addText = %q, want %q", m.addText, "Xabc")
	}
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyEnd}, ctrlKey('e'),
	} {
		m = press(t, m, key)
		if m.addCur != 4 {
			t.Fatalf("%v: addCur = %d, want 4", key, m.addCur)
		}
	}
	m = press(t, m, keyMsg('Y'))
	if m.addText != "XabcY" {
		t.Fatalf("typing at the line end: addText = %q, want %q", m.addText, "XabcY")
	}

	// The arrows move one rune at a time and stop at the line bounds.
	m = addInputModel(t, "ab")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.addCur != 2 {
		t.Fatalf("right at the end: addCur = %d, want 2", m.addCur)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.addCur != 0 {
		t.Fatalf("left at the start: addCur = %d, want 0", m.addCur)
	}
}

func TestAddInputAcceptsPastedText(t *testing.T) {
	// Bracketed paste (ctrl+shift+v in most terminals) lands at the
	// cursor; a copied trailing newline never reaches the input.
	m := addInputModel(t, "owner/")
	next, _ := m.Update(tea.PasteMsg{Content: "repo\r\n"})
	m = next.(Model)
	if m.addText != "owner/repo" {
		t.Fatalf("paste: addText = %q, want %q", m.addText, "owner/repo")
	}
	if m.addCur != len([]rune(m.addText)) {
		t.Fatalf("paste: addCur = %d, want the end of the pasted text", m.addCur)
	}

	// A paste pasted mid-line inserts at the cursor and re-validates.
	m = addInputModel(t, "owner/repo")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	next, _ = m.Update(tea.PasteMsg{Content: "XX"})
	m = next.(Model)
	if m.addText != "owner/reXXpo" {
		t.Fatalf("mid-line paste: addText = %q, want %q", m.addText, "owner/reXXpo")
	}
	if m.addErr != nil {
		t.Fatalf("addErr = %v, want the re-validated parse (the ref is still valid)", m.addErr)
	}

	// Pasting outside the add input is ignored, never typed into the list.
	m = testModel(t)
	next, _ = m.Update(tea.PasteMsg{Content: "q"})
	m = next.(Model)
	if m.quitting {
		t.Fatal("a paste outside the add input triggered a list key")
	}
}

func TestAddInputEditingKeepsTheValidationHint(t *testing.T) {
	// Every edit re-parses the reference, so the inline hint follows the
	// cursor-aware editing.
	m := addInputModel(t, "wago:eqol")
	if m.addParsed.Kind != state.SourceWago {
		t.Fatalf("addParsed = %+v, want the wago ref", m.addParsed)
	}
	m = press(t, m, ctrlKey('w'))
	if m.addParsed.Kind != "" || m.addErr != nil {
		t.Fatalf("after ctrl+w: addParsed = %+v addErr = %v, want an empty re-parse", m.addParsed, m.addErr)
	}
	m = typeText(t, m, "owner/repo")
	if m.addParsed.Kind != state.SourceGitHub {
		t.Fatalf("addParsed = %+v, want the github ref after retyping", m.addParsed)
	}
}
