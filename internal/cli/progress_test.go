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

// progressEvent is a test fixture: one event of a two-unit workload.
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

// TestProgressTTYLiveLine pins the terminal renderer's live step line: one
// line per unit — the flow's verb, the workload slot and the unit — spun
// by the cursor-hidden rewritten status line. Phases and versions stay in
// the result lines and the pipe stream; the live line never carries them.
func TestProgressTTYLiveLine(t *testing.T) {
	var buf bytes.Buffer
	r := newProgressRenderer(&buf, progressOptions{TTY: true, Verb: "syncing"})
	r.Handle(progressEvent(service.PhaseDownloading, service.Detail{Version: "5.18.4"}))
	r.Stop() // quiesce the spinner before inspecting the output
	out := buf.String()
	for _, want := range []string{
		"\x1b[?25l",                     // cursor hidden while live
		"\r\x1b[2K",                     // line rewritten in place
		"syncing: 2/5 retail/weakauras", // verb + workload slot + unit
	} {
		if !strings.Contains(out, want) {
			t.Errorf("live output %q missing %q", out, want)
		}
	}
	if strings.Contains(out, "5.18.4") || strings.Contains(out, "downloading") {
		t.Errorf("live step line carried the phase detail: %q", out)
	}
}

// TestProgressTTYSwitchesStepToDone pins the step switch: the unit's live
// line is rewritten in place to its terminal line — "done: 2/5
// retail/weakauras" — and committed whole; the outcome detail stays in the
// result line underneath, never on the step line.
func TestProgressTTYSwitchesStepToDone(t *testing.T) {
	var buf bytes.Buffer
	r := newProgressRenderer(&buf, progressOptions{TTY: true, Verb: "syncing"})
	defer r.Stop()
	r.Handle(progressEvent(service.PhaseResolving, service.Detail{}))
	r.Handle(progressEvent(service.PhaseDone, service.Detail{Version: "5.18.4"}))
	out := buf.String()
	if !strings.Contains(out, "\r\x1b[2Kdone: 2/5 retail/weakauras\n") {
		t.Errorf("step did not switch to its terminal line in place: %q", out)
	}
	if strings.Contains(out, "5.18.4") {
		t.Errorf("terminal step line carried the result detail: %q", out)
	}
}

// TestProgressTTYTerminalLineWithoutLiveStep pins that a unit with no
// in-flight step (a skipped entry, say) still lands its terminal line as
// one whole line.
func TestProgressTTYTerminalLineWithoutLiveStep(t *testing.T) {
	var buf bytes.Buffer
	r := newProgressRenderer(&buf, progressOptions{TTY: true, Verb: "syncing"})
	defer r.Stop()
	r.Handle(progressEvent(service.PhaseSkipped, service.Detail{Reason: "manual addon"}))
	if out := buf.String(); !strings.Contains(out, "skipped: 2/5 retail/weakauras\n") || strings.Contains(out, "\r") {
		t.Errorf("terminal line not committed whole: %q", out)
	}
}

// TestProgressTTYStopRestoresTerminal pins the cleanup contract (risk
// R4): Stop commits the trailing live step to the scrollback — the step in
// flight is kept, never lost with the live region — shows the cursor
// again, is idempotent, and drops any later event.
func TestProgressTTYStopRestoresTerminal(t *testing.T) {
	var buf bytes.Buffer
	r := newProgressRenderer(&buf, progressOptions{TTY: true, Verb: "syncing"})
	r.Handle(progressEvent(service.PhaseInstalling, service.Detail{}))
	r.Stop()
	r.Stop()
	out := buf.String()
	if !strings.HasSuffix(out, "\r\x1b[2Ksyncing: 2/5 retail/weakauras\n\x1b[?25h") {
		t.Errorf("Stop left terminal state dangling or dropped the live step: %q", out)
	}
	before := len(buf.Bytes())
	r.Handle(progressEvent(service.PhaseDone, service.Detail{}))
	if len(buf.Bytes()) != before {
		t.Errorf("event after Stop wrote %q", buf.String()[before:])
	}
}

// TestProgressQuietEmitsNothing pins the "--quiet" selection: a quiet run
// writes no progress bytes at all.
func TestProgressQuietEmitsNothing(t *testing.T) {
	var buf bytes.Buffer
	r := newProgressRenderer(&buf, progressOptions{Quiet: true, TTY: true})
	defer r.Stop()
	r.Handle(progressEvent(service.PhaseDownloading, service.Detail{}))
	r.Handle(progressEvent(service.PhaseDone, service.Detail{}))
	if buf.Len() != 0 {
		t.Errorf("quiet renderer emitted %q", buf.String())
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

// TestProgressTTYOneStepPerUnit pins the terminal step presentation the
// sync UX is built on: one live step line per unit — "syncing: 1/4
// forever/DeadlyBossMods" spinning while that unit works — that switches
// in place to "done: 1/4 forever/DeadlyBossMods" when it finishes, before
// the unit's result line prints underneath; only then does the next unit's
// step line appear. No later step can ever show before an earlier one
// finished.
func TestProgressTTYOneStepPerUnit(t *testing.T) {
	var buf bytes.Buffer
	r := newProgressRenderer(&buf, progressOptions{TTY: true, Verb: "syncing"})
	dbm := func(phase service.Phase, d service.Detail) service.ProgressEvent {
		return stepEvent(1, 4, "forever/DeadlyBossMods", phase, d)
	}
	details := func(phase service.Phase, d service.Detail) service.ProgressEvent {
		return stepEvent(2, 4, "forever/Details-Damage-Meter", phase, d)
	}
	r.Handle(dbm(service.PhaseResolving, service.Detail{}))
	r.Handle(dbm(service.PhaseDownloading, service.Detail{Version: "12.1.18"}))
	r.Handle(dbm(service.PhaseDone, service.Detail{Version: "12.1.18"}))
	r.Suspend(func() { buf.WriteString("forever/DeadlyBossMods: updated 12.1.17 -> 12.1.18\n") })
	r.Handle(details(service.PhaseResolving, service.Detail{}))
	r.Handle(details(service.PhaseDone, service.Detail{Version: "12.1.18"}))
	r.Suspend(func() { buf.WriteString("forever/Details-Damage-Meter: up to date (12.1.18)\n") })
	r.Stop()
	out := buf.String()
	trail := []string{
		"done: 1/4 forever/DeadlyBossMods",
		"forever/DeadlyBossMods: updated 12.1.17 -> 12.1.18",
		"done: 2/4 forever/Details-Damage-Meter",
		"forever/Details-Damage-Meter: up to date (12.1.18)",
	}
	pos := 0
	for _, line := range trail {
		i := strings.Index(out[pos:], line+"\n")
		if i < 0 {
			t.Fatalf("trail line %q missing or out of order in %q", line, out)
		}
		pos += i + len(line) + 1
	}
	if i := strings.Index(out, "syncing: 2/4 forever/Details-Damage-Meter"); i < 0 {
		t.Errorf("second step line never spun: %q", out)
	} else if j := strings.Index(out, "forever/DeadlyBossMods: updated 12.1.17 -> 12.1.18"); i < j {
		t.Errorf("step 2 started before step 1's result printed: %q", out)
	}
}

// TestProgressTTYSuspendPrintsBesideLiveStep pins the stdout/progress
// coordination: while the command prints its own stdout lines the live
// step line is erased from the live region and redrawn below the print —
// the print lands on a clean line, never appended to the rewritten status
// (risk R1) — and the step keeps spinning afterwards.
func TestProgressTTYSuspendPrintsBesideLiveStep(t *testing.T) {
	var buf bytes.Buffer
	r := newProgressRenderer(&buf, progressOptions{TTY: true, Verb: "syncing"})
	r.Handle(progressEvent(service.PhaseInstalling, service.Detail{}))
	r.Suspend(func() { buf.WriteString("retail/weakauras: updated 5.18.3 -> 5.18.4\n") })
	r.Stop() // quiesce the spinner before inspecting the output
	out := buf.String()
	if !strings.Contains(out, "\r\x1b[2Kretail/weakauras: updated 5.18.3 -> 5.18.4\n") {
		t.Errorf("foreign line not printed on a clean line: %q", out)
	}
	if !strings.HasSuffix(out, "\r\x1b[2Ksyncing: 2/5 retail/weakauras\n\x1b[?25h") {
		t.Errorf("live step not redrawn after the print and kept through Stop: %q", out)
	}
}

// TestProgressSuspendRunsPrinter pins that the line-oriented and quiet
// renderers run the printer untouched: plain progress is line-oriented,
// and quiet suppresses progress only — never the command's own output.
func TestProgressSuspendRunsPrinter(t *testing.T) {
	var buf bytes.Buffer
	r := newProgressRenderer(&buf, progressOptions{})
	defer r.Stop()
	r.Handle(progressEvent(service.PhaseResolving, service.Detail{}))
	r.Suspend(func() { buf.WriteString("retail/weakauras: updated 5.18.3 -> 5.18.4\n") })
	want := "progress: 2/5 retail/weakauras: resolving\nretail/weakauras: updated 5.18.3 -> 5.18.4\n"
	if buf.String() != want {
		t.Errorf("suspend output = %q, want %q", buf.String(), want)
	}

	var quiet bytes.Buffer
	q := newProgressRenderer(&quiet, progressOptions{Quiet: true, TTY: true})
	defer q.Stop()
	ran := false
	q.Suspend(func() { ran = true })
	if !ran {
		t.Error("quiet renderer dropped the printer")
	}
	if quiet.Len() != 0 {
		t.Errorf("quiet renderer emitted %q", quiet.String())
	}
}
