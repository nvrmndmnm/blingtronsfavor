package tui

import (
	"strings"
	"testing"

	"github.com/itp5b5/blessforge/internal/service"
)

// sortFixture builds four addons whose names, statuses and upstream
// versions all order differently, so each sort mode is observable. The
// sync outcomes (see sortModel) give the rows their status classes.
func sortFixture() []service.AddonRow {
	return []service.AddonRow{
		{ID: "b", Name: "Bravo", Upstream: "1.10", Status: "-"},
		{ID: "a", Name: "alpha", Upstream: "1.9", Status: "-"},
		{ID: "c", Name: "Charlie", Upstream: "-", Status: "-"},
		{ID: "d", Name: "delta", Upstream: "2.0", Status: "-"},
	}
}

// sortModel builds a model over the fixture rows with one sync outcome
// per status class and refreshes through the real rebuild path.
func sortModel(t *testing.T) Model {
	m := testModel(t)
	m.client.Addons = sortFixture()
	m.results = map[string]service.Result{
		"b": {AddonID: "b", Kind: service.ResultFailed},
		"a": {AddonID: "a", Kind: service.ResultAvailable, To: "1.10"},
		"c": {AddonID: "c", Kind: service.ResultUpToDate},
	}
	m.refreshRows()
	return m
}

// rowIDs returns the visible rows' ids in table order.
func rowIDs(m Model) []string {
	ids := make([]string, 0, len(m.rows))
	for _, a := range m.rows {
		ids = append(ids, a.ID)
	}
	return ids
}

func TestSortKeyCyclesModes(t *testing.T) {
	m := testModel(t)
	if m.sort != sortByName {
		t.Fatalf("initial sort = %v, want name", m.sort)
	}
	for _, want := range []sortMode{sortByStatus, sortByUpstream, sortByName} {
		m = press(t, m, keyMsg('o'))
		if m.sort != want {
			t.Fatalf("sort after o = %v, want %v", m.sort, want)
		}
		if !strings.Contains(m.statusLine, "sorted by "+want.label()) {
			t.Fatalf("statusLine = %q, want the sort name", m.statusLine)
		}
	}
}

func TestSortOrdersRowsPerMode(t *testing.T) {
	m := sortModel(t)
	if got := strings.Join(rowIDs(m), ","); got != "a,b,c,d" {
		t.Fatalf("name order = %q, want a,b,c,d", got)
	}

	m.sort = sortByStatus
	m.refreshRows()
	// Attention first: failed, then the available update, then the
	// unknown, then the up-to-date one.
	if got := strings.Join(rowIDs(m), ","); got != "b,a,d,c" {
		t.Fatalf("status order = %q, want b,a,d,c", got)
	}

	m.sort = sortByUpstream
	m.refreshRows()
	// Natural version order (1.9 before 1.10), unversioned last.
	if got := strings.Join(rowIDs(m), ","); got != "a,b,d,c" {
		t.Fatalf("upstream order = %q, want a,b,d,c", got)
	}
}

func TestSortOrderPersistsAcrossRefresh(t *testing.T) {
	m := sortModel(t)
	m = press(t, m, keyMsg('o')) // status
	next, _ := m.Update(statusMsg{status: service.Status{
		Clients: []service.ClientRow{{Flavor: "forever", Addons: sortFixture()}},
	}})
	m = next.(Model)

	if m.sort != sortByStatus {
		t.Fatalf("sort mode = %v after refresh, want it to persist", m.sort)
	}
	if got := strings.Join(rowIDs(m), ","); got != "b,a,d,c" {
		t.Fatalf("order after refresh = %q, want the status order to persist", got)
	}
}

func TestSortMarkFollowsActiveColumn(t *testing.T) {
	m := sortModel(t)
	for _, want := range []string{"▲ Addon", "▲ Status", "▲ Upstream"} {
		if !strings.Contains(m.View().Content, want) {
			t.Fatalf("frame is missing the sort mark %q:\n%s", want, m.View().Content)
		}
		m = press(t, m, keyMsg('o'))
	}
}

func TestCmpNaturalOrdersVersionsNumerically(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.9", "1.10", -1},
		{"1.10", "1.9", 1},
		{"1.2.3", "1.2.3", 0},
		{"2.0", "1.10", 1},
		{"1.2a", "1.2b", -1},
		{"01.9", "1.10", -1}, // leading zeros compare numerically
		{"1.2", "1.2b1", -1}, // a string that runs out first sorts first
		{"", "", 0},
	}
	for _, tc := range cases {
		got := cmpNatural(tc.a, tc.b)
		want := tc.want
		if want > 0 {
			want = 1
		} else if want < 0 {
			want = -1
		}
		if sign(got) != want {
			t.Errorf("cmpNatural(%q, %q) = %d, want sign %d", tc.a, tc.b, got, want)
		}
	}
}

func TestCmpUpstreamSortsAbsentVersionsLast(t *testing.T) {
	for _, absent := range []string{"-", ""} {
		if c := cmpUpstream("1.0", absent); c >= 0 {
			t.Errorf("cmpUpstream(%q, %q) = %d, want a version before an absent one", "1.0", absent, c)
		}
		if c := cmpUpstream(absent, "1.0"); c <= 0 {
			t.Errorf("cmpUpstream(%q, %q) = %d, want an absent version after a version", absent, "1.0", c)
		}
	}
	if c := cmpUpstream("-", ""); c != 0 {
		t.Errorf("cmpUpstream(-, \"\") = %d, want 0", c)
	}
}

// sign reduces a comparison result to -1, 0 or 1.
func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
