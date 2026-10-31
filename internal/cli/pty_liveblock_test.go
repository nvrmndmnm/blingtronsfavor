//go:build linux

package cli

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/service"
)

// PTY regression suite (XD-41): the CLI live block runs against a real
// script(1)-style pseudo terminal and the tests assert what a user would
// see on screen — redraw behavior, spinner phases and terminal-width
// clamping of long version strings — not raw escape bytes. Everything is
// driven synchronously (the renderer's writes complete before each
// assertion reads) and the spinner advances only on the controlled tick,
// so the suite is deterministic in CI: no sleeps, no timing flakiness.

// ptyFixture runs one live block renderer against a fresh PTY: the
// renderer writes to the terminal slave exactly as production writes to
// os.Stderr, and the fixture folds the display stream into a vtScreen —
// what the user would see — after every step.
type ptyFixture struct {
	prog     *ttyProgress
	pty      *ptyPair
	screen   *vtScreen
	captured *bytes.Buffer // the command's own stdout when it is not a tty
	raw      []byte        // every display byte so far
}

// newPTYFixture builds a fixture on a cols x rows terminal. stdoutTTY
// sends the command's own stdout to the same terminal (the interactive
// case, where results land inside the block) instead of a buffer.
func newPTYFixture(t *testing.T, cols, rows int, stdoutTTY bool) *ptyFixture {
	t.Helper()
	freezeSpinner(t)
	p := openPTY(t, cols, rows)
	f := &ptyFixture{pty: p, screen: newVTScreen(cols, rows), captured: &bytes.Buffer{}}
	var out io.Writer = f.captured
	if stdoutTTY {
		out = p.out
	}
	f.prog = newTTYProgress(p.slave, out, "syncing")
	t.Cleanup(f.prog.Stop)
	return f
}

// step runs one renderer action, folds the bytes it wrote into the screen
// and returns the visible rows. The renderer's writes are synchronous, so
// draining after the action yields exactly that action's output.
func (f *ptyFixture) step(t *testing.T, action func()) []string {
	t.Helper()
	action()
	b := f.pty.drain(t)
	f.raw = append(f.raw, b...)
	f.screen.feed(b)
	return f.screen.visible()
}

// freezeSpinner stops the render loop's own ticker so spinner phases
// advance only when the test calls tick — a controlled spinner tick, no
// timing flakiness in CI.
func freezeSpinner(t *testing.T) {
	t.Helper()
	old := ttySpinFPS
	ttySpinFPS = time.Hour
	t.Cleanup(func() { ttySpinFPS = old })
}

// assertScreen compares the visible rows — the block as the user sees it —
// against the expected screen state. Trailing spaces are invisible and the
// screen trims them per row, so both sides are compared without them.
func assertScreen(t *testing.T, got, want []string) {
	t.Helper()
	norm := slices.Clone(want)
	for i := range norm {
		norm[i] = strings.TrimRight(norm[i], " ")
	}
	if !slices.Equal(got, norm) {
		t.Fatalf("screen rows =\n%q\nwant\n%q", got, norm)
	}
}

// truncateCells is the expected clamp: the first w cells of a row (the
// live block only draws width-1 runes).
func truncateCells(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		return string(r[:w])
	}
	return s
}

// TestPTYLiveBlockRedrawHoldsGeometry pins the redraw behavior on a real
// terminal: every unit's row is on screen at once, rows update and flip in
// place, a printed result line seats in its unit's row, and the block
// never grows a ghost line however often it is repainted.
func TestPTYLiveBlockRedrawHoldsGeometry(t *testing.T) {
	prog, f := newPTYFixture(t, 80, 24, false)

	got := f.step(t, func() {
		prog.Handle(stepEvent(1, 2, "forever/DeadlyBossMods", service.PhaseResolving, service.Detail{}))
	})
	assertScreen(t, got, []string{"⣾ syncing: 1/2 forever/DeadlyBossMods: resolving"})

	// Every unit's row is on screen at once, in workload order.
	got = f.step(t, func() {
		prog.Handle(stepEvent(2, 2, "forever/auctionator", service.PhaseResolving, service.Detail{}))
	})
	assertScreen(t, got, []string{
		"⣾ syncing: 1/2 forever/DeadlyBossMods: resolving",
		"⣾ syncing: 2/2 forever/auctionator: resolving",
	})

	// The finished unit flips in place to its terminal line.
	got = f.step(t, func() {
		prog.Handle(stepEvent(2, 2, "forever/auctionator", service.PhaseDone, service.Detail{Version: "342"}))
	})
	assertScreen(t, got, []string{
		"⣾ syncing: 1/2 forever/DeadlyBossMods: resolving",
		"done: 2/2 forever/auctionator: 342",
	})

	// A printed result line replaces its unit's row in place and still
	// reaches stdout byte for byte (the frozen contract).
	const frozen = "forever/auctionator: updated 341 -> 342\n"
	got = f.step(t, func() {
		prog.Print(service.Unit{Flavor: client.Flavor("forever"), AddonID: "auctionator"}, frozen)
	})
	assertScreen(t, got, []string{
		"⣾ syncing: 1/2 forever/DeadlyBossMods: resolving",
		"forever/auctionator: updated 341 -> 342",
	})
	if f.captured.String() != frozen {
		t.Errorf("stdout got %q, want the frozen line %q", f.captured.String(), frozen)
	}

	// The failure lands in its own row.
	got = f.step(t, func() {
		prog.Handle(stepEvent(1, 2, "forever/DeadlyBossMods", service.PhaseFailed, service.Detail{Err: errors.New("boom")}))
	})
	want := []string{
		"failed: 1/2 forever/DeadlyBossMods: boom",
		"forever/auctionator: updated 341 -> 342",
	}
	assertScreen(t, got, want)

	// Stop keeps the block as it stands.
	got = f.step(t, prog.Stop)
	assertScreen(t, got, want)
	if !bytes.Contains(f.raw, []byte("\r\x1b[2K")) {
		t.Error("the block was never rewritten in place")
	}
}

// TestPTYSpinnerPhasesAdvanceOnControlledTick pins the spinner phases:
// the frame advances exactly one step per controlled tick and cycles
// through the spinner set, always inside the same single screen line.
func TestPTYSpinnerPhasesAdvanceOnControlledTick(t *testing.T) {
	prog, f := newPTYFixture(t, 80, 24, false)

	const row = "syncing: 1/1 forever/Demo: downloading"
	got := f.step(t, func() {
		prog.Handle(stepEvent(1, 1, "forever/Demo", service.PhaseDownloading, service.Detail{}))
	})
	assertScreen(t, got, []string{spinner.Dot.Frames[0] + row})

	for k := 1; k <= 2*len(spinner.Dot.Frames); k++ {
		got = f.step(t, prog.tick)
		assertScreen(t, got, []string{spinner.Dot.Frames[k%len(spinner.Dot.Frames)] + row})
	}
}

// TestPTYLongRowsClampToTerminalWidth pins the terminal-width clamp: a
// long version string (or a long rate-limit reason) is cut at the
// terminal edge, so every row stays exactly one screen line and the
// redraw geometry never drifts across a long run — even on a terminal
// narrower than the row.
func TestPTYLongRowsClampToTerminalWidth(t *testing.T) {
	const cols = 40
	prog, f := newPTYFixture(t, cols, 24, false)

	const longVer = "12.1.17-beta.9+build.20261030+aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	row1 := "done: 1/2 forever/VeryLongAddonName: " + longVer
	got := f.step(t, func() {
		prog.Handle(stepEvent(1, 2, "forever/VeryLongAddonName", service.PhaseDone, service.Detail{Version: longVer}))
	})
	assertScreen(t, got, []string{truncateCells(row1, cols)})

	const longReason = "rate limited, retry after 12m (upstream asked us to slow down)"
	liveText := "syncing: 2/2 forever/OtherAddon: resolving: " + longReason
	got = f.step(t, func() {
		prog.Handle(stepEvent(2, 2, "forever/OtherAddon", service.PhaseResolving, service.Detail{Reason: longReason}))
	})
	assertScreen(t, got, []string{
		truncateCells(row1, cols),
		truncateCells(spinner.Dot.Frames[0]+liveText, cols),
	})

	// Redraws across a long run never drift: the spinner keeps turning
	// and the rows stay clamped however often the block is repainted.
	for k := 1; k <= 3; k++ {
		got = f.step(t, prog.tick)
		assertScreen(t, got, []string{
			truncateCells(row1, cols),
			truncateCells(spinner.Dot.Frames[k%len(spinner.Dot.Frames)]+liveText, cols),
		})
	}

	const longReason2 = "still rate limited, retry after 9m (upstream asked us to slow down again)"
	nextText := "syncing: 2/2 forever/OtherAddon: downloading: " + longReason2
	got = f.step(t, func() {
		prog.Handle(stepEvent(2, 2, "forever/OtherAddon", service.PhaseDownloading, service.Detail{Reason: longReason2}))
	})
	assertScreen(t, got, []string{
		truncateCells(row1, cols),
		truncateCells(spinner.Dot.Frames[3%len(spinner.Dot.Frames)]+nextText, cols),
	})
	for i, row := range got {
		if n := len([]rune(row)); n > cols {
			t.Errorf("row %d is %d cells on screen, want <= %d: %q", i, n, cols, row)
		}
	}
}

// TestPTYPrintSeatingKeepsGeometryOnScreen pins the seating on a terminal
// where results and progress share one screen (the interactive case): the
// frozen result line lands where the unit's row lives and the next
// redraw finds every row still in place.
func TestPTYPrintSeatingKeepsGeometryOnScreen(t *testing.T) {
	prog, f := newPTYFixture(t, 80, 24, true)

	got := f.step(t, func() {
		prog.Handle(stepEvent(1, 2, "forever/DeadlyBossMods", service.PhaseDone, service.Detail{Version: "12.1.18"}))
		prog.Handle(stepEvent(2, 2, "forever/auctionator", service.PhaseResolving, service.Detail{}))
	})
	assertScreen(t, got, []string{
		"done: 1/2 forever/DeadlyBossMods: 12.1.18",
		"⣾ syncing: 2/2 forever/auctionator: resolving",
	})

	const frozen = "forever/DeadlyBossMods: updated 12.1.17 -> 12.1.18\n"
	got = f.step(t, func() {
		prog.Print(service.Unit{Flavor: client.Flavor("forever"), AddonID: "DeadlyBossMods"}, frozen)
	})
	assertScreen(t, got, []string{
		"forever/DeadlyBossMods: updated 12.1.17 -> 12.1.18",
		"⣾ syncing: 2/2 forever/auctionator: resolving",
	})
	if !bytes.Contains(f.raw, []byte(strings.TrimSuffix(frozen, "\n"))) {
		t.Error("the frozen result line never reached the terminal")
	}

	// The next redraw keeps every row in place — the raw write above did
	// not disturb the geometry.
	got = f.step(t, func() {
		prog.Handle(stepEvent(2, 2, "forever/auctionator", service.PhaseDone, service.Detail{Version: "342"}))
	})
	assertScreen(t, got, []string{
		"forever/DeadlyBossMods: updated 12.1.17 -> 12.1.18",
		"done: 2/2 forever/auctionator: 342",
	})
}
