package tui

import (
	"image/color"
	"os"
	goruntime "runtime"
	"strings"

	"charm.land/lipgloss/v2"
)

// caps describes the terminal capabilities the TUI renders for. Every
// capability fallback is part of the acceptance surface (R4): nothing the
// screen says may depend on a glyph or a color being renderable.
type caps struct {
	unicode bool // terminal locale is UTF-8: unicode glyphs allowed
	color   bool // colors allowed (NO_COLOR-aware)
}

// detectCaps derives the capabilities from the environment and the --ascii
// override: the override always wins, otherwise the locale decides the
// glyphs and NO_COLOR decides the colors.
func detectCaps(ascii bool) caps {
	return caps{
		unicode: !ascii && localeUTF8(),
		color:   os.Getenv("NO_COLOR") == "",
	}
}

// localeUTF8 reports whether the locale environment explicitly asks for
// UTF-8. LC_ALL, LC_CTYPE and LANG are consulted in that order (POSIX
// precedence); the first one set decides. With no locale set only Windows
// defaults to unicode (Go writes UTF-8 to its console); everywhere else the
// safe fallback is ASCII.
func localeUTF8() bool {
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if val := os.Getenv(name); val != "" {
			return strings.Contains(strings.ToLower(val), "utf")
		}
	}
	return goruntime.GOOS == "windows"
}

// glyphs is the terminal-glyph vocabulary of the TUI. The status glyphs
// degrade exactly as the UX contract fixes: "✓ ⚠ ✗ …" become "+ ! x *" on
// non-UTF-8 terminals; punctuation (arrow, ellipsis, dash, separators and
// the input cursor) degrades to plain ASCII as well, so an ASCII frame
// never contains a non-ASCII rune.
type glyphs struct {
	Check    string // status: up to date / updated / match
	Warn     string // status: update available / soft problems
	Cross    string // status: failed / missing / mismatch
	Pending  string // status: skipped / unknown / none
	Arrow    string // version transitions ("updated 1.0 → 1.1")
	Ellipsis string // text truncation marker
	Dash     string // prose dashes
	MidDot   string // separator ("·" / "|")
	Cursor   string // filter input cursor
	Sort     string // active sort column marker
	Up       string // help label for the up key
	Down     string // help label for the down key
	UpDown   string // paired arrows for tip lines ("↑↓" / "up/down")
}

var unicodeGlyphs = glyphs{
	Check:    "✓",
	Warn:     "⚠",
	Cross:    "✗",
	Pending:  "…",
	Arrow:    "→",
	Ellipsis: "…",
	Dash:     "—",
	MidDot:   "·",
	Cursor:   "▌",
	Sort:     "▲",
	Up:       "↑",
	Down:     "↓",
	UpDown:   "↑↓",
}

// asciiGlyphs is the fallback set: the status glyphs are exactly the ASCII
// set the UX contract fixes ("+ ! x *").
var asciiGlyphs = glyphs{
	Check:    "+",
	Warn:     "!",
	Cross:    "x",
	Pending:  "*",
	Arrow:    "->",
	Ellipsis: "...",
	Dash:     "-",
	MidDot:   "|",
	Cursor:   "_",
	Sort:     "^",
	Up:       "up",
	Down:     "down",
	UpDown:   "up/down",
}

// statusClass is the semantic class of a status word. Colors and glyphs are
// keyed off the class; the words themselves always stay (color never
// carries meaning alone).
type statusClass int

const (
	statusNeutral statusClass = iota // skipped, "-", unknown
	statusGood                       // up to date, updated, installed, match
	statusWarn                       // update available, soft problems
	statusBad                        // failed, missing, mismatch
)

// classifyStatus maps a status word to its semantic class. The words come
// either from the last sync result ("up to date", "updated 1.0 → 1.1",
// "update available", "available to install", "skipped", "failed" — the
// shared result vocabulary the UX contract fixes) or from the TOC verdict
// ("Match", "MultiMatch", "Mismatch", "NoToc", "Unknown", "Missing", "-").
func classifyStatus(word string) statusClass {
	switch {
	case word == "Match",
		strings.HasPrefix(word, "up to date"),
		strings.HasPrefix(word, "updated "),
		strings.HasPrefix(word, "installed "),
		strings.HasPrefix(word, "done"):
		return statusGood
	case word == "MultiMatch",
		word == "NoToc",
		strings.HasPrefix(word, "update "),
		strings.HasSuffix(word, "available to install"):
		return statusWarn
	case word == "Mismatch",
		word == "Missing",
		strings.HasPrefix(word, "failed"):
		return statusBad
	default:
		return statusNeutral
	}
}

// classColor returns the shared status color for a semantic class: ANSI
// base colors so every profile downgrade keeps the meaning. It is the one
// definition both the TUI theme and "bfctl status" decorate status words
// with, so the surfaces can never drift apart.
func classColor(c statusClass, colorOn bool) color.Color {
	if !colorOn {
		return lipgloss.NoColor{}
	}
	switch c {
	case statusGood:
		return lipgloss.ANSIColor(2) // green
	case statusWarn:
		return lipgloss.ANSIColor(3) // yellow
	case statusBad:
		return lipgloss.ANSIColor(1) // red
	}
	return lipgloss.NoColor{}
}

// ColorizeStatus decorates one status word with the shared status color
// language: the same classification (classifyStatus) and the same colors
// (classColor) the TUI table colors its Status cells with, exported so
// "bfctl status" shows its status cells the same way on a terminal. With
// color disabled the word is returned untouched — the words always carry
// the meaning, color only reinforces it (R4).
func ColorizeStatus(word string, colorOn bool) string {
	if !colorOn {
		return word
	}
	class := classifyStatus(word)
	if class == statusNeutral {
		return lipgloss.NewStyle().Faint(true).Render(word)
	}
	return lipgloss.NewStyle().Foreground(classColor(class, true)).Render(word)
}

// theme is the resolved visual language: the glyph set plus the semantic
// styles. It is plain data, chosen once at startup from the terminal
// capabilities, so rendering stays a pure function of state + theme + size.
type theme struct {
	g glyphs

	accent  lipgloss.Style // selection and header badge
	good    lipgloss.Style // up-to-date class
	warn    lipgloss.Style // update-available class
	bad     lipgloss.Style // failed / missing class
	neutral lipgloss.Style // skipped / unknown / "-"
	dim     lipgloss.Style // secondary information (root, labels)
	title   lipgloss.Style // bold headings

	// box is the expanded detail pane's border.
	box lipgloss.Border
}

// asciiBorder is the plain-ASCII box: the fallback for the detail pane
// on non-UTF-8 terminals.
var asciiBorder = lipgloss.Border{
	Top: "-", Bottom: "-", Left: "|", Right: "|",
	TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
	MiddleLeft: "+", MiddleRight: "+", Middle: "+", MiddleTop: "+", MiddleBottom: "+",
}

// newTheme resolves the visual language for the given capabilities. Colors
// are ANSI base colors so every profile downgrade keeps the meaning; with
// color disabled the styles carry only typography, and the words carry the
// information as they always do.
func newTheme(c caps) theme {
	th := theme{
		g:       asciiGlyphs,
		box:     asciiBorder,
		dim:     lipgloss.NewStyle().Faint(true),
		title:   lipgloss.NewStyle().Bold(true),
		neutral: lipgloss.NewStyle().Faint(true),
	}
	if c.unicode {
		th.g = unicodeGlyphs
		th.box = lipgloss.RoundedBorder()
	}
	accent := color.Color(lipgloss.NoColor{})
	if c.color {
		accent = lipgloss.ANSIColor(6) // cyan
	}
	th.accent = th.title.Foreground(accent).Bold(true)
	th.good = lipgloss.NewStyle().Foreground(classColor(statusGood, c.color))
	th.warn = lipgloss.NewStyle().Foreground(classColor(statusWarn, c.color))
	th.bad = lipgloss.NewStyle().Foreground(classColor(statusBad, c.color))
	return th
}

// statusGlyph returns the class's status glyph.
func (th theme) statusGlyph(c statusClass) string {
	switch c {
	case statusGood:
		return th.g.Check
	case statusWarn:
		return th.g.Warn
	case statusBad:
		return th.g.Cross
	default:
		return th.g.Pending
	}
}

// statusStyle returns the class's color style. Colors only ever decorate a
// word that says the same thing.
func (th theme) statusStyle(c statusClass) lipgloss.Style {
	switch c {
	case statusGood:
		return th.good
	case statusWarn:
		return th.warn
	case statusBad:
		return th.bad
	default:
		return th.neutral
	}
}
