package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Screen-level acceptance tests for the full help overlay (?): every
// keybinding grouped by the context it works in, the action keys carrying
// their concrete behavior and none of the trivial input narration, and
// the layout holding at the acceptance widths (60 to 200 columns). The
// routing tests (open/close keys, quit behavior) live in update_test.go.

// helpView renders the help overlay at the given size and capabilities.
func helpView(t *testing.T, c caps, w, h int) string {
	t.Helper()
	m := viewTest(t, c, w, h, viewRows())
	m.helpOpen = true
	return m.View().Content
}

func TestHelpOverlayListsEveryKeyByContext(t *testing.T) {
	for _, w := range []int{60, 80, 100, 120, 200} {
		content := ansi.Strip(helpView(t, caps{unicode: true, color: true}, w, 24))
		for _, want := range []string{
			// The context groups.
			"the list", "filter input", "add input", "target flavor", "confirmations", "during a run",
			// The list's keys, with the action keys' concrete behavior.
			"↑/k up", "↓/j down", "enter detail", "/ filter",
			"u update all addons", "a add", "U update the selected addon only",
			"c check for updates without modifying disk (dry run)",
			"r reload status from disk, update the checked stamp",
			"s toggle release/branch tracking",
			"d delete", "? help", "q quit", "ctrl+c cancel & quit",
			// The modal contexts' actionable controls.
			"y/enter", "n/esc", "1-9", "esc", "enter", "q ask to quit",
		} {
			if !strings.Contains(content, want) {
				t.Errorf("width %d: help overlay is missing %q:\n%s", w, want, content)
			}
		}
		// The trivial input narration is gone: nothing narrates typing
		// or backspace any more.
		for _, notWant := range []string{"typing", "backspace", "erase", "narrows the list", "enters a reference"} {
			if strings.Contains(content, notWant) {
				t.Errorf("width %d: help overlay still narrates %q:\n%s", w, notWant, content)
			}
		}
	}
}

func TestHelpOverlayHoldsAtAcceptanceSizes(t *testing.T) {
	for _, w := range []int{60, 80, 100, 120, 200} {
		for _, h := range []int{24, 28} {
			lines := strings.Split(helpView(t, caps{unicode: true, color: true}, w, h), "\n")
			if len(lines) != h {
				t.Fatalf("width %d height %d: overlay has %d lines, want %d", w, h, len(lines), h)
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got > w {
					t.Fatalf("width %d height %d: line %d is %d cells wide:\n%q", w, h, i, got, line)
				}
			}
		}
	}
}

func TestHelpOverlayASCIIModeDegradesAllGlyphs(t *testing.T) {
	content := helpView(t, caps{unicode: false, color: false}, 100, 24)
	for i, line := range strings.Split(content, "\n") {
		for _, r := range line {
			if r > 127 {
				t.Fatalf("ASCII help overlay: line %d contains non-ASCII rune %q:\n%q", i, r, line)
			}
		}
	}
	plain := ansi.Strip(content)
	for _, want := range []string{"up/k up", "down/j down", "q quit", "ctrl+c cancel & quit"} {
		if !strings.Contains(plain, want) {
			t.Errorf("ASCII help overlay is missing %q:\n%s", want, plain)
		}
	}
}
