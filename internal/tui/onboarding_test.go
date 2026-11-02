package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/paths"
	"github.com/nvrmndmnm/blingtronsfavor/internal/service"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
)

// Guided-init and empty-state tests (A6): the detected-install picker and
// its manual path prompt, the zero-addons hint panel, and the tip line's
// state tracking. The service entry points are stubbed so the tests never
// touch the network or the real filesystem.

// stubDetectInstalls arms the install scan (like stubAdd arms the add
// flow), so the onboarding fixtures are hermetic.
func stubDetectInstalls(t *testing.T, installs []client.ClientInstall) {
	t.Helper()
	old := detectInstalls
	detectInstalls = func() []client.ClientInstall { return installs }
	t.Cleanup(func() { detectInstalls = old })
}

// stubDetectInstall arms the single-path detection behind the manual path
// prompt's live validation.
func stubDetectInstall(t *testing.T, fn func(string) (*client.ClientInstall, error)) {
	t.Helper()
	old := detectInstall
	detectInstall = fn
	t.Cleanup(func() { detectInstall = old })
}

// stubInitClient arms the init step behind the picker and prompt (R5: the
// step itself is service.Init in production).
func stubInitClient(t *testing.T, fn func(string) (service.InitResult, error)) {
	t.Helper()
	old := initClient
	initClient = fn
	t.Cleanup(func() { initClient = old })
}

// testModelNoClients builds the model with no configured client — the
// guided-init screen — offering the given detected installs in the picker.
// The scan is stubbed before NewModel runs, so the fixture is deterministic.
func testModelNoClients(t *testing.T, opts Options, installs ...client.ClientInstall) Model {
	t.Helper()
	stubDetectInstalls(t, installs)
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	if err := paths.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	path, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Open(path).Save(state.New()); err != nil {
		t.Fatal(err)
	}
	return NewModel(context.Background(), opts, &runtime{})
}

// testInstalls builds two detected installs for the picker fixtures.
func testInstalls() []client.ClientInstall {
	return []client.ClientInstall{
		{Flavor: client.FlavorForever, FlavorDir: "/games/World of Warcraft/_forever_",
			Version: client.Version{Major: 1, Minor: 60, Patch: 1, Build: 58900}, Interface: 16001},
		{Flavor: client.FlavorMainline, FlavorDir: "/games/World of Warcraft/_retail_",
			Version: client.Version{Major: 12, Minor: 1, Patch: 0, Build: 59201}, Interface: 120100},
	}
}

func TestOnboardingPickerOffersDetectedInstalls(t *testing.T) {
	m := testModelNoClients(t, Options{}, testInstalls()...)
	if m.pathInput {
		t.Fatal("with detected installs the picker should show, not the path prompt")
	}
	content := m.View().Content
	for _, want := range []string{
		"Forever 1.60.1 (Interface 16001)",
		"Retail 12.1.0 (Interface 120100)",
		"/games/World of Warcraft/_forever_",
		"type a flavor directory path manually",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("picker frame is missing %q:\n%s", want, content)
		}
	}
	if !strings.Contains(content, "enter set up") {
		t.Fatalf("picker frame is missing the set-up tip:\n%s", content)
	}
}

func TestOnboardingEnterInitsPickedInstall(t *testing.T) {
	var got string
	stubInitClient(t, func(flavorDir string) (service.InitResult, error) {
		got = flavorDir
		return service.InitResult{
			Install:   client.ClientInstall{Flavor: client.FlavorForever, FlavorDir: flavorDir},
			StatePath: "state.json",
		}, nil
	})
	m := testModelNoClients(t, Options{}, testInstalls()...)

	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if got != "/games/World of Warcraft/_forever_" {
		t.Fatalf("init ran on %q, want the picked install", got)
	}
	if !strings.Contains(m.statusLine, "initialised") {
		t.Fatalf("statusLine = %q, want an initialised message", m.statusLine)
	}
	if cmd == nil {
		t.Fatal("the init did not schedule the status reload")
	}
}

func TestOnboardingInitFailureStaysOnScreen(t *testing.T) {
	stubInitClient(t, func(string) (service.InitResult, error) {
		return service.InitResult{}, errors.New("disk on fire")
	})
	m := testModelNoClients(t, Options{}, testInstalls()...)

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if !strings.Contains(m.statusLine, "init failed") {
		t.Fatalf("statusLine = %q, want the init failure", m.statusLine)
	}
	if len(m.status.Clients) != 0 {
		t.Fatal("a failed init must leave the guided-init screen up")
	}
}

func TestOnboardingManualPathEntryInits(t *testing.T) {
	var got string
	stubInitClient(t, func(flavorDir string) (service.InitResult, error) {
		got = flavorDir
		return service.InitResult{Install: client.ClientInstall{Flavor: client.FlavorForever, FlavorDir: flavorDir}}, nil
	})
	stubDetectInstall(t, func(path string) (*client.ClientInstall, error) {
		return &client.ClientInstall{Flavor: client.FlavorForever, FlavorDir: path}, nil
	})
	m := testModelNoClients(t, Options{}, testInstalls()...)

	// Down to the trailing manual entry — one j per detected install
	// (the cursor clamps on the entry) — enter opens the prompt.
	for range m.picker {
		m = press(t, m, keyMsg('j'))
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.pathInput {
		t.Fatal("the manual entry did not open the path prompt")
	}
	for _, r := range "/games/_forever_" {
		m = press(t, m, keyMsg(r))
	}
	if m.pathText != "/games/_forever_" {
		t.Fatalf("pathText = %q, want the typed path", m.pathText)
	}
	if m.pathInstall == nil {
		t.Fatal("the typed path did not validate live")
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got != "/games/_forever_" {
		t.Fatalf("init ran on %q, want the typed path", got)
	}

	// esc from the prompt returns to the picker when there is one.
	m = testModelNoClients(t, Options{}, testInstalls()...)
	for range m.picker {
		m = press(t, m, keyMsg('j'))
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, escKey())
	if m.pathInput {
		t.Fatal("esc did not return to the picker")
	}
}

// TestOnboardingHelpShowsOnlyActionableKeys pins the guided-init screen
// after the XD-58 help cleanup: the help and tip lines carry the
// actionable controls (enter, esc) and none of the trivial "backspace
// erase" narration.
func TestOnboardingHelpShowsOnlyActionableKeys(t *testing.T) {
	m := testModelNoClients(t, Options{}, testInstalls()...)
	for range m.picker {
		m = press(t, m, keyMsg('j'))
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"enter set up", "esc back"} {
		if !strings.Contains(content, want) {
			t.Fatalf("path prompt help is missing %q:\n%s", want, content)
		}
	}
	for _, notWant := range []string{"backspace", "erase"} {
		if strings.Contains(content, notWant) {
			t.Fatalf("path prompt help still narrates %q:\n%s", notWant, content)
		}
	}
}

func TestOnboardingPathPromptValidatesLive(t *testing.T) {
	inits := 0
	stubInitClient(t, func(string) (service.InitResult, error) {
		inits++
		return service.InitResult{}, nil
	})
	stubDetectInstall(t, func(string) (*client.ClientInstall, error) {
		return nil, errors.New(".build.info not found")
	})
	// Nothing detected: the prompt is the screen from the start.
	m := testModelNoClients(t, Options{})
	if !m.pathInput {
		t.Fatal("without detection the inline path prompt should show at once")
	}
	for _, r := range "/not/wow" {
		m = press(t, m, keyMsg(r))
	}
	if m.pathErr == nil {
		t.Fatal("the typed path did not report its detection error")
	}
	content := m.View().Content
	if !strings.Contains(content, ".build.info not found") {
		t.Fatalf("the prompt is missing the detection error:\n%s", content)
	}

	// Enter on an invalid path keeps the prompt up and inits nothing.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if inits != 0 {
		t.Fatal("enter ran the init on a path that does not detect")
	}
	if !strings.Contains(m.statusLine, "path:") {
		t.Fatalf("statusLine = %q, want the path prompt message", m.statusLine)
	}

	// esc clears the prompt when there is no picker to return to.
	m = press(t, m, escKey())
	if !m.pathInput || m.pathText != "" {
		t.Fatalf("esc did not clear the prompt (pathInput=%v pathText=%q)", m.pathInput, m.pathText)
	}
}

func TestOnboardingQuitQuitsAndIgnoresListKeys(t *testing.T) {
	m := testModelNoClients(t, Options{}, testInstalls()...)
	next, _ := m.Update(keyMsg('q'))
	if !next.(Model).quitting {
		t.Fatal("q did not quit the guided-init screen")
	}
	// List keys do nothing on the guided-init screen.
	for _, code := range []rune{'u', 'a', '/', 'o'} {
		next, _ := m.Update(keyMsg(code))
		if got := next.(Model); got.syncing || got.adding || got.filtering || got.addInput {
			t.Fatalf("pressing %q on the guided-init screen triggered a list action", code)
		}
	}
}

func TestZeroAddonHintPanelReplacesTheGrid(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 28
	m.client.Addons = nil
	m.refreshRows()
	content := m.View().Content
	for _, want := range []string{"no addons yet", "press a to add", "press u to sync"} {
		if !strings.Contains(content, want) {
			t.Fatalf("the zero-addon hint panel is missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "Upstream") {
		t.Fatalf("the empty grid should be replaced by the hint panel:\n%s", content)
	}
}

func TestTipLineTracksCurrentState(t *testing.T) {
	cases := []struct {
		name    string
		build   func(m *Model)
		want    []string
		notWant []string
	}{
		{
			name:    "nothing selected",
			want:    []string{"↑↓ select", "enter details", "u sync"},
			notWant: []string{"U update one", "q quit"},
		},
		{
			name:    "row selected",
			build:   func(m *Model) { m.detailFocus = true },
			want:    []string{"U update one", "s tracking", "d delete"},
			notWant: []string{"enter details"},
		},
		{
			name:    "mid-sync",
			build:   func(m *Model) { *m = beginSync(*m) },
			want:    []string{"ctrl+c cancel", "q quit"},
			notWant: []string{"enter details", "U update one"},
		},
		{
			name:    "mid-add",
			build:   func(m *Model) { m.adding = true },
			want:    []string{"esc cancel", "q quit"},
			notWant: []string{"enter details"},
		},
		{
			name:    "zero addons",
			build:   func(m *Model) { m.client.Addons = nil; m.refreshRows() },
			want:    []string{"a add", "u sync", "? help"},
			notWant: []string{"enter details"},
		},
		{
			name:    "filter with no matches",
			build:   func(m *Model) { m.filter = "zzz"; m.refreshRows() },
			want:    []string{"esc clear"},
			notWant: []string{"enter details"},
		},
		{
			name:    "add input",
			build:   func(m *Model) { m.addInput = true },
			want:    []string{"enter continue", "esc cancel"},
			notWant: []string{"u sync", "backspace erase"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(t)
			if tc.build != nil {
				tc.build(&m)
			}
			tip := m.tipLine()
			for _, want := range tc.want {
				if !strings.Contains(tip, want) {
					t.Errorf("tip %q is missing %q", tip, want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(tip, notWant) {
					t.Errorf("tip %q carries stale advice %q", tip, notWant)
				}
			}
		})
	}
}

func TestTipLineIsOnTheStatusBar(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 28
	lines := strings.Split(m.View().Content, "\n")
	status := lines[len(lines)-2]
	for _, want := range []string{"↑↓ select", "enter details", "u sync"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status bar is missing the tip %q:\n%q", want, status)
		}
	}
}

func TestOnboardingHoldsAtAcceptanceSizes(t *testing.T) {
	for _, w := range []int{60, 80, 100, 120, 200} {
		models := map[string]Model{}
		models["picker"] = testModelNoClients(t, Options{}, testInstalls()...)
		path := testModelNoClients(t, Options{}, testInstalls()...)
		path.pickerCursor = len(path.picker)
		path.pathInput = true
		path.pathText = "~/Games/World of Warcraft/_forever_"
		models["path"] = path
		for name, m := range models {
			m.width, m.height = w, 28
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != 28 {
				t.Fatalf("width %d %s: frame has %d lines, want 28", w, name, len(lines))
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got > w {
					t.Fatalf("width %d %s: line %d is %d cells wide:\n%q", w, name, i, got, line)
				}
			}
		}
	}
}
