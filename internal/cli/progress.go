package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/itp5b5/blessforge/internal/service"
)

// progressRenderer turns the service layer's ProgressEvents into terminal
// output while a command runs — one shared renderer for every flow, chosen
// once per run (deliverable D3). It only ever writes progress to the
// stream it was built for: stdout stays reserved for results and JSON
// (decision Q4), so "bfctl sync --json" stdout remains a single valid JSON
// document.
//
// Handle is safe for concurrent workers (risk R3): each implementation
// serializes its render loop internally, so events can never interleave on
// screen even if the caller forwards from several goroutines. Print seats
// one block of the command's own stdout output (the frozen per-addon
// result lines) into the display without breaking its layout: the text is
// written to stdout unchanged — the frozen contract holds byte for byte —
// and on a terminal it lands exactly where the unit's row lives, so every
// addon's story stays in one place. Stop is idempotent and must be deferred
// right after construction (risk R4): it restores the terminal state —
// cursor shown, no dangling ANSI — and drops any event delivered
// afterwards.
//
// Wiring convention for the run handlers: construct one renderer per run,
// defer Stop, hand Handle to the service layer as the progress callback,
// route the command's own stdout lines through Print, and call Stop once
// more before the final outcome text (summary, "added …", error messages)
// — the outcome always prints on a quiet terminal.
type progressRenderer interface {
	Handle(ev service.ProgressEvent)
	Print(unit service.Unit, text string)
	Stop()
}

// progressMode names the explicit rendering mode "--progress" selects.
// The zero value is auto — today's selection untouched — and the other
// modes force one renderer whatever the streams look like, so a script can
// pin the presentation: live draws the block even off a terminal, plain
// streams the incremental lines even on one, and none silences progress
// entirely. An explicit mode overrides "--quiet" for progress rendering
// ("--quiet" still raises the log threshold); the vocabulary itself never
// changes with the mode (the UX contract in docs/bfctl.1).
type progressMode int

const (
	progressAuto progressMode = iota
	progressLive
	progressPlain
	progressNone
)

// progressModeWords is the "--progress" vocabulary, in the order the usage
// text lists the values.
var progressModeWords = []string{"auto", "live", "plain", "none"}

func (m progressMode) String() string {
	if m < 0 || int(m) >= len(progressModeWords) {
		return progressModeWords[0]
	}
	return progressModeWords[m]
}

// Set implements flag.Value for "--progress=auto|live|plain|none".
func (m *progressMode) Set(s string) error {
	for i, word := range progressModeWords {
		if s == word {
			*m = progressMode(i)
			return nil
		}
	}
	return fmt.Errorf("unknown progress mode %q (want auto, live, plain or none)", s)
}

// registerProgressFlag adds "--progress" to a progress-reporting
// subcommand's flag set and returns the parsed mode (auto unless set).
func registerProgressFlag(fs *flag.FlagSet) *progressMode {
	m := new(progressMode)
	fs.Var(m, "progress", "progress rendering: auto (default; TTY detection), live, plain or none")
	return m
}

// progressOptions are the run-mode facts that pick the renderer once per
// run:
//
//   - Mode is the explicit "--progress" selection. Anything but auto picks
//     that renderer outright; auto is today's behavior, chosen from Quiet
//     and TTY exactly as before.
//   - Quiet suppresses progress entirely ("--quiet"): errors still surface
//     in the final summary and the exit code.
//   - TTY is the stderr stream's own terminal bit, detected independently
//     of stdout (risk R5): a terminal gets the live per-addon block, one
//     row per unit that spins and updates in place as the work goes,
//     anything else — pipe, CI log, file — the same steps as incremental
//     plain lines (decisions Q1/Q4/Q6).
//
// Verb labels the live rows of the terminal renderer with the flow's
// in-flight word — "syncing", "adding", "adopting" — so each command reads
// naturally while it works. Verbose and JSON need no renderer change:
// "--verbose" adds slog debug detail beside the progress, and "--json"
// only constrains stdout, which progress never touches.
//
// Stdout is where Print's text goes; nil means os.Stdout. Tests inject a
// buffer to observe the frozen result lines independently of the terminal
// stream.
type progressOptions struct {
	Mode   progressMode
	Quiet  bool
	TTY    bool
	Verb   string
	Stdout io.Writer
}

// newProgressRenderer selects the progress renderer for one run. The
// choice is made exactly once here; flows just forward events and defer
// Stop. An explicit --progress mode picks the renderer outright; auto
// falls through to the historical Quiet/TTY selection unchanged.
func newProgressRenderer(w io.Writer, opts progressOptions) progressRenderer {
	out := opts.Stdout
	if out == nil {
		out = os.Stdout
	}
	switch opts.Mode {
	case progressNone:
		return quietProgress{out: out}
	case progressLive:
		return newTTYProgress(w, out, opts.Verb)
	case progressPlain:
		return &plainProgress{w: w, out: out}
	}
	switch {
	case opts.Quiet:
		return quietProgress{out: out}
	case opts.TTY:
		return newTTYProgress(w, out, opts.Verb)
	default:
		return &plainProgress{w: w, out: out}
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

// termCaps are the terminal capabilities the live block renders for — the
// CLI mirror of the TUI's caps (internal/tui/theme.go) so both surfaces
// fall back alike: the locale decides the glyphs, NO_COLOR decides the
// colors. Nothing the block shows may depend on a glyph or a color being
// renderable (risk R4).
type termCaps struct {
	unicode bool // terminal locale is UTF-8: unicode glyphs allowed
	color   bool // colors allowed (NO_COLOR-aware)
}

// detectTermCaps derives the capabilities from the environment.
func detectTermCaps() termCaps {
	return termCaps{
		unicode: localeUTF8(),
		color:   os.Getenv("NO_COLOR") == "",
	}
}

// localeUTF8 reports whether the locale environment explicitly asks for
// UTF-8. LC_ALL, LC_CTYPE and LANG are consulted in that order (POSIX
// precedence); the first one set decides. With no locale set only Windows
// defaults to unicode (Go writes UTF-8 to its console); everywhere else
// the safe fallback is ASCII. (The same rule as internal/tui's localeUTF8.)
func localeUTF8() bool {
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if val := os.Getenv(name); val != "" {
			return strings.Contains(strings.ToLower(val), "utf")
		}
	}
	return runtime.GOOS == "windows"
}

// asciiSpinner is the spinner set for non-UTF-8 terminals: spinner.Line's
// plain-ASCII frames (the TUI's fallback), padded to the two-column shape
// of spinner.Dot's braille frames so a row's geometry never depends on
// which set is in use.
var asciiSpinner = []string{"| ", "/ ", "- ", "\\ "}

// terminalWidth reports w's width in columns when w is a terminal, and 0
// when it is not or the size cannot be read: 0 is "no known bound", so a
// display that is not a terminal is never clamped.
func terminalWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	width, _, err := term.GetSize(f.Fd())
	if err != nil {
		return 0
	}
	return width
}

// quietProgress discards every event: "--quiet" emits no progress at all.
type quietProgress struct {
	out io.Writer
}

func (quietProgress) Handle(service.ProgressEvent) {}

// Print writes the command's own output untouched: quiet suppresses
// progress, not the result lines.
func (p quietProgress) Print(_ service.Unit, text string) {
	_, _ = fmt.Fprint(p.out, text)
}

func (quietProgress) Stop() {}

// plainProgress writes one incremental plain line per event — the pipe/CI
// behaviour (decisions Q1/Q6). Lines carry zero ANSI/control bytes and end
// in "\n", so a consumer can parse them as they arrive.
type plainProgress struct {
	mu  sync.Mutex
	w   io.Writer
	out io.Writer
}

func (p *plainProgress) Handle(ev service.ProgressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintln(p.w, progressLine(ev))
}

// Print writes the command's own output untouched: plain lines are whole
// lines of their own, so foreign output can never be appended to one.
func (p *plainProgress) Print(_ service.Unit, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprint(p.out, text)
}

func (p *plainProgress) Stop() {}

// blockEntry is one entry of the terminal renderer's live block: a unit's
// row (the row the unit's events update in place) or a static note the
// command printed beside a row. texts are the entry's screen lines; a live
// entry's first line carries the spinner frame and is rewritten as the
// unit's phases change.
type blockEntry struct {
	unit  service.Unit
	index int // workload slot; rows sort by it so the block is report-ordered
	row   bool
	live  bool
	texts []string
}

// ttySpinFPS is the live block's spinner frame rate. Tests replace it
// with a long interval and drive ttyProgress.tick directly, so spinner
// phases advance on a controlled tick in CI — no timing flakiness (XD-41).
var ttySpinFPS = spinner.Dot.FPS

// fdWriter is the writer capability the terminal-width probe needs: an
// *os.File (os.Stderr in production) exposes its file descriptor.
type fdWriter interface {
	Fd() uintptr
}

// ttyProgress keeps a live block with one row per unit on a terminal
// stderr — the presentation the sync UX is built on: every unit's row is
// visible at once, each row tracking its own addon through the phases
// ("syncing: 1/4 forever/DeadlyBossMods: downloading", spinner, rewritten
// in place) and flipping in place to its terminal line ("done: 1/4
// forever/DeadlyBossMods: 12.1.18") when the unit finishes. A result
// printed through Print replaces the unit's row with the frozen result
// line, so an addon's whole story stays in one place and nothing spews as
// a chunk. Every drawn row is clamped to the terminal width, so a long
// version string can never soft-wrap and corrupt the redraw geometry.
// The spinner keeps turning on live rows between events so long
// waits never look like a hang (objective O1), and a rate-limit wait (risk
// R6) shows its reason on the row while it lasts. Only rows are ever
// rewritten; every other line lands whole in the scrollback. The spinner
// frames are the TUI's (bubbles spinner.Dot) to keep both surfaces visually
// alike, falling back to plain-ASCII frames on non-UTF-8 terminals (risk
// R4), and every row is clamped to the terminal width so a long line can
// never wrap and corrupt the redraw geometry (the XD-16 long-line caveat).
type ttyProgress struct {
	mu     sync.Mutex
	w      io.Writer
	out    io.Writer
	outTTY bool // out is a terminal too: its writes land on this screen
	verb   string
	frames []string
	frame  int
	color  bool // colors allowed (NO_COLOR-aware); the block draws none
	width  int  // terminal columns rows are clamped to; 0 = unknown

	entries []*blockEntry // the block, top to bottom
	byUnit  map[service.Unit]*blockEntry
	drawn   int  // screen lines currently occupied by the block
	hidden  bool // the cursor is currently hidden
	closed  bool // Stop has run; later events are dropped

	once    sync.Once
	done    chan struct{}
	stopped chan struct{}
}

// newTTYProgress starts the render loop's spinner. Nothing is written
// until the first event, so an eventless run emits no terminal noise.
func newTTYProgress(w, out io.Writer, verb string) *ttyProgress {
	if verb == "" {
		verb = "working"
	}
	caps := detectTermCaps()
	frames := spinner.Dot.Frames
	if !caps.unicode {
		frames = asciiSpinner
	}
	outTTY := false
	if f, ok := out.(*os.File); ok {
		outTTY = streamIsTTY(f)
	}
	p := &ttyProgress{
		w:       w,
		out:     out,
		outTTY:  outTTY,
		verb:    verb,
		frames:  frames,
		color:   caps.color,
		width:   terminalWidth(w),
		byUnit:  make(map[service.Unit]*blockEntry),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go p.spin()
	return p
}

// spin advances the spinner frame at the TUI's frame rate until Stop.
func (p *ttyProgress) spin() {
	defer close(p.stopped)
	t := time.NewTicker(ttySpinFPS)
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
	if p.closed || !p.anyLive() {
		return
	}
	p.frame = (p.frame + 1) % len(p.frames)
	p.draw()
}

// anyLive reports whether some row is still spinning. Callers hold p.mu.
func (p *ttyProgress) anyLive() bool {
	for _, e := range p.entries {
		if e.live {
			return true
		}
	}
	return false
}

// Handle renders one event. Live phases (resolving/downloading/installing)
// update the unit's row in place — created on the unit's first event,
// sorted into the block by workload slot, its rate-limit reason shown while
// it lasts — and terminal phases (done/failed/skipped) switch that same row
// in place to the terminal line ("done: 2/5 retail/weakauras: 5.18.4").
// All rows are on screen at once, so concurrent units each spin in their
// own row while the others work.
func (p *ttyProgress) Handle(ev service.ProgressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	e := p.rowFor(ev)
	switch ev.Phase {
	case service.PhaseDone, service.PhaseFailed, service.PhaseSkipped:
		e.live = false
		e.texts = []string{terminalStepLine(ev)}
	default:
		e.live = true
		e.texts = []string{liveStepLine(p.verb, ev)}
	}
	p.draw()
}

// rowFor returns the unit's row, creating and inserting it in workload
// order on the unit's first event. Callers hold p.mu.
func (p *ttyProgress) rowFor(ev service.ProgressEvent) *blockEntry {
	if e, ok := p.byUnit[ev.Unit]; ok {
		return e
	}
	e := &blockEntry{unit: ev.Unit, index: ev.Index, row: true, texts: []string{liveStepLine(p.verb, ev)}}
	pos := 0
	for i, other := range p.entries {
		if other.row && other.index < ev.Index {
			pos = i + 1
		}
	}
	p.entries = append(p.entries, nil)
	copy(p.entries[pos+1:], p.entries[pos:])
	p.entries[pos] = e
	p.byUnit[ev.Unit] = e
	return e
}

// Print seats one block of the command's own stdout output into the
// display and writes it to stdout unchanged — the frozen per-addon result
// lines reach scripts byte for byte while the terminal keeps its layout.
// For a finished unit the text replaces the unit's row in place, so the
// row becomes the result line itself; for a unit still in flight ("bfctl
// add --explain" prints its plan note mid-run) the text lands just above
// the row and the row keeps spinning below it.
func (p *ttyProgress) Print(unit service.Unit, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_, _ = fmt.Fprint(p.out, text)
		return
	}
	lines := printLines(text)
	if len(lines) == 0 {
		return
	}
	anchor := p.seat(unit, lines)
	// Park the cursor on the anchor line so the stdout write lands where
	// the rows live instead of stray at the block's foot.
	if p.drawn > anchor {
		_, _ = fmt.Fprintf(p.w, "\x1b[%dA", p.drawn-anchor)
	}
	_, _ = fmt.Fprint(p.w, "\r")
	_, _ = fmt.Fprint(p.out, text)
	if p.outTTY {
		// The write advanced the terminal cursor over the text's screen
		// rows — wrapped rows included, which screenAdvance counts: the
		// frozen write itself is never clamped.
		p.drawn = anchor + screenAdvance(text, p.rowWidth())
	} else {
		p.drawn = anchor
	}
	p.draw()
}

// seat lays the printed lines into the block and returns the index of the
// first screen line their content begins at. Callers hold p.mu.
func (p *ttyProgress) seat(unit service.Unit, lines []string) int {
	e, ok := p.byUnit[unit]
	if !ok {
		// No row to seat into: append a static entry at the block's foot.
		p.entries = append(p.entries, &blockEntry{unit: unit, texts: lines})
		return p.totalLines() - len(lines)
	}
	idx := p.lineIndex(e)
	if e.live {
		// A note beside a live row: it lands just above the row, which
		// shifts down and keeps spinning below it.
		pos := p.sliceIndex(e)
		p.entries = append(p.entries, nil)
		copy(p.entries[pos+1:], p.entries[pos:])
		p.entries[pos] = &blockEntry{unit: unit, texts: lines}
		return idx
	}
	e.texts = lines
	return idx
}

// printLines splits printed text into its screen lines (a trailing newline
// is not a line of its own).
func printLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// screenLine prepares one entry line for the screen. With NO_COLOR set no
// styling reaches the screen at all (ansi.Strip drops every escape and
// keeps every word — the color-never-alone rule, risk R4), and the line is
// clamped to the terminal width so one entry line is always exactly one
// screen row however long its text. The entry's text itself is untouched:
// the full story stays in the block and in the frozen stdout lines.
func (p *ttyProgress) screenLine(s string) string {
	if !p.color {
		s = ansi.Strip(s)
	}
	if w := p.rowWidth(); w > 0 {
		s = ansi.Truncate(s, w, "")
	}
	return s
}

// rowWidth returns the width rows are clamped and wrap-counted at: the
// terminal's live width re-probed as the stream is drawn, falling back to
// the width measured at construction when the stream reports no size (a
// test buffer). Re-probing keeps a resize mid-run from leaving stale
// geometry; the fallback keeps non-terminal streams clampable.
func (p *ttyProgress) rowWidth() int {
	if w := p.terminalWidth(); w > 0 {
		return w
	}
	return p.width
}

// screenAdvance returns how many screen rows the cursor drops when text is
// written verbatim to a width-column terminal — Print's frozen stdout
// write, which is never clamped and so may wrap. The block's bookkeeping
// (drawn) must count every wrapped row or the redraw's cursor-up
// arithmetic drifts (the XD-16 long-line caveat). width 0 is an unknown
// width (the stream is not a terminal): one row per line, as ever.
func screenAdvance(text string, width int) int {
	lines := printLines(text)
	n := 0
	for i, line := range lines {
		partial := i == len(lines)-1 && !strings.HasSuffix(text, "\n")
		n += lineAdvance(ansi.StringWidth(line), width, partial)
	}
	return n
}

// lineAdvance returns the rows one written line contributes to the
// cursor's advance on a width-column terminal. A complete line spans
// 1+⌈(cells-1)/width⌉ rows — terminals wrap lazily, so a line ending
// exactly at the right margin stays on its row — while a final line
// without a newline leaves the cursor mid-row and counts only the rows it
// crosses.
func lineAdvance(cells, width int, partial bool) int {
	if width <= 0 {
		if partial {
			return 0
		}
		return 1
	}
	if cells == 0 {
		if partial {
			return 0
		}
		return 1
	}
	if partial {
		return (cells - 1) / width
	}
	return 1 + (cells-1)/width
}

// lineIndex returns the block-relative screen line where the entry starts.
// Callers hold p.mu.
func (p *ttyProgress) lineIndex(e *blockEntry) int {
	n := 0
	for _, other := range p.entries {
		if other == e {
			return n
		}
		n += len(other.texts)
	}
	return n
}

// sliceIndex returns the entry's position in p.entries. Callers hold p.mu.
func (p *ttyProgress) sliceIndex(e *blockEntry) int {
	for i, other := range p.entries {
		if other == e {
			return i
		}
	}
	return len(p.entries)
}

// totalLines returns the block's height in screen lines. Callers hold p.mu.
func (p *ttyProgress) totalLines() int {
	n := 0
	for _, e := range p.entries {
		n += len(e.texts)
	}
	return n
}

// Stop shuts the render loop down and restores the terminal: the block —
// rows in flight included — stays in the scrollback (a unit still working
// when the run ends, an interrupt above all, must stay visible) and the
// cursor is shown again: no dangling ANSI state (risk R4).
func (p *ttyProgress) Stop() {
	p.once.Do(func() {
		close(p.done)
		<-p.stopped
		p.mu.Lock()
		defer p.mu.Unlock()
		p.closed = true
		if p.hidden {
			_, _ = fmt.Fprint(p.w, "\x1b[?25h")
			p.hidden = false
		}
	})
}

// terminalWidth reports the display width of the renderer's terminal
// stream — 0 when the stream is not a terminal. Re-probed through
// rowWidth on every draw so a resize mid-run can never leave stale
// geometry.
func (p *ttyProgress) terminalWidth() int {
	w, ok := p.w.(fdWriter)
	if !ok {
		return 0
	}
	width, _, err := term.GetSize(w.Fd())
	if err != nil {
		return 0
	}
	return width
}

// draw repaints the whole block in place: the cursor moves to the block's
// top line, every line is rewritten there, and any line the block no longer
// has is erased. Rows are whole lines ending in "\n", so the cursor always
// rests on a clean line below the block. Each row goes through screenLine —
// NO_COLOR-aware and clamped to the terminal width — so one entry line is
// always exactly one screen row and the line count always matches the
// block geometry (the long-line drift guard).
// Callers hold p.mu.
func (p *ttyProgress) draw() {
	if !p.hidden {
		_, _ = fmt.Fprint(p.w, "\x1b[?25l")
		p.hidden = true
	}
	if p.drawn > 0 {
		_, _ = fmt.Fprintf(p.w, "\x1b[%dA", p.drawn)
	}
	for _, e := range p.entries {
		for j, text := range e.texts {
			prefix := ""
			if e.live && j == 0 {
				prefix = p.frames[p.frame]
			}
			_, _ = fmt.Fprintf(p.w, "\r\x1b[2K%s\n", p.screenLine(prefix+text))
		}
	}
	n := p.totalLines()
	if L := p.drawn - n; L > 0 {
		// The block shrank: erase the orphaned lines and settle the
		// cursor back below the block.
		for i := 0; i < L; i++ {
			_, _ = fmt.Fprint(p.w, "\r\x1b[2K\n")
		}
		_, _ = fmt.Fprintf(p.w, "\x1b[%dA", L)
	}
	p.drawn = n
}

// liveStepLine renders a unit's in-flight row: the spinner frame is added
// at draw time; here the flow's verb, the workload slot, the unit and the
// phase it is working through — "syncing: 1/4 forever/DeadlyBossMods:
// downloading". A rate-limit wait shows its reason instead of going
// silent (risk R6).
func liveStepLine(verb string, ev service.ProgressEvent) string {
	s := fmt.Sprintf("%s: %d/%d %s/%s: %s", verb, ev.Index, ev.Total, ev.Unit.Flavor, ev.Unit.AddonID, ev.Phase)
	if d := sanitizeProgressText(ev.Detail.Reason); d != "" {
		s += ": " + d
	}
	return sanitizeProgressText(s)
}

// terminalStepLine renders a unit's finalized row: the terminal phase
// replaces the live row's verb in place and carries the outcome detail —
// "done: 2/5 retail/weakauras: 5.18.4", "failed: …: <error>", "skipped:
// …: <reason>" — until a printed result line replaces the row outright.
func terminalStepLine(ev service.ProgressEvent) string {
	s := fmt.Sprintf("%s: %d/%d %s/%s", ev.Phase, ev.Index, ev.Total, ev.Unit.Flavor, ev.Unit.AddonID)
	if d := progressDetail(ev.Detail); d != "" {
		s += ": " + d
	}
	return sanitizeProgressText(s)
}

// interruptGuard restores the terminal when SIGINT aborts a run that has no
// graceful interrupt path ("bfctl add", "bfctl adopt"): the renderer is
// stopped — the block kept as it stands, the cursor shown again — and the
// signal is re-raised with the default disposition, so the process
// terminates exactly as it would have without the guard: no message and no
// exit-code change (objective O5). A run that handles SIGINT itself
// ("bfctl sync", the TUI)
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
