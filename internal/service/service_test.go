package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/state"
)

// testState points bfctl's config and cache at fresh temp directories and
// records one forever client (Forever 1.60.1, interface 16001) with the
// given addon.
func testState(t *testing.T, a *state.AddonState) {
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
	st := state.New()
	st.Clients[client.FlavorForever] = &state.ClientState{
		Flavor:    client.FlavorForever,
		Root:      filepath.Join(t.TempDir(), "_forever_"),
		Interface: 16001,
		Addons:    map[string]*state.AddonState{a.ID: a},
	}
	if err := state.Open(path).Save(st); err != nil {
		t.Fatal(err)
	}
}

func TestReportSummary(t *testing.T) {
	tests := []struct {
		name string
		rep  Report
		want string
	}{
		{"clean", Report{UpToDate: 3}, "3 up to date, 0 updated"},
		{"updated", Report{UpToDate: 1, Updated: 2}, "1 up to date, 2 updated"},
		{"check-only", Report{CheckOnly: true, UpToDate: 1, Updates: 2}, "1 up to date, 2 update(s) available"},
		{"mixed", Report{UpToDate: 1, Updated: 1, Skipped: 1, Failed: 1},
			"1 up to date, 1 updated, 1 manual skipped, 1 failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rep.Summary(); got != tt.want {
				t.Fatalf("Summary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToggleTrackingToStable(t *testing.T) {
	testState(t, &state.AddonState{
		ID:       "DemoAddon",
		Source:   state.SourceRef{Kind: state.SourceGitHub, Slug: "owner/repo"},
		Tracking: "main",
	})
	got, err := ToggleTracking(context.Background(), client.FlavorForever, "DemoAddon")
	if err != nil {
		t.Fatalf("ToggleTracking: %v", err)
	}
	if got != "stable" {
		t.Fatalf("ToggleTracking = %q, want \"stable\"", got)
	}
	st, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if tr := st.Client(client.FlavorForever).Addons["DemoAddon"].Tracking; tr != "stable" {
		t.Fatalf("state tracking = %q, want \"stable\"", tr)
	}
}

func TestToggleTrackingRefusesNonGitHub(t *testing.T) {
	testState(t, &state.AddonState{
		ID:       "DemoAddon",
		Source:   state.SourceRef{Kind: state.SourceGit, URL: "https://example.invalid/repo.git"},
		Tracking: "stable",
	})
	_, err := ToggleTracking(context.Background(), client.FlavorForever, "DemoAddon")
	if err == nil {
		t.Fatal("ToggleTracking on a non-GitHub source: succeeded, want refusal")
	}
}

func TestToggleTrackingUnknownAddon(t *testing.T) {
	testState(t, &state.AddonState{ID: "DemoAddon", Source: state.SourceRef{Kind: state.SourceGitHub, Slug: "owner/repo"}})
	_, err := ToggleTracking(context.Background(), client.FlavorForever, "Nope")
	if err == nil {
		t.Fatal("ToggleTracking on an unknown addon: succeeded, want error")
	}
}

func TestLoadStatusEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	if err := paths.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	s, err := LoadStatus()
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if len(s.Clients) != 0 || len(s.Addons) != 0 {
		t.Fatalf("LoadStatus = %+v, want empty", s)
	}
}
