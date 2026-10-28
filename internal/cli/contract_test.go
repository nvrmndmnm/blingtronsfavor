package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
)

// This file pins the frozen output contract of "bfctl sync" (decision Q5,
// objective O3): the historical per-addon line format, "sync --json" as one
// clean JSON document on stdout, plain parseable progress when output is
// redirected, "--quiet" suppression and the exit codes 0/1/2/4 — plus the
// SIGINT mid-render terminal cleanup (risk R4). Every sync test drives the
// real runSync end to end without touching the network: the manual fixture
// is skipped and the broken one fails at source parsing, which exercises
// the result, progress and exit paths alike.

// manualAddon is the fixture for an addon installed by a foreign tool:
// sync skips it with its reason and never resolves anything upstream.
func manualAddon() *state.AddonState {
	return &state.AddonState{
		ID:       "ManualAddOn",
		Source:   state.SourceRef{Kind: state.SourceManual, Slug: "ManualAddOn"},
		Strategy: state.StrategyManual,
	}
}

// brokenAddon is the fixture for a GitHub addon whose recorded source does
// not parse: sync reports exactly one failed unit — still without a single
// network call.
func brokenAddon() *state.AddonState {
	return &state.AddonState{
		ID:     "BrokenAddOn",
		Source: state.SourceRef{Kind: state.SourceGitHub, Slug: "not a repo"},
	}
}

// testSyncState points bfctl's config and cache at fresh temp directories
// and records one forever client (Forever 1.60.1, interface 16001) with the
// given addons.
func testSyncState(t *testing.T, addons ...*state.AddonState) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	if err := paths.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	path, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]*state.AddonState, len(addons))
	for _, a := range addons {
		byID[a.ID] = a
	}
	st := state.New()
	st.Clients[client.FlavorForever] = &state.ClientState{
		Flavor:    client.FlavorForever,
		Root:      filepath.Join(t.TempDir(), "_forever_"),
		Interface: 16001,
		Addons:    byID,
	}
	if err := state.Open(path).Save(st); err != nil {
		t.Fatal(err)
	}
}

// captureStreams runs fn with os.Stdout and os.Stderr replaced by pipes and
// returns everything written to each stream — two redirected streams, which
// is exactly the non-TTY environment the contract cares about (risk R5).
func captureStreams(t *testing.T, fn func()) (string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outR.Close() }()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = errR.Close() }()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	fn()
	_ = outW.Close()
	_ = errW.Close()
	out, err := io.ReadAll(outR)
	if err != nil {
		t.Fatal(err)
	}
	errOut, err := io.ReadAll(errR)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), string(errOut)
}

// runSyncCaptured runs one real "bfctl sync" against the fixture state and
// returns both streams plus the exit code. The shared logging flags are
// reset afterwards so one run cannot leak --quiet into the next.
func runSyncCaptured(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	t.Cleanup(func() { logQuiet, logVerbose = false, false })
	var code int
	stdout, stderr := captureStreams(t, func() {
		code = runSync(args, BuildInfo{Version: "test"})
	})
	return stdout, stderr, code
}

// TestSyncPerAddonLineFormatFrozen pins the historical "bfctl sync"
// per-addon stdout line format byte for byte (decision Q5): launch-script
// parsers see exactly these lines, whatever happens to the progress
// rendering around them.
func TestSyncPerAddonLineFormatFrozen(t *testing.T) {
	forever := client.FlavorForever
	tests := []struct {
		name    string
		r       service.Result
		explain bool
		want    string
	}{
		{"skipped", service.Result{Flavor: forever, AddonID: "Demo", Kind: service.ResultSkipped, Skip: "manual addon"}, false,
			"forever/Demo: skipped: manual addon\n"},
		{"failed", service.Result{Flavor: forever, AddonID: "Demo", Kind: service.ResultFailed, Err: errors.New("boom")}, false,
			"forever/Demo: error: boom\n"},
		{"up to date", service.Result{Flavor: forever, AddonID: "Demo", Kind: service.ResultUpToDate, To: "5.18.4"}, false,
			"forever/Demo: up to date (5.18.4)\n"},
		{"available fresh", service.Result{Flavor: forever, AddonID: "Demo", Kind: service.ResultAvailable, From: "-", To: "5.18.4"}, false,
			"forever/Demo: 5.18.4 available to install\n"},
		{"available update", service.Result{Flavor: forever, AddonID: "Demo", Kind: service.ResultAvailable, From: "5.18.3", To: "5.18.4"}, false,
			"forever/Demo: update available (5.18.3 -> 5.18.4)\n"},
		{"installed fresh", service.Result{Flavor: forever, AddonID: "Demo", Kind: service.ResultUpdated, From: "-", To: "5.18.4"}, false,
			"forever/Demo: installed 5.18.4\n"},
		{"updated", service.Result{Flavor: forever, AddonID: "Demo", Kind: service.ResultUpdated, From: "5.18.3", To: "5.18.4"}, false,
			"forever/Demo: updated 5.18.3 -> 5.18.4\n"},
		{"explain plan", service.Result{Flavor: forever, AddonID: "Demo", Kind: service.ResultUpToDate, To: "5.18.4",
			Plan: resolver.Plan{Strategy: state.StrategyRelease, Ref: "v5.18.4", Reason: "matching release asset"}}, true,
			"forever/Demo: up to date (5.18.4)\n" +
				"    plan: release ref=v5.18.4 reason: matching release asset\n"},
		{"explain suppressed on failure", service.Result{Flavor: forever, AddonID: "Demo", Kind: service.ResultFailed, Err: errors.New("boom"),
			Plan: resolver.Plan{Strategy: state.StrategyRelease, Ref: "v5.18.4", Reason: "matching release asset"}}, true,
			"forever/Demo: error: boom\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := captureStdout(t, func() { printResult(tt.r, tt.explain) })
			if got != tt.want {
				t.Errorf("printResult() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSyncJSONStdoutIsOneCleanJSONDocument pins the "sync --json" purity
// contract (risk R2): stdout is byte for byte a single valid JSON document
// with no trailing data — progress, however much of it streams, stays on
// stderr — and the document keeps the frozen schema of docs/bfctl.1.
func TestSyncJSONStdoutIsOneCleanJSONDocument(t *testing.T) {
	testSyncState(t, brokenAddon(), manualAddon())
	stdout, stderr, code := runSyncCaptured(t, "--json")
	if code != ExitPartial {
		t.Errorf("exit = %d, want %d (one failed unit)", code, ExitPartial)
	}
	want := `{
  "results": [
    {
      "flavor": "forever",
      "addon": "BrokenAddOn",
      "status": "failed",
      "from": "-",
      "error": "addon BrokenAddOn: parse repository \"not a repo\": want owner/repo or a github.com URL"
    },
    {
      "flavor": "forever",
      "addon": "ManualAddOn",
      "status": "skipped",
      "from": "-",
      "error": "installed by a foreign tool (re-bind with \"bfctl adopt --bind\" to manage it)"
    }
  ],
  "summary": {
    "up_to_date": 0,
    "updates_available": 0,
    "updated": 0,
    "manual_skipped": 1,
    "failed": 1
  }
}
`
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout is not one valid JSON document:\n%s", stdout)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var doc map[string]json.RawMessage
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout does not decode as JSON: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Error("stdout carries trailing data after the JSON document")
	}
	if strings.Contains(stdout, "progress:") {
		t.Errorf("progress leaked onto stdout:\n%s", stdout)
	}
	assertNoControlBytes(t, []byte(stdout))
	if !strings.Contains(stderr, "progress: 1/2 forever/BrokenAddOn: failed") ||
		!strings.Contains(stderr, "progress: 2/2 forever/ManualAddOn: skipped") {
		t.Errorf("progress did not stream to stderr:\n%s", stderr)
	}
}

// TestSyncRedirectedOutputStaysPlainAndParseable pins the non-TTY contract
// (decisions Q1/Q4/Q6): with both streams redirected the command emits no
// ANSI or control bytes at all, every stderr line is one parseable progress
// line in the canonical shape, and the frozen result lines land on stdout
// only.
func TestSyncRedirectedOutputStaysPlainAndParseable(t *testing.T) {
	testSyncState(t, brokenAddon(), manualAddon())
	stdout, stderr, code := runSyncCaptured(t)
	if code != ExitPartial {
		t.Errorf("exit = %d, want %d (one failed unit)", code, ExitPartial)
	}
	assertNoControlBytes(t, []byte(stdout))
	assertNoControlBytes(t, []byte(stderr))
	wantStdout := "forever/BrokenAddOn: error: addon BrokenAddOn: parse repository \"not a repo\": want owner/repo or a github.com URL\n" +
		"forever/ManualAddOn: skipped: installed by a foreign tool (re-bind with \"bfctl adopt --bind\" to manage it)\n" +
		"0 up to date, 0 updated, 1 manual skipped, 1 failed\n"
	if stdout != wantStdout {
		t.Errorf("stdout = %q, want %q", stdout, wantStdout)
	}
	wantStderr := "progress: 1/2 forever/BrokenAddOn: failed: addon BrokenAddOn: parse repository \"not a repo\": want owner/repo or a github.com URL\n" +
		"progress: 2/2 forever/ManualAddOn: skipped: installed by a foreign tool (re-bind with \"bfctl adopt --bind\" to manage it)\n"
	if stderr != wantStderr {
		t.Errorf("stderr = %q, want %q", stderr, wantStderr)
	}
	lineRE := regexp.MustCompile(`^progress: \d+/\d+ \S+/\S+: \S+`)
	for _, line := range strings.Split(strings.TrimSuffix(stderr, "\n"), "\n") {
		if !lineRE.MatchString(line) {
			t.Errorf("progress line not parseable: %q", line)
		}
	}
}

// TestSyncQuietShowsNoProgress pins the "--quiet" mode end to end: progress
// is suppressed entirely while the frozen result lines and the summary
// still print unchanged on stdout.
func TestSyncQuietShowsNoProgress(t *testing.T) {
	testSyncState(t, manualAddon())
	stdout, stderr, code := runSyncCaptured(t, "--quiet")
	if code != ExitOK {
		t.Errorf("exit = %d, want %d", code, ExitOK)
	}
	if stderr != "" {
		t.Errorf("quiet run wrote to stderr: %q", stderr)
	}
	want := "forever/ManualAddOn: skipped: installed by a foreign tool (re-bind with \"bfctl adopt --bind\" to manage it)\n" +
		"0 up to date, 0 updated, 1 manual skipped\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestSyncExitCodesFrozen pins the documented exit codes end to end
// (docs/bfctl.1, EXIT STATUS): 0 clean, 1 failed to start, 2 usage,
// 4 partial failure.
func TestSyncExitCodesFrozen(t *testing.T) {
	t.Run("0 clean", func(t *testing.T) {
		testSyncState(t, manualAddon())
		if _, _, code := runSyncCaptured(t); code != ExitOK {
			t.Errorf("exit = %d, want %d", code, ExitOK)
		}
	})
	t.Run("1 state unreadable", func(t *testing.T) {
		testSyncState(t)
		path, err := paths.StateFile()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, code := runSyncCaptured(t); code != ExitError {
			t.Errorf("exit = %d, want %d", code, ExitError)
		}
	})
	t.Run("2 usage", func(t *testing.T) {
		testSyncState(t)
		for _, args := range [][]string{{"stray"}, {"--workers", "0"}, {"--bogus"}} {
			if _, _, code := runSyncCaptured(t, args...); code != ExitUsage {
				t.Errorf("runSync(%q) exit = %d, want %d", args, code, ExitUsage)
			}
		}
	})
	t.Run("4 partial failure", func(t *testing.T) {
		testSyncState(t, brokenAddon())
		if _, _, code := runSyncCaptured(t); code != ExitPartial {
			t.Errorf("exit = %d, want %d", code, ExitPartial)
		}
	})
}

// TestInterruptMidRenderLeavesTerminalClean sends a real SIGINT at a
// renderer that is mid-render behind the real interruptGuard (the "bfctl
// add" / "bfctl adopt" abort path; risk R4). The child is the test binary
// itself, so the guard, the renderer and the signal disposition under test
// are the production ones: the live line must be erased and the cursor
// restored — no dangling ANSI state — and the re-raise must end the process
// exactly as without the guard. "bfctl sync" ends in the same Stop()
// cleanup before its rollback message (pinned by
// TestProgressTTYStopRestoresTerminal).
func TestInterruptMidRenderLeavesTerminalClean(t *testing.T) {
	if os.Getenv("BFCTL_INTERRUPT_CHILD") == "1" {
		runInterruptChild() // renders until the parent's SIGINT kills it
		return
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readyR.Close() }()
	cmd := exec.Command(os.Args[0], "-test.run=^TestInterruptMidRenderLeavesTerminalClean$")
	cmd.Env = append(os.Environ(), "BFCTL_INTERRUPT_CHILD=1")
	cmd.ExtraFiles = []*os.File{readyW} // child fd 3: readiness signal
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = readyW.Close() // the child holds the only write end now

	type childResult struct {
		out []byte
		err error // the reaping error; nil would mean the child exited on its own
	}
	done := make(chan childResult, 1)
	go func() {
		buf := make([]byte, 1)
		if _, err := readyR.Read(buf); err != nil {
			_ = cmd.Process.Kill()
			out, _ := io.ReadAll(stdout)
			_ = cmd.Wait()
			done <- childResult{out, fmt.Errorf("read readiness byte: %w", err)}
			return
		}
		// Let the emit loop redraw a few more times so the SIGINT lands
		// while the live line is on screen.
		time.Sleep(20 * time.Millisecond)
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			_ = cmd.Process.Kill()
			out, _ := io.ReadAll(stdout)
			_ = cmd.Wait()
			done <- childResult{out, fmt.Errorf("send SIGINT: %w", err)}
			return
		}
		out, _ := io.ReadAll(stdout) // EOF when the child dies
		done <- childResult{out, cmd.Wait()}
	}()

	var r childResult
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child survived its SIGINT for 10s")
	}
	if r.err == nil {
		t.Fatal("child exited cleanly; want death by the re-raised SIGINT")
	}
	var ee *exec.ExitError
	if !errors.As(r.err, &ee) || ee.ExitCode() != -1 {
		t.Fatalf("child exit = %v, want termination by signal", r.err)
	}
	if s := cmd.ProcessState.String(); !strings.Contains(s, "signal: interrupt") {
		t.Fatalf("child process state = %q, want signal: interrupt", s)
	}
	out := string(r.out)
	if !strings.Contains(out, "retail/weakauras: installing") {
		t.Errorf("child never rendered the live status line: %q", out)
	}
	if !strings.HasSuffix(out, "\r\x1b[2K\x1b[?25h") {
		t.Errorf("SIGINT left the terminal dirty: want the live line erased and the cursor restored, got %q", out)
	}
}

// runInterruptChild is the child half of TestInterruptMidRenderLeavesTerminalClean:
// live progress behind the real interruptGuard, a steady event stream so
// the SIGINT always lands mid-render, and a readiness byte on fd 3 once the
// guard is installed and the first live line is drawn.
func runInterruptChild() {
	prog := newProgressRenderer(os.Stdout, progressOptions{TTY: true})
	defer prog.Stop()
	defer interruptGuard(prog)()
	ev := progressEvent(service.PhaseInstalling, service.Detail{Version: "5.18.4"})
	prog.Handle(ev)
	ready := os.NewFile(3, "ready")
	_, _ = ready.Write([]byte{1})
	for {
		prog.Handle(ev)
		time.Sleep(2 * time.Millisecond)
	}
}
