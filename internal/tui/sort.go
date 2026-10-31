package tui

import (
	"sort"
	"strings"
	"unicode"

	"github.com/itp5b5/blessforge/internal/service"
)

// sortMode is the active row ordering. "o" cycles name → status →
// upstream; the mode lives in the model, so the order survives every
// refresh of the session (refreshRows re-applies it to each snapshot).
type sortMode int

const (
	sortByName sortMode = iota
	sortByStatus
	sortByUpstream
)

// label names the mode the way the status line reports it.
func (s sortMode) label() string {
	switch s {
	case sortByStatus:
		return "status"
	case sortByUpstream:
		return "upstream"
	default:
		return "name"
	}
}

// next cycles name → status → upstream → name.
func (s sortMode) next() sortMode {
	return (s + 1) % 3
}

// columnTitle is the table column the mode orders by — that column's
// header carries the sort mark, so the active sort is always on screen.
func (s sortMode) columnTitle() string {
	switch s {
	case sortByStatus:
		return "Status"
	case sortByUpstream:
		return "Upstream"
	default:
		return "Addon"
	}
}

// sortSpecs copies the column specs with the active sort column's title
// carrying the sort mark. The mark is prefixed so it survives the header
// truncation on narrow terminals, and the marked title feeds the width
// planning so wide terminals never clip it either.
func (m Model) sortSpecs() []colSpec {
	specs := append([]colSpec(nil), columnSpecs...)
	for i := range specs {
		if specs[i].title == m.sort.columnTitle() {
			specs[i].title = m.theme.g.Sort + " " + specs[i].title
		}
	}
	return specs
}

// sortRows stably orders the visible rows by the active mode: name is
// A→Z, status brings the rows needing attention first (failed/missing,
// then updates, then unknown, then up to date) and upstream is
// version-aware (1.9 before 1.10) with unversioned rows last. Ties fall
// back to name then id, so the order is a deterministic function of the
// rows and survives every refresh unchanged.
func (m Model) sortRows(rows []service.AddonRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch m.sort {
		case sortByStatus:
			wa, wb := m.statusText(a), m.statusText(b)
			ra, rb := statusRank(classifyStatus(wa)), statusRank(classifyStatus(wb))
			if ra != rb {
				return ra < rb
			}
			if wa, wb = strings.ToLower(wa), strings.ToLower(wb); wa != wb {
				return wa < wb
			}
		case sortByUpstream:
			if c := cmpUpstream(a.Upstream, b.Upstream); c != 0 {
				return c < 0
			}
		}
		na, nb := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if na != nb {
			return na < nb
		}
		return a.ID < b.ID
	})
}

// statusRank orders the semantic classes for the status sort: the rows
// needing attention first (failed/missing, then the available updates),
// then the unknowns, then the up-to-date ones.
func statusRank(c statusClass) int {
	switch c {
	case statusBad:
		return 0
	case statusWarn:
		return 1
	case statusNeutral:
		return 2
	default:
		return 3
	}
}

// cmpUpstream orders two upstream version strings for the upstream sort:
// absent versions ("-" or empty) sort last, the rest naturally.
func cmpUpstream(a, b string) int {
	da, db := versionAbsent(a), versionAbsent(b)
	if da != db {
		if da {
			return 1
		}
		return -1
	}
	return cmpNatural(a, b)
}

// versionAbsent reports whether an upstream cell carries no version.
func versionAbsent(s string) bool {
	return s == "" || s == "-"
}

// cmpNatural compares two strings run by run: digit runs compare
// numerically (1.9 before 1.10, no overflow on long runs), other runs
// case-insensitively. A string that runs out first sorts first.
func cmpNatural(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	for len(ra) > 0 && len(rb) > 0 {
		if isDigit(ra[0]) && isDigit(rb[0]) {
			da, restA := digitRun(ra)
			db, restB := digitRun(rb)
			if c := cmpDigits(da, db); c != 0 {
				return c
			}
			ra, rb = restA, restB
			continue
		}
		ca, cb := unicode.ToLower(ra[0]), unicode.ToLower(rb[0])
		if ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
		ra, rb = ra[1:], rb[1:]
	}
	switch {
	case len(ra) > 0:
		return 1
	case len(rb) > 0:
		return -1
	}
	return 0
}

// digitRun splits off the leading digit run of s.
func digitRun(s []rune) (digits, rest []rune) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

// cmpDigits compares two digit runs numerically without parsing them:
// leading zeros drop away, a longer run is the bigger number, and equal
// lengths compare lexicographically.
func cmpDigits(a, b []rune) int {
	for len(a) > 0 && a[0] == '0' {
		a = a[1:]
	}
	for len(b) > 0 && b[0] == '0' {
		b = b[1:]
	}
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// isDigit reports whether r is an ASCII digit.
func isDigit(r rune) bool {
	return r >= '0' && r <= '9'
}
