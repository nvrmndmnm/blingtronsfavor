package service

import (
	"slices"
	"testing"

	"github.com/itp5b5/blessforge/internal/state"
	"github.com/itp5b5/blessforge/internal/toc"
)

// TestAddonStatusWorstVerdictAndBuiltFor pins addonStatus's contract: the
// worst verdict across the owned directories wins so one broken directory
// cannot hide, and builtFor carries the interface numbers the
// worst-verdict directories' selected .toc files declare — the facts
// behind the TUI's plain-language verdict ("built for 110002 — may be
// outdated").
func TestAddonStatusWorstVerdictAndBuiltFor(t *testing.T) {
	byDir := map[string]toc.Addon{
		"Good":  {Dir: "Good", Toc: &toc.Toc{Interfaces: []int{16001}}, Verdict: toc.VerdictMatch},
		"Multi": {Dir: "Multi", Toc: &toc.Toc{Interfaces: []int{15000, 16001}}, Verdict: toc.VerdictMultiMatch},
		"Old":   {Dir: "Old", Toc: &toc.Toc{Interfaces: []int{110002}}, Verdict: toc.VerdictMismatch},
		"Blank": {Dir: "Blank", Toc: &toc.Toc{}, Verdict: toc.VerdictMismatch},
		"None":  {Dir: "None", Verdict: toc.VerdictNoToc},
	}
	tests := []struct {
		name  string
		dirs  []string
		want  string
		built []int
	}{
		{"no dirs", nil, "-", nil},
		{"match", []string{"Good"}, "Match", []int{16001}},
		{"worst wins", []string{"Good", "Old"}, "Mismatch", []int{110002}},
		{"multi match lists every interface", []string{"Multi"}, "MultiMatch", []int{15000, 16001}},
		{"mismatch without interface", []string{"Blank"}, "Mismatch", nil},
		{"no toc", []string{"None"}, "NoToc", nil},
		{"missing dir", []string{"Gone"}, "Missing", nil},
		{"missing hides nothing", []string{"Good", "Gone"}, "Missing", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, built := addonStatus(&state.AddonState{OwnedDirs: tt.dirs}, byDir)
			if status != tt.want {
				t.Errorf("status = %q, want %q", status, tt.want)
			}
			if !slices.Equal(built, tt.built) {
				t.Errorf("builtFor = %v, want %v", built, tt.built)
			}
		})
	}
}
