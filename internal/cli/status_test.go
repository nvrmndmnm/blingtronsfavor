package cli

import (
	"fmt"
	"strings"
	"testing"
	"text/tabwriter"

	"github.com/nvrmndmnm/blingtronsfavor/internal/service"
	"github.com/nvrmndmnm/blingtronsfavor/internal/tui"
)

// statusFixture is one client section exercising every status class of the
// shared color language: green Match, yellow MultiMatch, dim "-".
func statusFixture() service.ClientRow {
	return service.ClientRow{
		Flavor:     "forever",
		FlavorName: "Forever",
		Version:    "1.60.1",
		Interface:  16001,
		Root:       "/games/World of Warcraft/_forever_",
		Addons: []service.AddonRow{
			{Name: "DBM-Core", Installed: "11.2.0", Strategy: "release", Dirs: []string{"DBM-Core"}, Status: "Match", Upstream: "11.2.1"},
			{Name: "HandyNotes", Installed: "5.4", Strategy: "release", Dirs: []string{"HandyNotes"}, Status: "MultiMatch", Upstream: "-"},
			{Name: "OldAddon", Installed: "1.0", Strategy: "manual", Status: "-", Upstream: "-"},
		},
	}
}

// TestStatusPlainTableMatchesTabwriter pins the redirected contract: with
// color off the section renders byte for byte as the historical
// text/tabwriter table did (R6 keeps pipes frozen).
func TestStatusPlainTableMatchesTabwriter(t *testing.T) {
	cs := statusFixture()
	got := captureStdout(t, func() { printClientStatus(cs, false, statusView{}) })

	var table strings.Builder
	tw := tabwriter.NewWriter(&table, 0, 4, tablePad, ' ', 0)
	_, _ = fmt.Fprintln(tw, "Addon\tVersion\tStrategy\tDirs\tTOC\tUpstream")
	for _, a := range cs.Addons {
		dirs := "-"
		if len(a.Dirs) > 0 {
			dirs = strings.Join(a.Dirs, ", ")
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", a.Name, a.Installed, a.Strategy, dirs, a.Status, a.Upstream)
	}
	_ = tw.Flush()
	want := fmt.Sprintf("%s (Interface %d) — %s\n%s", cs.FlavorName, cs.Interface, cs.Root, table.String())
	if got != want {
		t.Errorf("printClientStatus() = %q, want %q", got, want)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("plain output carries ANSI:\n%q", got)
	}
}

// TestStatusColorTableUsesSharedLanguage pins the terminal contract: the
// status cells carry the shared status color language (the exact bytes
// internal/tui decorates with), the other cells and the column alignment
// stay plain.
func TestStatusColorTableUsesSharedLanguage(t *testing.T) {
	cs := statusFixture()
	got := captureStdout(t, func() {
		printClientStatus(cs, false, statusView{tty: true, color: true})
	})
	for _, word := range []string{"Match", "MultiMatch", "-"} {
		want := tui.ColorizeStatus(word, true)
		if !strings.Contains(got, want) {
			t.Errorf("colored output is missing the shared status cell %q:\n%q", want, got)
		}
	}
	for _, plain := range []string{"DBM-Core", "11.2.0", "release", "Upstream"} {
		if !strings.Contains(got, plain) {
			t.Errorf("colored output lost the plain cell %q:\n%q", plain, got)
		}
	}
	// The header line keeps its plain shape.
	if !strings.Contains(got, fmt.Sprintf("%s (Interface %d) — %s\n", cs.FlavorName, cs.Interface, cs.Root)) {
		t.Errorf("colored output changed the section header:\n%q", got)
	}
}

// TestStatusFreshnessLineTTYOnly pins the freshness context: "checked just
// now" tops a terminal table and never leaks into plain output.
func TestStatusFreshnessLineTTYOnly(t *testing.T) {
	st := service.Status{Clients: []service.ClientRow{statusFixture()}}

	ttyOut := captureStdout(t, func() {
		_ = printStatusTables(st, false, statusView{tty: true, color: true})
	})
	if !strings.Contains(ttyOut, "checked just now") {
		t.Errorf("tty output is missing the freshness line:\n%q", ttyOut)
	}
	if !strings.Contains(ttyOut, "Upstream column is from the last update") {
		t.Errorf("tty output is missing the upstream freshness context:\n%q", ttyOut)
	}

	offlineOut := captureStdout(t, func() {
		_ = printStatusTables(st, true, statusView{tty: true})
	})
	if !strings.Contains(offlineOut, "checked just now") {
		t.Errorf("offline tty output is missing the freshness line:\n%q", offlineOut)
	}
	if strings.Contains(offlineOut, "Upstream column") {
		t.Errorf("offline freshness line mentions the dropped column:\n%q", offlineOut)
	}

	plain := captureStdout(t, func() {
		_ = printStatusTables(st, false, statusView{})
	})
	if strings.Contains(plain, "checked just now") {
		t.Errorf("plain output carries the freshness line:\n%q", plain)
	}

	empty := captureStdout(t, func() {
		_ = printStatusTables(service.Status{}, false, statusView{tty: true})
	})
	if strings.Contains(empty, "checked just now") {
		t.Errorf("empty report prints a freshness line with nothing to read:\n%q", empty)
	}
}

// TestStatusPipeOutputStaysPlain pins the end-to-end redirected contract of
// "bfctl status": no ANSI, no freshness line, the historical rows intact.
func TestStatusPipeOutputStaysPlain(t *testing.T) {
	testSyncState(t, manualAddon())
	var code int
	stdout := captureStdout(t, func() { code = runStatusTable(false, "") })
	if code != ExitOK {
		t.Errorf("runStatusTable() = %d, want %d", code, ExitOK)
	}
	if strings.Contains(stdout, "\x1b") {
		t.Errorf("piped output carries ANSI:\n%q", stdout)
	}
	if strings.Contains(stdout, "checked just now") {
		t.Errorf("piped output carries the freshness line:\n%q", stdout)
	}
	if !strings.Contains(stdout, "ManualAddOn") {
		t.Errorf("piped output lost the addon rows:\n%q", stdout)
	}
}
