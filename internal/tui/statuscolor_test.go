package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestColorizeStatus pins the shared status color language export that
// "bfctl status" colors its cells with: without color the word passes
// through untouched, with color the word survives intact inside the
// styling (color never carries meaning alone, R4).
func TestColorizeStatus(t *testing.T) {
	if got := ColorizeStatus("up to date", false); got != "up to date" {
		t.Errorf("ColorizeStatus(word, false) = %q, want the word unchanged", got)
	}
	for _, word := range []string{"up to date", "updated 1.0 → 1.1", "update 12.1.17", "failed: boom", "Match", "Mismatch", "manual", "-"} {
		got := ColorizeStatus(word, true)
		if ansi.Strip(got) != word {
			t.Errorf("ColorizeStatus(%q, true) strips to %q, want the word intact", word, ansi.Strip(got))
		}
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("ColorizeStatus(%q, true) = %q, want ANSI styling", word, got)
		}
	}
}

// TestColorizeStatusMatchesThemeStyles pins that the export and the theme
// share one color definition: every class renders in the same color the
// TUI table paints its Status cells with.
func TestColorizeStatusMatchesThemeStyles(t *testing.T) {
	th := newTheme(caps{unicode: true, color: true})
	tests := []struct {
		word  string
		style string
	}{
		{"up to date", th.good.Render("up to date")},
		{"update 12.1.17", th.warn.Render("update 12.1.17")},
		{"failed: boom", th.bad.Render("failed: boom")},
		{"manual", th.neutral.Render("manual")},
	}
	for _, tt := range tests {
		if got := ColorizeStatus(tt.word, true); got != tt.style {
			t.Errorf("ColorizeStatus(%q, true) = %q, want the theme style %q", tt.word, got, tt.style)
		}
	}
}
