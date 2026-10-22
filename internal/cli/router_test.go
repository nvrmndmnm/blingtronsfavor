package cli

import (
	"flag"
	"testing"
)

// TestInterleave keeps flags and positionals freely mixable on the command
// line: Go's flag package alone would stop parsing at the first positional.
func TestInterleave(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantPos     []string
		wantBranch  string
		wantVerbose bool
	}{
		{name: "flag after positional",
			args:        []string{"owner/repo", "-verbose"},
			wantPos:     []string{"owner/repo"},
			wantVerbose: true},
		{name: "value flag after positional",
			args:       []string{"owner/repo", "-branch", "dev"},
			wantPos:    []string{"owner/repo"},
			wantBranch: "dev"},
		{name: "value flag with equals",
			args:       []string{"owner/repo", "-branch=dev"},
			wantPos:    []string{"owner/repo"},
			wantBranch: "dev"},
		{name: "flags before positional",
			args:       []string{"-branch", "dev", "owner/repo"},
			wantPos:    []string{"owner/repo"},
			wantBranch: "dev"},
		{name: "double dash ends flags",
			args:    []string{"--", "-owner/repo"},
			wantPos: []string{"-owner/repo"}},
		{name: "multiple positionals keep order",
			args:        []string{"first", "-verbose", "second"},
			wantPos:     []string{"first", "second"},
			wantVerbose: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			branch := fs.String("branch", "", "branch")
			verbose := fs.Bool("verbose", false, "verbose")
			if err := fs.Parse(interleave(fs, tc.args)); err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := fs.Args()
			if len(got) != len(tc.wantPos) {
				t.Fatalf("positionals = %q, want %q", got, tc.wantPos)
			}
			for i := range got {
				if got[i] != tc.wantPos[i] {
					t.Fatalf("positionals = %q, want %q", got, tc.wantPos)
				}
			}
			if *branch != tc.wantBranch {
				t.Errorf("branch = %q, want %q", *branch, tc.wantBranch)
			}
			if *verbose != tc.wantVerbose {
				t.Errorf("verbose = %v, want %v", *verbose, tc.wantVerbose)
			}
		})
	}
}
