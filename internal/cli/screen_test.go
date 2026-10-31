package cli

import (
	"strconv"
	"strings"
)

// vtScreen is a tiny terminal emulator for the VT100 subset the live
// block draws with (carriage return, newline, cursor-up, erase-line and
// soft wrapping). It exists so the PTY tests can assert what a user would
// actually see on screen — the redraw geometry — instead of raw escape
// bytes. Line wrapping is modelled, so a row that outgrows the terminal
// width visibly corrupts the screen exactly the way a real terminal
// would, which is the drift the width clamp guards against.
//
// The feed is the post-line-discipline stream a terminal emulator reads
// from the PTY master (ONLCR has already turned "\n" into "\r\n").
// Escape sequences are parsed statefully, so feeds may split them.
// Every rune counts one cell: the live block only draws width-1 runes.
type vtScreen struct {
	w, h  int
	cells [][]rune
	x, y  int

	state  int    // 0 normal, 1 saw ESC, 2 inside a CSI sequence
	params string // CSI parameters seen so far
}

const (
	vtNormal = iota
	vtEsc
	vtCSI
)

// newVTScreen starts a blank w x h screen with the cursor top-left.
func newVTScreen(w, h int) *vtScreen {
	s := &vtScreen{w: w, h: h, cells: make([][]rune, h)}
	for i := range s.cells {
		row := make([]rune, w)
		for j := range row {
			row[j] = ' '
		}
		s.cells[i] = row
	}
	return s
}

// feed consumes display bytes and advances the screen state.
func (s *vtScreen) feed(b []byte) {
	for _, r := range string(b) {
		s.put(r)
	}
}

// put applies one rune of the stream.
func (s *vtScreen) put(r rune) {
	switch s.state {
	case vtEsc:
		if r == '[' {
			s.state = vtCSI
			s.params = ""
			return
		}
		s.state = vtNormal // unknown sequence: drop it
		return
	case vtCSI:
		if r >= '0' && r <= '9' || r == ';' || r == '?' {
			s.params += string(r)
			return
		}
		s.csi(r)
		s.state = vtNormal
		return
	}
	switch {
	case r == 0x1b:
		s.state = vtEsc
	case r == '\r':
		s.x = 0
	case r == '\n':
		s.y++
		s.scroll()
	case r < 0x20 || r == 0x7f:
		// no other control bytes reach the screen
	default:
		if s.x >= s.w { // soft wrap: what a real terminal does to an over-wide row
			s.x = 0
			s.y++
			s.scroll()
		}
		s.cells[s.y][s.x] = r
		s.x++
	}
}

// csi applies one CSI escape: cursor-up (A) and erase-line (K) move and
// clear the screen; everything else (colors, cursor visibility) is
// ignored — it does not change the geometry.
func (s *vtScreen) csi(final rune) {
	switch final {
	case 'A':
		s.y -= s.param(1)
		if s.y < 0 {
			s.y = 0
		}
	case 'K':
		switch s.param(0) {
		case 1: // erase from line start to the cursor
			for i := 0; i <= s.x && i < s.w; i++ {
				s.cells[s.y][i] = ' '
			}
		case 2: // erase the whole line
			for i := range s.cells[s.y] {
				s.cells[s.y][i] = ' '
			}
		default: // erase from the cursor to line end
			for i := s.x; i < s.w; i++ {
				s.cells[s.y][i] = ' '
			}
		}
	}
}

// param parses the first CSI parameter, falling back to def.
func (s *vtScreen) param(def int) int {
	p := strings.TrimPrefix(s.params, "?")
	if p == "" {
		return def
	}
	first, _, _ := strings.Cut(p, ";")
	n, err := strconv.Atoi(first)
	if err != nil {
		return def
	}
	return n
}

// scroll keeps the cursor inside the screen, scrolling up when a newline
// runs off the bottom.
func (s *vtScreen) scroll() {
	for s.y >= s.h {
		copy(s.cells, s.cells[1:])
		row := make([]rune, s.w)
		for j := range row {
			row[j] = ' '
		}
		s.cells[s.h-1] = row
		s.y--
	}
}

// lines renders the screen as exactly h rows of text (trailing spaces
// trimmed per row).
func (s *vtScreen) lines() []string {
	out := make([]string, s.h)
	for i, row := range s.cells {
		out[i] = strings.TrimRight(string(row), " ")
	}
	return out
}

// visible renders the screen's occupied rows: lines() with the trailing
// blank rows dropped. This is what the assertions compare against — the
// block the user sees, one entry per screen line.
func (s *vtScreen) visible() []string {
	lines := s.lines()
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
