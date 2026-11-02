package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itp5b5/blessforge/internal/github"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/state"
)

// TestNextStepFor pins the derived next steps of the "errors teach" pass:
// every cross-layer error class teaches its specific fix and anything else
// points at the detail flag.
func TestNextStepFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"rate limited", &github.RateLimitError{RetryAfter: time.Minute}, hintRateLimited},
		{"wrapped rate limited", fmt.Errorf("download: %w", &github.RateLimitError{}), hintRateLimited},
		{"state locked", fmt.Errorf("save: %w", state.ErrLocked), "wait for the other bfctl process"},
		{"permission", fmt.Errorf("open: %w", os.ErrPermission), "check the permissions"},
		{"unclassified", errors.New("boom"), "--verbose"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextStepFor(tt.err)
			if !strings.Contains(got, tt.want) {
				t.Errorf("nextStepFor(%v) = %q, want it to contain %q", tt.err, got, tt.want)
			}
		})
	}
}

// TestFailEndsWithNextStep pins the runtime error shape: the message ends
// with the actionable next step, derived from the error class.
func TestFailEndsWithNextStep(t *testing.T) {
	var code int
	_, stderr := captureStreams(t, func() {
		code = fail("add", errors.New("boom"), ExitError)
	})
	if code != ExitError {
		t.Errorf("fail() = %d, want %d", code, ExitError)
	}
	want := "bfctl add: boom — rerun with --verbose for details\n"
	if stderr != want {
		t.Errorf("fail() stderr = %q, want %q", stderr, want)
	}
}

// TestFailKeepsExplicitHint pins that fail never stacks a second next step
// onto an error that already carries one.
func TestFailKeepsExplicitHint(t *testing.T) {
	err := withHint(errors.New("boom"), `fix the thing`)
	_, stderr := captureStreams(t, func() {
		_ = fail("status", err, ExitError)
	})
	want := "bfctl status: boom — fix the thing\n"
	if stderr != want {
		t.Errorf("fail() stderr = %q, want %q", stderr, want)
	}
}

// TestWithHintKeepsErrorClass pins that the hint wrapper unwraps to the
// original error, so errors.Is/As classification survives the message.
func TestWithHintKeepsErrorClass(t *testing.T) {
	base := fmt.Errorf("open state: %w", os.ErrPermission)
	err := withHint(base, hintStateFile)
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("errors.Is(withHint(%v), os.ErrPermission) = false, want true", base)
	}
	if !strings.HasSuffix(err.Error(), hintStateFile) {
		t.Errorf("hinted error = %q, want it to end with %q", err.Error(), hintStateFile)
	}
}

// TestLoadStateErrorsTeach pins that a state read failure carries the
// state-file next step wherever loadState's error surfaces.
func TestLoadStateErrorsTeach(t *testing.T) {
	testSyncState(t)
	path, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadState(); err == nil {
		t.Fatal("loadState() = nil error, want a corrupt-state error")
	} else if !strings.Contains(err.Error(), hintStateFile) {
		t.Errorf("loadState() error = %q, want it to end with %q", err.Error(), hintStateFile)
	}
}

// TestParseSubcommandFlagErrorEndsWithNextStep pins the trailing next step
// for the one error message the CLI does not author itself: the flag
// package's parse error is closed with the fix.
func TestParseSubcommandFlagErrorEndsWithNextStep(t *testing.T) {
	var code int
	_, stderr := captureStreams(t, func() {
		fs := subcommandFlagSet("sync")
		code, _ = parseSubcommand(fs, []string{"--bogus"})
	})
	if code != ExitUsage {
		t.Errorf("parseSubcommand() = %d, want %d", code, ExitUsage)
	}
	want := `next: fix the flags above and rerun ("bfctl sync --help" lists them)` + "\n"
	if !strings.HasSuffix(stderr, want) {
		t.Errorf("parseSubcommand() stderr = %q, want it to end with %q", stderr, want)
	}
}
