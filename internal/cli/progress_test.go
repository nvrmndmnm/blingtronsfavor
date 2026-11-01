package cli

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	"github.com/charmbracelet/x/ansi"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/service"
)

// progressEvent is a test fixture: one event of a five-unit workload.
func progressEvent(phase service.Phase, d service.Detail) service.ProgressEvent {
	return service.ProgressEvent{
		Unit:   service.Unit{Flavor: client.Flavor("retail"), AddonID: "weakauras"},
		Phase:  phase,
		Detail: d,
		Index:  2,
		Total:  5,
	}
}

// stepEvent is a test fixture: one event of one unit's step in a workload,
// the unit named "flavor/addon".
func stepEvent(index, total int, unit string, phase service.Phase, d service.Detail) service.ProgressEvent {
	flavor, addon, _ := strings.Cut(unit, "/")
	return service.ProgressEvent{
		Unit:   service.Unit{Flavor: client.Flavor(flavor), AddonID: addon},
		Phase:  phase,
		Detail: d,
		Index:  index,
		Total:  total,
	}
}

// assertNoControlBytes pins the non-TTY contract (decisions Q1/Q4/Q6):
// progress output over a pipe is plain text — no ANSI escapes, no carriage
// returns, no other control bytes — only "\n" line breaks.
func assertNoControlBytes(t *testing.T, out []byte) {
	t.Helper()
	for i, b := range out {
		if b == '\n' {
			continue
		}
		if b < 0x20 || b == 0x7f {
			t.Fatalf("control byte %#x at offset %d in %q", b, i, out)
		}
	}
}

// assertNoColorEscape pins the NO_COLOR contract (risk R4): not one SGR
// sequence — the only bytes that color a terminal — reaches the display.
func assertNoColorEscape(t *testing.T, out []byte) {
	t.Helper()
	if re := regexp.MustCompile(`\x1b\[[0-9;]*m`); re.Match(out) {
		t.Fatalf("display carried color %q", re.Find(out))
	}
}

// assertASCIIOnly pins the non-UTF-8 terminal contract (risk R4): the
// display never carries a non-ASCII byte.
func assertASCIIOnly(t *testing.T, out []byte) {
	t.Helper()
	for i, b := range out {
		if b >= 0x80 {
			t.Fatalf("non-ASCII byte %#x at offset %d in %q", b, i, out)
		}
	}
}

// drawnRowRE matches one row rewrite on the display: draw() writes every
// row as "\r\x1b[2K<row>\n", and an erased orphan row matches empty.
var drawnRowRE = regexp.MustCompile("\r\x1b\\[2K([^\n]*)\n")

// drawnRows returns the content of every row the renderer wrote to the
// display, in order.
func drawnRows(out string) []string {
	var rows []string
	for _, m := range drawnRowRE.FindAllStringSubmatch(out, -1) {
		rows = append(rows, m[1])
	}
	return rows
}

// assertRowsFit pins the row clamp (proposal B4): every row the renderer
// draws fits the terminal width, so one row is always one screen row and
// the redraw's cursor arithmetic can never drift.
func assertRowsFit(t *testing.T, out string, width int) {
	t.Helper()
	for i, row := range drawnRows(out) {
		if w := ansi.StringWidth(row); w > width {
			t.Errorf("drawn row %d is %d columns, want <= %d: %q", i, w, width, row)
		}
	}
}

// rowStartsWithAnyFrame reports whether the display drew one of frames in
// front of want. The exact frame is not pinned (the spinner turns between
// writes); only the set is.
func rowStartsWithAnyFrame(out string, frames []string, want string) bool {
	for _, frame := range frames {
		if strings.Contains(out, frame+want) {
			return true
		}
	}
	return false
}

// ttyFixture builds a terminal renderer whose display goes to term and the
// command's own stdout lines go to out — the two streams a real run keeps
// apart (progress on stderr, results on stdout).
type ttyFixture struct {
	prog      *ttyProgress
	term, out *bytes.Buffer
}

func newTTYFixture() *ttyFixture {
	f := &ttyFixture{term: &bytes.Buffer{}, out: &bytes.Buffer{}}
	f.prog = newTTYProgress(f.term, f.out, "syncing")
	return f
}

// rows returns each block entry's screen lines in block order.
func (f *ttyFixture) rows() [][]string {
	var rows [][]string
	for _, e := range f.prog.entries {
		rows = append(rows, e.texts)
	}
	return rows
}

// TestProgressPlainLines pins the pipe/CI renderer: one incremental plain
// line per event in the canonical shape, with the detail attached after
// the phase — reason first, then error, then version/ref.
func TestProgressPlainLines(t *testing.T) {
	var buf bytes.Buffer
	r := newProgressRenderer(&buf, progressOptions{})
	defer r.Stop()
	events := []service.ProgressEvent{
		progressEvent(service.PhaseResolving, service.Detail{}),
		progressEvent(service.PhaseDownloading, service.Detail{Version: "5.18.4", Ref: "v5.18.4"}),
		progressEvent(service.PhaseFailed, service.Detail{Err: errors.New("cannot resolve owner/repo")}),
		progressEvent(service.PhaseSkipped, service.Detail{Reason: "manual addon"}),
		progressEvent(service.PhaseResolving, service.Detail{Reason: "rate limited, retry after 12m", Err: errors.New("403")}),
		progressEvent(service.PhaseDone, service.Detail{Version: "5.18.4", Ref: "main"}),
	}
	for _, ev := range events {
		r.Handle(ev)
	}
	want := []string{
		"progress: 2/5 retail/weakauras: resolving",
		"progress: 2/5 retail/weakauras: downloading: 5.18.4 (ref v5.18.4)",
		"progress: 2/5 retail/weakauras: failed: cannot resolve owner/repo",
		"progress: 2/5 retail/weakauras: skipped: manual addon",
		"progress: 2/5 retail/weakauras: resolving: rate limited, retry after 12m",
		"progress: 2/5 retail/weakauras: done: 5.18.4 (ref main)",
	}
	got := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("got %d lines %q, want %d", len(got), buf.String(), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
	assertNoControlBytes(t, buf.Bytes())
}

// TestProgressPlainSanitizesDetail pins that detail text carried through
// an error can never smuggle a line break or an ANSI escape into a
// progress line: the pipe consumer always sees whole, plain lines.
func TestProgressPlainSanitizesDetail(t *testing.T) {
	var buf bytes.Buffer
	r := newProgressRenderer(&buf, progressOptions{})
	defer r.Stop()
	r.Handle(progressEvent(service.PhaseFailed, service.Detail{Err: errors.New("bad\n\x1b[31mred\x07")}))
	assertNoControlBytes(t, buf.Bytes())
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Errorf("got %d lines for 1 event, want 1: %q", n, buf.String())
	}
}

// TestProgressTTYLiveRowUpdatesInPlace pins the terminal renderer's live
// row: one row per unit — the flow's verb, the workload slot, the unit and
// the phase it is working through — spun and rewritten in place as the
// phases change. The version detail stays out of the live row; it lands in
// the terminal line and the result line.
func TestProgressTTYLiveRowUpdatesInPlace(t *testing.T) {
	f := newTTYFixture()
	f.prog.Handle(progressEvent(service.PhaseResolving, service.Detail{}))
	f.prog.Handle(progressEvent(service.PhaseDownloading, service.Detail{Version: "5.18.4"}))
	f.prog.Stop() // quiesce the spinner before inspecting the output
	out := f.term.String()
	for _, want := range []string{
		"\x1b[?25l", // cursor hidden while live
		"\r\x1b[2K", // row rewritten in place
		"syncing: 2/5 retail/weakauras: resolving",   // verb + slot + unit + phase
		"syncing: 2/5 retail/weakauras: downloading", // the phase updates in place
	} {
		if !strings.Contains(out, want) {
			t.Errorf("live output %q missing %q", out, want)
		}
	}
	if strings.Contains(out, "5.18.4") {
		t.Errorf("live row carried the version detail: %q", out)
	}
}

// TestProgressTTYLiveBlockKeepsEveryUnitAtOnce pins the block contract the
// sync UX is built on: every unit's row is in the block at the same time,
// rows sort by workload slot whatever order the units start in, and each
// row tracks its own unit — concurrent units never steal each other's row
// or appear out of order.
func TestProgressTTYLiveBlockKeepsEveryUnitAtOnce(t *testing.T) {
	f := newTTYFixture()
	f.prog.Handle(stepEvent(3, 4, "forever/auctionator", service.PhaseResolving, service.Detail{}))
	f.prog.Handle(stepEvent(1, 4, "forever/DeadlyBossMods", service.PhaseDownloading, service.Detail{Version: "12.1.18"}))
	f.prog.Handle(stepEvent(2, 4, "forever/Details-Damage-Meter", service.PhaseResolving, service.Detail{}))
	f.prog.Stop()
	rows := f.rows()
	want := [][]string{
		{"syncing: 1/4 forever/DeadlyBossMods: downloading"},
		{"syncing: 2/4 forever/Details-Damage-Meter: resolving"},
		{"syncing: 3/4 forever/auctionator: resolving"},
	}
	if len(rows) != len(want) {
		t.Fatalf("block has %d rows %q, want %d", len(rows), rows, len(want))
	}
	for i := range want {
		if rows[i][0] != want[i][0] {
			t.Errorf("row %d = %q, want %q", i, rows[i][0], want[i][0])
		}
	}
}

// TestProgressTTYTerminalRowCarriesDetail pins the row switch: the unit's
// live row is rewritten in place to its terminal line — "done: 2/5
// retail/weakauras: 5.18.4" — carrying the outcome detail, the row's
// final state before a printed result replaces it.
func TestProgressTTYTerminalRowCarriesDetail(t *testing.T) {
	f := newTTYFixture()
	f.prog.Handle(progressEvent(service.PhaseResolving, service.Detail{}))
	f.prog.Handle(progressEvent(service.PhaseDone, service.Detail{Version: "5.18.4"}))
	f.prog.Stop()
	if !strings.Contains(f.term.String(), "\r\x1b[2Kdone: 2/5 retail/weakauras: 5.18.4\n") {
		t.Errorf("row did not switch to its terminal line in place: %q", f.term.String())
	}
}

// TestProgressTTYTerminalRowWithoutLiveRow pins that a unit with no
// in-flight row (a skipped entry, say) still lands its terminal line as
// one whole row.
func TestProgressTTYTerminalRowWithoutLiveRow(t *testing.T) {
	f := newTTYFixture()
	f.prog.Handle(progressEvent(service.PhaseSkipped, service.Detail{Reason: "manual addon"}))
	f.prog.Stop()
	rows := f.rows()
	if len(rows) != 1 || rows[0][0] != "skipped: 2/5 retail/weakauras: manual addon" {
		t.Errorf("rows = %q, want the terminal row", rows)
	}
	if out := f.term.String(); !strings.Contains(out, "\r\x1b[2Kskipped: 2/5 retail/weakauras: manual addon\n") {
		t.Errorf("terminal row not committed whole: %q", out)
	}
}

// TestProgressTTYPrintReplacesFinishedRow pins the result seating: the
// printed result line is written to stdout byte for byte (the frozen
// contract) and replaces the finished unit's row in place, so the addon's
// whole story stays in one row instead of spewing below the block.
func TestProgressTTYPrintReplacesFinishedRow(t *testing.T) {
	f := newTTYFixture()
	f.prog.Handle(stepEvent(1, 2, "forever/DeadlyBossMods", service.PhaseDone, service.Detail{Version: "12.1.18"}))
	f.prog.Handle(stepEvent(2, 2, "forever/auctionator", service.PhaseResolving, service.Detail{}))
	const result = "forever/DeadlyBossMods: updated 12.1.17 -> 12.1.18\n"
	f.prog.Print(service.Unit{Flavor: client.Flavor("forever"), AddonID: "DeadlyBossMods"}, result)
	f.prog.Stop()
	if f.out.String() != result {
		t.Errorf("stdout got %q, want the frozen line %q", f.out.String(), result)
	}
	rows := f.rows()
	want := [][]string{
		{"forever/DeadlyBossMods: updated 12.1.17 -> 12.1.18"},
		{"syncing: 2/2 forever/auctionator: resolving"},
	}
	if len(rows) != len(want) {
		t.Fatalf("block has %d rows %q, want %d", len(rows), rows, len(want))
	}
	for i := range want {
		if rows[i][0] != want[i][0] {
			t.Errorf("row %d = %q, want %q", i, rows[i][0], want[i][0])
		}
	}
	if !strings.Contains(f.term.String(), "\r\x1b[2Kforever/DeadlyBossMods: updated 12.1.17 -> 12.1.18\n") {
		t.Errorf("result line not seated in the row on screen: %q", f.term.String())
	}
}

// TestProgressTTYPrintNoteKeepsLiveRow pins the mid-run note ("bfctl add
// --explain" prints its plan note while the unit still works): the note
// lands just above the unit's row and the row keeps spinning below it.
func TestProgressTTYPrintNoteKeepsLiveRow(t *testing.T) {
	f := newTTYFixture()
	f.prog.Handle(progressEvent(service.PhaseInstalling, service.Detail{}))
	const note = "plan: matching release asset\n"
	f.prog.Print(service.Unit{Flavor: client.Flavor("retail"), AddonID: "weakauras"}, note)
	f.prog.Stop()
	if f.out.String() != note {
		t.Errorf("stdout got %q, want %q", f.out.String(), note)
	}
	rows := f.rows()
	want := [][]string{
		{"plan: matching release asset"},
		{"syncing: 2/5 retail/weakauras: installing"},
	}
	if len(rows) != len(want) {
		t.Fatalf("block has %d rows %q, want %d", len(rows), rows, len(want))
	}
	for i := range want {
		if rows[i][0] != want[i][0] {
			t.Errorf("row %d = %q, want %q", i, rows[i][0], want[i][0])
		}
	}
}

// TestProgressTTYPrintMultiLineResult pins a multi-line print (--explain's
// plan line): every line lands in the unit's row and the rows below shift
// down intact.
func TestProgressTTYPrintMultiLineResult(t *testing.T) {
	f := newTTYFixture()
	f.prog.Handle(stepEvent(1, 2, "forever/DeadlyBossMods", service.PhaseDone, service.Detail{}))
	f.prog.Handle(stepEvent(2, 2, "forever/auctionator", service.PhaseResolving, service.Detail{}))
	const result = "forever/DeadlyBossMods: up to date (12.1.18)\n    plan: release ref=v12.1.18 reason: matching release asset\n"
	f.prog.Print(service.Unit{Flavor: client.Flavor("forever"), AddonID: "DeadlyBossMods"}, result)
	f.prog.Stop()
	if f.out.String() != result {
		t.Errorf("stdout got %q, want %q", f.out.String(), result)
	}
	rows := f.rows()
	want := []string{
		"forever/DeadlyBossMods: up to date (12.1.18)",
		"    plan: release ref=v12.1.18 reason: matching release asset",
		"syncing: 2/2 forever/auctionator: resolving",
	}
	var flat []string
	for _, r := range rows {
		flat = append(flat, r...)
	}
	if len(flat) != len(want) {
		t.Fatalf("block has %d lines %q, want %d", len(flat), flat, len(want))
	}
	for i := range want {
		if flat[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, flat[i], want[i])
		}
	}
}

// TestProgressTTYStopRestoresTerminal pins the cleanup contract (risk
// R4): Stop keeps the block — rows in flight included — as it stands,
// shows the cursor again, is idempotent, and drops any later event.
func TestProgressTTYStopRestoresTerminal(t *testing.T) {
	f := newTTYFixture()
	f.prog.Handle(progressEvent(service.PhaseInstalling, service.Detail{}))
	f.prog.Stop()
	f.prog.Stop()
	out := f.term.String()
	if !strings.HasSuffix(out, "syncing: 2/5 retail/weakauras: installing\n\x1b[?25h") {
		t.Errorf("Stop left terminal state dangling or dropped the live row: %q", out)
	}
	before := f.term.Len()
	f.prog.Handle(progressEvent(service.PhaseDone, service.Detail{}))
	if f.term.Len() != before {
		t.Errorf("event after Stop wrote %q", f.term.String()[before:])
	}
}

// TestProgressTTYPrintAfterStopStillWritesStdout pins that a late print
// (a result decided as the run shuts down) still reaches stdout — the
// frozen lines are never dropped — it just skips the dead display.
func TestProgressTTYPrintAfterStopStillWritesStdout(t *testing.T) {
	f := newTTYFixture()
	f.prog.Stop()
	const result = "retail/weakauras: updated 5.18.3 -> 5.18.4\n"
	f.prog.Print(service.Unit{Flavor: client.Flavor("retail"), AddonID: "weakauras"}, result)
	if f.out.String() != result {
		t.Errorf("stdout got %q, want %q", f.out.String(), result)
	}
}

// TestProgressQuietEmitsNothing pins the "--quiet" selection: a quiet run
// writes no progress bytes at all, but the command's own result lines
// still reach stdout.
func TestProgressQuietEmitsNothing(t *testing.T) {
	var out bytes.Buffer
	r := newProgressRenderer(&bytes.Buffer{}, progressOptions{Quiet: true, TTY: true, Stdout: &out})
	defer r.Stop()
	r.Handle(progressEvent(service.PhaseDownloading, service.Detail{}))
	r.Handle(progressEvent(service.PhaseDone, service.Detail{}))
	const result = "retail/weakauras: updated 5.18.3 -> 5.18.4\n"
	r.Print(service.Unit{Flavor: client.Flavor("retail"), AddonID: "weakauras"}, result)
	if out.String() != result {
		t.Errorf("quiet renderer dropped the result line: %q", out.String())
	}
}

// TestProgressConcurrentEventsStayLineOriented pins the serialized render
// loop (risk R3): concurrent workers never interleave, every event lands
// as one intact line. Run with -race this also proves the renderer state
// is race-free.
func TestProgressConcurrentEventsStayLineOriented(t *testing.T) {
	var buf bytes.Buffer
	r := newProgressRenderer(&buf, progressOptions{})
	defer r.Stop()
	const workers, perWorker = 8, 50
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				r.Handle(progressEvent(service.PhaseResolving, service.Detail{}))
			}
		}(w)
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != workers*perWorker {
		t.Fatalf("got %d lines, want %d", len(lines), workers*perWorker)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "progress: 2/5 retail/weakauras: resolving") {
			t.Fatalf("line %d garbled: %q", i, line)
		}
	}
	assertNoControlBytes(t, buf.Bytes())
}

// TestProgressPrintWritesStdout pins that the line-oriented and quiet
// renderers write the command's own output untouched: plain progress is
// line-oriented, and quiet suppresses progress only — never the result
// lines.
func TestProgressPrintWritesStdout(t *testing.T) {
	var buf, out bytes.Buffer
	r := newProgressRenderer(&buf, progressOptions{Stdout: &out})
	defer r.Stop()
	r.Handle(progressEvent(service.PhaseResolving, service.Detail{}))
	const result = "retail/weakauras: updated 5.18.3 -> 5.18.4\n"
	r.Print(service.Unit{Flavor: client.Flavor("retail"), AddonID: "weakauras"}, result)
	if want := "progress: 2/5 retail/weakauras: resolving\n"; buf.String() != want {
		t.Errorf("progress output = %q, want %q", buf.String(), want)
	}
	if out.String() != result {
		t.Errorf("stdout = %q, want %q", out.String(), result)
	}
}

// TestProgressTTYSpinnerFramesFollowTheLocale pins the ASCII spinner
// fallback (XD-45, proposal B3): the frames are the TUI's spinner.Dot on
// UTF-8 terminals and spinner.Line's plain-ASCII set otherwise, so a
// non-UTF-8 terminal never sees a non-ASCII byte (risk R4).
func TestProgressTTYSpinnerFramesFollowTheLocale(t *testing.T) {
	t.Run("UTF-8 terminal keeps the braille frames", func(t *testing.T) {
		t.Setenv("LC_ALL", "C.UTF-8")
		f := newTTYFixture()
		f.prog.Handle(progressEvent(service.PhaseResolving, service.Detail{}))
		f.prog.Stop() // quiesce the spinner before inspecting the output
		if !rowStartsWithAnyFrame(f.term.String(), spinner.Dot.Frames, "syncing: 2/5 retail/weakauras: resolving") {
			t.Errorf("live row did not spin with the braille frames: %q", f.term.String())
		}
	})
	t.Run("non-UTF-8 terminal falls back to the ASCII frames", func(t *testing.T) {
		t.Setenv("LC_ALL", "C")
		f := newTTYFixture()
		f.prog.Handle(progressEvent(service.PhaseResolving, service.Detail{}))
		f.prog.Stop()
		if !rowStartsWithAnyFrame(f.term.String(), asciiSpinner, "syncing: 2/5 retail/weakauras: resolving") {
			t.Errorf("live row did not spin with the ASCII frames: %q", f.term.String())
		}
		assertASCIIOnly(t, f.term.Bytes())
	})
}

// TestProgressTTYNoColorKeepsWordsIntact pins the NO_COLOR fallback (XD-45,
// proposal B3): with NO_COLOR set no color reaches the screen — styling is
// stripped from the display copy — while every word stays (the
// color-never-alone rule) and the frozen stdout line keeps its bytes
// untouched.
func TestProgressTTYNoColorKeepsWordsIntact(t *testing.T) {
	const styled = "\x1b[32mforever/DeadlyBossMods: updated 12.1.17 -> 12.1.18\x1b[0m\n"
	t.Run("NO_COLOR strips color from the display", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		f := newTTYFixture()
		f.prog.Handle(progressEvent(service.PhaseDone, service.Detail{Version: "5.18.4"}))
		f.prog.Print(service.Unit{Flavor: client.Flavor("forever"), AddonID: "DeadlyBossMods"}, styled)
		f.prog.Stop()
		if f.out.String() != styled {
			t.Errorf("stdout = %q, want the frozen bytes %q", f.out.String(), styled)
		}
		assertNoColorEscape(t, f.term.Bytes())
		for _, want := range []string{
			"done: 2/5 retail/weakauras: 5.18.4",
			"forever/DeadlyBossMods: updated 12.1.17 -> 12.1.18",
		} {
			if !strings.Contains(f.term.String(), want) {
				t.Errorf("display lost the words %q: %q", want, f.term.String())
			}
		}
	})
	t.Run("color allowed leaves the display bytes untouched", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		f := newTTYFixture()
		f.prog.Handle(progressEvent(service.PhaseDone, service.Detail{Version: "5.18.4"}))
		f.prog.Print(service.Unit{Flavor: client.Flavor("forever"), AddonID: "DeadlyBossMods"}, styled)
		f.prog.Stop()
		if want := strings.TrimSuffix(styled, "\n"); !strings.Contains(f.term.String(), want) {
			t.Errorf("display rewrote the styled text: %q", f.term.String())
		}
	})
}

// TestProgressTTYClampsRowsToTerminalWidth pins the row clamp (XD-45,
// proposal B4): a long version string can never wrap its row — one entry
// line is always one screen row — so the redraw geometry can never drift.
// The row's text keeps the full story; only the screen copy is clamped.
func TestProgressTTYClampsRowsToTerminalWidth(t *testing.T) {
	f := newTTYFixture()
	f.prog.width = 24 // the fixture's buffer has no size; pin the clamp width
	const long = "RareScanner_FOREVER_1.60.1.b15-build.4712+abcdef0123456789"
	f.prog.Handle(stepEvent(1, 2, "forever/rarescanner", service.PhaseResolving, service.Detail{}))
	f.prog.Handle(stepEvent(1, 2, "forever/rarescanner", service.PhaseDone, service.Detail{Version: long}))
	f.prog.Handle(stepEvent(2, 2, "forever/auctionator", service.PhaseResolving, service.Detail{}))
	f.prog.Stop()
	rows := f.rows()
	if rows[0][0] != "done: 1/2 forever/rarescanner: "+long {
		t.Errorf("row lost the full text: %q", rows[0][0])
	}
	assertRowsFit(t, f.term.String(), 24)
}

// TestProgressTTYLongPrintKeepsRedrawGeometry pins the frozen-write
// geometry (XD-45, proposal B4): a result line longer than the terminal
// wraps when it lands on the shared screen, and the block counts every
// wrapped row and erases the orphaned rows afterwards, so the block's
// cursor arithmetic never drifts however long the run goes.
func TestProgressTTYLongPrintKeepsRedrawGeometry(t *testing.T) {
	f := newTTYFixture()
	f.prog.width = 10    // the fixture's buffer has no size; pin the width
	f.prog.outTTY = true // the stdout write lands on the same screen
	f.prog.Handle(stepEvent(1, 1, "forever/auctionator", service.PhaseDone, service.Detail{}))
	const result = "forever/auctionator: updated 12.1.17 -> 12.1.18 plus a very long tail\n"
	f.prog.Print(service.Unit{Flavor: client.Flavor("forever"), AddonID: "auctionator"}, result)
	f.prog.Handle(stepEvent(2, 2, "forever/rarescanner", service.PhaseResolving, service.Detail{}))
	f.prog.Stop()
	if f.out.String() != result {
		t.Errorf("stdout = %q, want the frozen line %q", f.out.String(), result)
	}
	if !strings.Contains(f.term.String(), "\r\x1b[2K\n") {
		t.Errorf("the wrapped remainder was never erased: %q", f.term.String())
	}
	assertRowsFit(t, f.term.String(), 10)
	if f.prog.drawn != f.prog.totalLines() {
		t.Errorf("drawn = %d rows, block has %d: geometry drifted", f.prog.drawn, f.prog.totalLines())
	}
}

// TestScreenAdvance pins the frozen-write row accounting: how far the
// cursor drops when Print writes a line verbatim to a width-column
// terminal. Terminals wrap lazily — a line ending exactly at the right
// margin stays on its row — and a final line without a newline leaves the
// cursor mid-row. Width 0 (not a terminal) keeps the old one-row-per-line
// accounting.
func TestScreenAdvance(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		width int
		want  int
	}{
		{"one short line", "abc\n", 10, 1},
		{"one short line without newline", "abc", 10, 0},
		{"line ending exactly at the margin", "0123456789\n", 10, 1},
		{"wrapped line", "0123456789A\n", 10, 2},
		{"margin line without newline", "0123456789", 10, 0},
		{"wrapped line without newline", "0123456789A", 10, 1},
		{"two lines", "a\nb\n", 10, 2},
		{"two lines without final newline", "a\nb", 10, 1},
		{"blank line", "\n", 10, 1},
		{"escapes do not count as cells", "\x1b[31mabc\x1b[0m\n", 10, 1},
		{"empty text", "", 10, 0},
		{"unknown width: one row per line", strings.Repeat("x", 40) + "\n", 0, 1},
		{"unknown width without newline", strings.Repeat("x", 40), 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := screenAdvance(tt.text, tt.width); got != tt.want {
				t.Errorf("screenAdvance(%q, %d) = %d, want %d", tt.text, tt.width, got, tt.want)
			}
		})
	}
}

// TestProgressModeFlagParsesTheModeVocabulary pins the --progress values
// (XD-48): auto, live, plain and none are accepted, anything else is a
// usage error naming the vocabulary.
func TestProgressModeFlagParsesTheModeVocabulary(t *testing.T) {
	fs := subcommandFlagSet("sync")
	mode := registerProgressFlag(fs)
	if err := fs.Parse([]string{"--progress=plain"}); err != nil {
		t.Fatalf("parse --progress=plain: %v", err)
	}
	if *mode != progressPlain {
		t.Fatalf("mode = %v, want plain", *mode)
	}
	for word, want := range map[string]progressMode{"auto": progressAuto, "live": progressLive, "plain": progressPlain, "none": progressNone} {
		if err := mode.Set(word); err != nil {
			t.Fatalf("--progress=%s rejected: %v", word, err)
		}
		if *mode != want {
			t.Fatalf("--progress=%s set mode %v, want %v", word, *mode, want)
		}
	}
	err := mode.Set("bogus")
	if err == nil {
		t.Fatal("--progress=bogus must be a usage error")
	}
	for _, want := range []string{"bogus", "auto, live, plain or none"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("usage error %q is missing %q", err, want)
		}
	}
}

// TestProgressModeMatrix pins the --progress matrix (XD-48): none is
// silent whatever the streams look like (the result lines still reach
// stdout), plain streams the incremental lines even on a terminal, live
// draws the block even off one, and auto keeps today's behavior bit for
// bit — the exact bytes the historical Quiet/TTY selection produced when
// no --progress is passed.
func TestProgressModeMatrix(t *testing.T) {
	defer func(fps time.Duration) { ttySpinFPS = fps }(ttySpinFPS)
	ttySpinFPS = time.Hour // no spinner tick: every render is deterministic

	unit := service.Unit{Flavor: client.Flavor("forever"), AddonID: "DeadlyBossMods"}
	const result = "forever/DeadlyBossMods: up to date (12.1.18)\n"
	run := func(mode progressMode, quiet, tty bool) (term, out string) {
		var termBuf, outBuf bytes.Buffer
		r := newProgressRenderer(&termBuf, progressOptions{Mode: mode, Quiet: quiet, TTY: tty, Verb: "syncing", Stdout: &outBuf})
		r.Handle(stepEvent(1, 2, "forever/DeadlyBossMods", service.PhaseResolving, service.Detail{}))
		r.Handle(stepEvent(1, 2, "forever/DeadlyBossMods", service.PhaseDone, service.Detail{Version: "12.1.18"}))
		r.Print(unit, result)
		r.Stop()
		return termBuf.String(), outBuf.String()
	}

	t.Run("none is silent on a terminal", func(t *testing.T) {
		term, out := run(progressNone, false, true)
		if term != "" {
			t.Errorf("progress display wrote %q, want silence", term)
		}
		if out != result {
			t.Errorf("stdout = %q, want the frozen result line untouched", out)
		}
	})
	t.Run("plain on a terminal", func(t *testing.T) {
		term, _ := run(progressPlain, false, true)
		assertNoControlBytes(t, []byte(term))
		if !strings.Contains(term, "progress: 1/2 forever/DeadlyBossMods: resolving") ||
			!strings.Contains(term, "progress: 1/2 forever/DeadlyBossMods: done: 12.1.18") {
			t.Errorf("plain mode lost the incremental lines: %q", term)
		}
	})
	t.Run("live off a terminal", func(t *testing.T) {
		term, _ := run(progressLive, false, false)
		if !strings.Contains(term, "syncing: 1/2 forever/DeadlyBossMods: resolving") ||
			!strings.Contains(term, "done: 1/2 forever/DeadlyBossMods: 12.1.18") {
			t.Errorf("live mode lost the block rows: %q", term)
		}
	})
	t.Run("an explicit mode overrides --quiet", func(t *testing.T) {
		term, _ := run(progressPlain, true, true)
		if !strings.Contains(term, "progress: 1/2 forever/DeadlyBossMods: resolving") {
			t.Errorf("--quiet suppressed an explicit --progress=plain: %q", term)
		}
	})
	t.Run("auto matches today's behavior bit for bit", func(t *testing.T) {
		// At the default (no --progress) the renderer is today's: a
		// terminal gets the live block, a pipe gets the plain lines and
		// --quiet gets silence — byte for byte the explicit mode each of
		// those stream facts maps to.
		for _, tc := range []struct {
			name       string
			quiet, tty bool
			want       progressMode
		}{
			{"terminal", false, true, progressLive},
			{"pipe", false, false, progressPlain},
			{"quiet", true, true, progressNone},
		} {
			autoTerm, autoOut := run(progressAuto, tc.quiet, tc.tty)
			wantTerm, wantOut := run(tc.want, tc.quiet, tc.tty)
			if autoTerm != wantTerm || autoOut != wantOut {
				t.Errorf("%s: auto renders differ from --progress=%s", tc.name, tc.want)
			}
		}
	})
}

// TestPhaseLabelsAreTheServicePhaseWords pins the progress vocabulary on
// the CLI side (the UX contract in docs/bfctl.1): every live row, terminal
// row and pipe line is labeled by the service.Phase word itself — the same
// strings the TUI renders for the same states, so the two surfaces cannot
// drift apart.
func TestPhaseLabelsAreTheServicePhaseWords(t *testing.T) {
	for _, p := range []service.Phase{service.PhaseResolving, service.PhaseDownloading, service.PhaseInstalling, service.PhaseDone, service.PhaseFailed, service.PhaseSkipped} {
		ev := progressEvent(p, service.Detail{})
		if got := liveStepLine("syncing", ev); !strings.Contains(got, ": "+string(p)) {
			t.Errorf("liveStepLine(%s) = %q, want the phase word %q", p, got, p)
		}
		if got := progressLine(ev); !strings.Contains(got, ": "+string(p)) {
			t.Errorf("progressLine(%s) = %q, want the phase word %q", p, got, p)
		}
	}
	for _, p := range []service.Phase{service.PhaseDone, service.PhaseFailed, service.PhaseSkipped} {
		if got := terminalStepLine(progressEvent(p, service.Detail{})); !strings.HasPrefix(got, string(p)+": ") {
			t.Errorf("terminalStepLine(%s) = %q, want the phase word %q as its label", p, got, p)
		}
	}
}

// TestSyncVerbSharesTheTUIVocabulary pins the flow verbs: a check-only run
// works under "checking", the same verb the TUI's check preview uses.
func TestSyncVerbSharesTheTUIVocabulary(t *testing.T) {
	if got := syncVerb(false); got != "syncing" {
		t.Errorf("syncVerb(false) = %q, want %q", got, "syncing")
	}
	if got := syncVerb(true); got != "checking" {
		t.Errorf("syncVerb(true) = %q, want %q", got, "checking")
	}
}
