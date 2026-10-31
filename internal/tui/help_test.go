package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Screen-level acceptance tests for the full help overlay (?): every
// keybinding grouped by the context it works in, holding at 60 and 200
// columns. The routing tests (open/close keys, quit behavior) live in
// update_test.go.

// helpView renders the help overlay at the given size and capabilities.
func helpView(t *testing.T, c caps, w, h int) string {
	t.Helper()
	m := viewTest(t, c, w, h, viewRows())
	m.helpOpen = true
	return m.View().Content
}

func TestHelpOverlayListsEveryKeyByContext(t *testing.T) {
	for _, w := range []int{60, 200} {
		content := ansi.Strip(helpView(t, caps{unicode: true, color: true}, w, 24))
		for _, want := range []string{
			// The context groups.
			"the list", "filter input", "add input", "target flavor", "confirmations", "during a run",
			// The list's keys.
			"↑/k up", "↓/j down", "enter detail", "/ filter",
			"u update all", "a add", "U update one", "c check", "r refresh",
			"s release/branch", "d delete", "? help", "q quit", "ctrl+c cancel & quit",
			// The modal contexts' keys.
			"typing", "backspace", "y/enter", "n/esc", "1-9", "esc", "enter", "q ask to quit",
		} {
			if !strings.Contains(content, want) {
				t.Errorf("width %d: help overlay is missing %q:\n%s", w, want, content)
			}
		}
	}
}

func TestHelpOverlayHoldsAtAcceptanceSizes(t *testing.T) {
	for _, w := range []int{60, 200} {
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
