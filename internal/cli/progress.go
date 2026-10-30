package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"

	"github.com/itp5b5/blessforge/internal/service"
)

// progressRenderer turns the service layer's ProgressEvents into terminal
// output while a command runs — one shared renderer for every flow, chosen
// once per run (deliverable D3). It only ever writes to the stream it was
// built for: stdout stays reserved for results and JSON (decision Q4), so
// "bfctl sync --json" stdout remains a single valid JSON document.
//
// Handle is safe for concurrent workers (risk R3): each implementation
// serializes its render loop internally, so events can never interleave on
// screen even if the caller forwards from several goroutines. Suspend runs
// the command's own stdout prints (the plan note, the frozen per-addon
// result format) beside the live step: the step line is erased first and
// redrawn below the print, so the two never collide on one terminal. Stop
// is idempotent and must be deferred right after construction (risk R4):
// it restores the terminal state — cursor shown, no dangling ANSI — and
// drops any event delivered afterwards.
//
// Wiring convention for the run handlers: construct one renderer per run,
// defer Stop, hand Handle to the service layer as the progress callback,
// route mid-run stdout prints through Suspend, and call Stop once more
// before the final outcome text (summary, "added …", error messages) —
// the outcome always prints on a quiet terminal.
type progressRenderer interface {
	Handle(ev service.ProgressEvent)
	Suspend(fn func())
	Stop()
}

// progressOptions are the run-mode facts that pick the renderer once per
// run. Only two of them change the selection:
//
//   - Quiet suppresses progress entirely ("--quiet"): errors still surface
//     in the final summary and the exit code.
//   - TTY is the stderr stream's own terminal bit, detected independently
//     of stdout (risk R5): a terminal gets the spinner-backed live step
//     line that switches to its terminal step when the unit finishes,
//     anything else — pipe, CI log, file — the same steps as incremental
//     plain lines (decisions Q1/Q4/Q6).
//
// Verb labels the live step line of the terminal renderer with the flow's
// in-flight word — "syncing", "adding", "adopting" — so each command reads
// naturally while it works. Verbose and JSON need no renderer change:
// "--verbose" adds slog debug detail beside the progress, and "--json"
// only constrains stdout, which the renderer never touches.
type progressOptions struct {
	Quiet bool
	TTY   bool
	Verb  string
}

// newProgressRenderer selects the progress renderer for one run. The
// choice is made exactly once here; flows just forward events and defer
// Stop.
func newProgressRenderer(w io.Writer, opts progressOptions) progressRenderer {
	switch {
	case opts.Quiet:
		return quietProgress{}
	case opts.TTY:
		return newTTYProgress(w, opts.Verb)
	default:
		return &plainProgress{w: w}
	}
}

// progressLine renders one event as the canonical progress line: workload
// position, unit, phase and one detail. The shape is stable and line
// oriented — "progress: <index>/<total> <flavor>/<addon>: <phase>[: <detail>]"
// — so piped output stays parseable as it streams.
func progressLine(ev service.ProgressEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "progress: %d/%d %s/%s: %s", ev.Index, ev.Total, ev.Unit.Flavor, ev.Unit.AddonID, ev.Phase)
	if d := progressDetail(ev.Detail); d != "" {
		b.WriteString(": ")
		b.WriteString(d)
	}
	return sanitizeProgressText(b.String())
}

// progressDetail picks the one human-readable detail for an event: an
// explicit reason (skip reasons, rate-limit waits) beats the error behind
// a failure, which beats the version/ref being worked on.
func progressDetail(d service.Detail) string {
	switch {
	case d.Reason != "":
		return d.Reason
	case d.Err != nil:
		return d.Err.Error()
	case d.Version != "" && d.Ref != "" && d.Ref != d.Version:
		return d.Version + " (ref " + d.Ref + ")"
	case d.Version != "":
		return d.Version
	default:
		return d.Ref
	}
}

// sanitizeProgressText strips every control byte from rendered text, so a
// detail carried through an error string can never smuggle a line break or
// an ANSI escape into progress output (decisions Q1/Q4: progress lines are
// plain and parseable whatever the stream).
func sanitizeProgressText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// quietProgress discards every event: "--quiet" emits no progress at all.
type quietProgress struct{}

func (quietProgress) Handle(service.ProgressEvent) {}

// Suspend runs fn untouched: quiet suppresses progress, not the command's
// own output.
func (quietProgress) Suspend(fn func()) { fn() }

func (quietProgress) Stop() {}

// plainProgress writes one incremental plain line per event — the pipe/CI
// behaviour (decisions Q1/Q6). Lines carry zero ANSI/control bytes and end
// in "\n", so a consumer can parse them as they arrive.
type plainProgress struct {
	mu sync.Mutex
	w  io.Writer
}

func (p *plainProgress) Handle(ev service.ProgressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintln(p.w, progressLine(ev))
}

// Suspend runs fn untouched: plain lines are whole lines of their own, so
// foreign output can never be appended to one.
func (p *plainProgress) Suspend(fn func()) { fn() }

func (p *plainProgress) Stop() {}

// ttyProgress keeps one live step line per unit on a terminal stderr — the
// step presentation the sync UX is built on: the line "<verb>: 1/4
// forever/DeadlyBossMods" spins while that one unit resolves, downloads and
// installs, and switches in place to its terminal line ("done: 1/4
// forever/DeadlyBossMods") the moment the unit finishes — just before the
// unit's result line prints underneath and the next unit's step line
// appears. A step is never swapped away by a later one and a later step can
// never show before an earlier one finished. The spinner keeps turning on
// the live line between events so long waits never look like a hang
// (objective O1), and a rate-limit wait (risk R6) appends its reason to the
// line while it lasts. Only the live line is ever rewritten; every other
// line lands whole in the scrollback. The spinner frames are the TUI's
// (bubbles spinner.Dot) to keep both surfaces visually alike.
type ttyProgress struct {
	mu     sync.Mutex
	w      io.Writer
	verb   string // the flow's in-flight label ("syncing", "adding", …)
	frames []string
	frame  int
	ev     service.ProgressEvent // the unit whose step line is live
	live   bool                  // ev names the unit now on screen
	status string                // live status text; empty while nothing is live
	shown  bool                  // the live line is currently on screen
	hidden bool                  // the cursor is currently hidden
	closed bool                  // Stop has run; later events are dropped

	once    sync.Once
	done    chan struct{}
	stopped chan struct{}
}

// newTTYProgress starts the render loop's spinner. Nothing is written
// until the first event, so an eventless run emits no terminal noise.
func newTTYProgress(w io.Writer, verb string) *ttyProgress {
	if verb == "" {
		verb = "working"
	}
	p := &ttyProgress{
		w:       w,
		verb:    verb,
		frames:  spinner.Dot.Frames,
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go p.spin()
	return p
}

// spin advances the spinner frame at the TUI's frame rate until Stop.
func (p *ttyProgress) spin() {
	defer close(p.stopped)
	t := time.NewTicker(spinner.Dot.FPS)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-t.C:
			p.tick()
		}
	}
}

func (p *ttyProgress) tick() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.status == "" {
		return
	}
	p.frame = (p.frame + 1) % len(p.frames)
	p.draw()
}

// Handle renders one event. Live phases (resolving/downloading/installing)
// keep the unit's step line spinning — created on the unit's first event,
// its rate-limit reason shown while it lasts — and terminal phases
// (done/failed/skipped) switch that same line in place to the terminal
// line and commit it to the scrollback, before the unit's result line
// prints underneath.
func (p *ttyProgress) Handle(ev service.ProgressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	switch ev.Phase {
	case service.PhaseDone, service.PhaseFailed, service.PhaseSkipped:
		if p.live && sameUnit(p.ev, ev) {
			p.clear() // the unit's own line switches in place
		} else {
			p.commit() // another unit was live: its step stays visible
		}
		p.live = false
		p.status = ""
		_, _ = fmt.Fprintln(p.w, terminalStepLine(ev))
	default:
		status := liveStepLine(p.verb, ev)
		if d := sanitizeProgressText(ev.Detail.Reason); d != "" {
			status += ": " + d
		}
		if p.live && sameUnit(p.ev, ev) {
			if p.status == status {
				return
			}
		} else {
			p.commit()
		}
		p.ev, p.live, p.status = ev, true, status
		p.draw()
	}
}

// Suspend runs fn on a clean line while a step line is live: the live line
// is erased first and redrawn below fn's output — the command's own stdout
// lines are never appended to the rewritten status (risk R1) — and the
// step keeps spinning afterwards instead of being committed early. fn runs
// under the render lock — it must not emit progress events.
func (p *ttyProgress) Suspend(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		fn()
		return
	}
	p.clear()
	fn()
	if p.status != "" {
		p.draw()
	}
}

// Stop shuts the render loop down and restores the terminal: the trailing
// live line is committed to the scrollback — a step in flight when the run
// ends, an interrupt above all, must stay visible — and the cursor is
// shown again: no dangling ANSI state (risk R4).
func (p *ttyProgress) Stop() {
	p.once.Do(func() {
		close(p.done)
		<-p.stopped
		p.mu.Lock()
		defer p.mu.Unlock()
		p.closed = true
		p.commit()
		if p.hidden {
			_, _ = fmt.Fprint(p.w, "\x1b[?25h")
			p.hidden = false
		}
	})
}

// draw rewrites the live status line in place. Callers hold p.mu.
func (p *ttyProgress) draw() {
	if !p.hidden {
		_, _ = fmt.Fprint(p.w, "\x1b[?25l")
		p.hidden = true
	}
	_, _ = fmt.Fprintf(p.w, "\r\x1b[2K%s%s", p.frames[p.frame], p.status)
	p.shown = true
}

// commit finalizes the live status line into the scrollback as a plain
// line, so the step it names stays visible after the live region moves on.
// Callers hold p.mu.
func (p *ttyProgress) commit() {
	if p.status == "" {
		return
	}
	p.clear()
	_, _ = fmt.Fprintln(p.w, p.status)
	p.status = ""
}

// clear erases the live line if one is on screen. Callers hold p.mu.
func (p *ttyProgress) clear() {
	if p.shown {
		_, _ = fmt.Fprint(p.w, "\r\x1b[2K")
		p.shown = false
	}
}

// liveStepLine renders a unit's in-flight step line:
// "<verb>: <index>/<total> <flavor>/<addon>".
func liveStepLine(verb string, ev service.ProgressEvent) string {
	return sanitizeProgressText(fmt.Sprintf("%s: %d/%d %s/%s", verb, ev.Index, ev.Total, ev.Unit.Flavor, ev.Unit.AddonID))
}

// terminalStepLine renders a unit's finalized step line: the terminal
// phase replaces the live line's verb in place — "done: 1/4
// forever/DeadlyBossMods". The outcome detail stays in the result line
// underneath, never on the step line.
func terminalStepLine(ev service.ProgressEvent) string {
	return sanitizeProgressText(fmt.Sprintf("%s: %d/%d %s/%s", ev.Phase, ev.Index, ev.Total, ev.Unit.Flavor, ev.Unit.AddonID))
}

// sameUnit reports whether two events report on the same unit slot.
func sameUnit(a, b service.ProgressEvent) bool {
	return a.Unit == b.Unit && a.Index == b.Index && a.Total == b.Total
}

// interruptGuard restores the terminal when SIGINT aborts a run that has no
// graceful interrupt path ("bfctl add", "bfctl adopt"): the renderer is
// stopped — the trailing step committed to the scrollback, the cursor
// shown again — and the signal is re-raised with the default disposition,
// so the process terminates exactly as it would have without the guard: no
// message and no exit-code change (objective O5). A run that handles SIGINT
// itself ("bfctl sync", the TUI)
// must not install the guard: the re-raise would preempt its own
// cancellation and rollback reporting. The returned function removes the
// guard and must be deferred right after installation.
func interruptGuard(prog progressRenderer) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
			prog.Stop()
			signal.Reset(os.Interrupt)
			if p, err := os.FindProcess(os.Getpid()); err == nil {
				_ = p.Signal(os.Interrupt)
			}
		case <-done:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
