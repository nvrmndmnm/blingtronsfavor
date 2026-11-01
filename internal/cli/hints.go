package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/itp5b5/blessforge/internal/github"
	"github.com/itp5b5/blessforge/internal/state"
)

// The "errors teach" pass (UX proposal B5): every user-facing error ends
// with an actionable next step. Validation errors carry their next step in
// the message itself ("no client configured: run \"bfctl init\" first");
// runtime errors that arrive wrapped from the service, state or GitHub
// layers get one attached at the report site (fail, withHint). The frozen
// channels (R6) — the per-addon result lines, the "sync --json" error
// fields and the pipe progress lines — keep the raw error text; the sync
// run note carries the teaching step for rate limiting instead.

// hintRateLimited is the next step for a GitHub rate-limit error: an
// authenticated token raises the API quota from 60 to 5000 req/h.
const hintRateLimited = `export GH_TOKEN=<token> (or GITHUB_TOKEN) for 5000 req/h, then rerun`

// hintStateFile is the next step for state-file trouble. The failing file
// is named in the error message itself ("read state <path>: ..."), so the
// hint can point at "the file named above" without repeating the path.
const hintStateFile = `fix or delete the state file named above ("bfctl init" recreates it)`

// hintHome is the next step when a path under the user's home directory
// cannot be resolved at all.
const hintHome = `check that HOME points at your home directory`

// hintedError appends a next step to an error's message. It unwraps to the
// original error, so errors.Is/As classification keeps working across it.
type hintedError struct {
	err  error
	hint string
}

func (e *hintedError) Error() string { return e.err.Error() + " — " + e.hint }

func (e *hintedError) Unwrap() error { return e.err }

// withHint returns err with an actionable next step appended to its
// message. The hint becomes part of Error() but never obscures the wrapped
// error.
func withHint(err error, hint string) error {
	return &hintedError{err: err, hint: hint}
}

// nextStepFor derives the actionable next step for a runtime error: the
// cross-layer classes every command can meet — GitHub rate limiting, a
// state file another process holds, filesystem permissions — get their
// specific step; anything else falls back to asking for detail, which is
// always true for a runtime failure.
func nextStepFor(err error) string {
	switch {
	case github.IsRateLimited(err):
		return hintRateLimited
	case errors.Is(err, state.ErrLocked):
		return "wait for the other bfctl process to finish, then rerun"
	case errors.Is(err, os.ErrPermission):
		return "check the permissions of the file named above, then rerun"
	}
	return "rerun with --verbose for details"
}

// fail reports one runtime error for subcommand cmd on stderr and returns
// code. The message ends with an actionable next step: one err already
// carries (through withHint), otherwise one is derived from the error
// class. Validation errors keep their inline next steps and are printed
// directly instead.
func fail(cmd string, err error, code int) int {
	fmt.Fprintf(os.Stderr, "bfctl %s: %v\n", cmd, ensureHint(err))
	return code
}

// failHint is fail with a caller-chosen next step: the reporting command
// often knows the fix better than the error class does (the scanned
// directory, the client root to re-record).
func failHint(cmd string, err error, code int, hint string) int {
	return fail(cmd, withHint(err, hint), code)
}

// ensureHint attaches the derived next step unless the error already
// carries one.
func ensureHint(err error) error {
	var he *hintedError
	if errors.As(err, &he) {
		return err
	}
	return withHint(err, nextStepFor(err))
}
