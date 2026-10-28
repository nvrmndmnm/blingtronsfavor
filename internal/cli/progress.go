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
// screen even if the caller forwards from several goroutines. Suspend
// withdraws the live status line while the command prints its own stdout
// lines (the frozen per-addon result format), so the two never collide on
// one terminal. Stop is idempotent and must be deferred right after
// construction (risk R4): it restores the terminal state — cursor shown,
// no dangling ANSI — and drops any event delivered afterwards.
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
//     of stdout (risk R5): a terminal gets the live status line, anything
//     else — pipe, CI log, file — degrades to incremental plain lines
//     (decisions Q1/Q4/Q6).
//
// Verbose and JSON need no renderer change: "--verbose" adds slog debug
// detail beside the progress, and "--json" only constrains stdout, which
// the renderer never touches.
type progressOptions struct {
	Quiet bool
	TTY   bool
}

// newProgressRenderer selects the progress renderer for one run. The
// choice is made exactly once here; flows just forward events and defer
// Stop.
func newProgressRenderer(w io.Writer, opts progressOptions) progressRenderer {
	switch {
	case opts.Quiet:
		return quietProgress{}
	case opts.TTY:
		return newTTYProgress(w)
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

// ttyProgress renders one live status line on a terminal stderr: the line
// is rewritten in place and the spinner keeps turning between events, so
// long waits never look like a hang (objective O1). The spinner frames are
// the TUI's (bubbles spinner.Dot) to keep both surfaces visually alike.
// Terminal phases are committed to the scrollback as plain lines, so
// finished units stay visible while the live line moves on.
type ttyProgress struct {
	mu     sync.Mutex
	w      io.Writer
	frames []string
	frame  int
	status string // live status text; empty while nothing is live
	shown  bool   // the live line is currently on screen
	hidden bool   // the cursor is currently hidden
	closed bool   // Stop has run; later events are dropped

	once    sync.Once
	done    chan struct{}
	stopped chan struct{}
}

// newTTYProgress starts the render loop's spinner. Nothing is written
// until the first event, so an eventless run emits no terminal noise.
func newTTYProgress(w io.Writer) *ttyProgress {
	p := &ttyProgress{
		w:       w,
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

// Handle rewrites the live line for a running phase and commits a plain
// line for a terminal one (done/failed/skipped).
func (p *ttyProgress) Handle(ev service.ProgressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	line := progressLine(ev)
	switch ev.Phase {
	case service.PhaseDone, service.PhaseFailed, service.PhaseSkipped:
		p.clear()
		_, _ = fmt.Fprintln(p.w, line)
		p.status = ""
	default:
		p.status = line
		p.draw()
	}
}

// Suspend withdraws the live status line while fn prints the command's own
// stdout lines (the frozen per-addon result format): the print lands on a
// clean line instead of being appended to the rewritten status, and the
// status returns with the next event or spinner tick. fn runs under the
// render lock — it must not emit progress events.
func (p *ttyProgress) Suspend(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		fn()
		return
	}
	p.clear()
	fn()
}

// Stop shuts the render loop down and restores the terminal: the live line
// is erased and the cursor shown again — no dangling ANSI state (risk R4).
func (p *ttyProgress) Stop() {
	p.once.Do(func() {
		close(p.done)
		<-p.stopped
		p.mu.Lock()
		defer p.mu.Unlock()
		p.closed = true
		p.clear()
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

// clear erases the live line if one is on screen. Callers hold p.mu.
func (p *ttyProgress) clear() {
	if p.shown {
		_, _ = fmt.Fprint(p.w, "\r\x1b[2K")
		p.shown = false
	}
}

// interruptGuard restores the terminal when SIGINT aborts a run that has no
// graceful interrupt path ("bfctl add", "bfctl adopt"): the renderer is
// stopped — live line erased, cursor shown again — and the signal is
// re-raised with the default disposition, so the process terminates exactly
// as it would have without the guard: no message and no exit-code change
// (objective O5). A run that handles SIGINT itself ("bfctl sync", the TUI)
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
