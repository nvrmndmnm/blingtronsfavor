package cli

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

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
