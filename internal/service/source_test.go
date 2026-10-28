package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/github"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/state"
)

// requireGit skips the fixture-driven source build tests when git is not
// installed: the packager needs git for every repository operation.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required for the source build tests: %v", err)
	}
}

// gitRun runs git in dir and returns its trimmed stdout, failing the test on
// any error. Commits carry an explicit identity so the tests do not depend
// on the machine's git configuration.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitRepo git-inits dir, commits everything in it and returns the name of
// its branch.
func commitRepo(t *testing.T, dir string) string {
	t.Helper()
	gitRun(t, dir, "init", "--quiet")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "-c", "user.name=bfctl-test", "-c", "user.email=bfctl-test@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture")
	return gitRun(t, dir, "symbolic-ref", "--short", "HEAD")
}

// TestBuildSourcePlanComposesLocalRepo builds a source-pkgmeta plan from a
// local git repository and checks the staged bundle: one top-level folder
// named after the repository, its .toc rewritten to the detected client's
// interface, and every "@project-" substitution token replaced.
func TestBuildSourcePlanComposesLocalRepo(t *testing.T) {
	requireGit(t)
	testState(t, &state.AddonState{ID: "Demo", DisplayName: "Demo",
		Source: state.SourceRef{Kind: state.SourceGitHub, URL: "https://github.com/acme/Demo", Slug: "acme/Demo"}})

	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"Demo.toc": "## Interface: 120100\n## Title: Demo @project-version@\n",
		"Demo.lua": "-- built from @project-version@\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	branch := commitRepo(t, repo)

	ref, err := resolver.ParseRepo("acme/Demo")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "out")
	if err := buildSourcePlan(context.Background(), resolver.Plan{
		Strategy: state.StrategySourcePkgmeta,
		URL:      "file://" + filepath.ToSlash(repo),
		Ref:      branch,
	}, ref, client.FlavorForever, dest); err != nil {
		t.Fatalf("buildSourcePlan: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dest, "Demo", "Demo.toc"))
	if err != nil {
		t.Fatalf("read staged toc: %v", err)
	}
	if !strings.Contains(string(data), "## Interface: 16001") {
		t.Errorf("staged toc not rewritten to the detected client:\n%s", data)
	}
	if strings.Contains(string(data), "@project-") {
		t.Errorf("staged toc still carries substitution tokens:\n%s", data)
	}
}

// TestInstallProviderProgress drives the provider add path with a
// collector: the unit reports resolve, download, install and its terminal
// completion in order, with the single-unit frame intact.
func TestInstallProviderProgress(t *testing.T) {
	testState(t, &state.AddonState{
		ID:       "SeedAddOn",
		Strategy: state.StrategyManual,
		Source:   state.SourceRef{Kind: state.SourceManual, Slug: "SeedAddOn"},
	})
	var got []ProgressEvent
	up, err := InstallProvider(context.Background(), &fakeProvider{},
		state.SourceRef{Kind: state.SourceWago, Slug: "VBNBxKx5"},
		"WagoAddOn", "wago:VBNBxKx5", client.FlavorForever, false,
		func(ev ProgressEvent) { got = append(got, ev) })
	if err != nil {
		t.Fatalf("InstallProvider: %v", err)
	}
	if up.Version != "2.0.0" {
		t.Errorf("upstream version = %q, want 2.0.0", up.Version)
	}
	want := []Phase{PhaseResolving, PhaseDownloading, PhaseInstalling, PhaseDone}
	if len(got) != len(want) {
		t.Fatalf("got %d events (%+v), want %d", len(got), got, len(want))
	}
	for i, ev := range got {
		if ev.Phase != want[i] {
			t.Errorf("event %d phase = %q, want %q", i, ev.Phase, want[i])
		}
		if ev.Unit != (Unit{Flavor: client.FlavorForever, AddonID: "WagoAddOn"}) || ev.Index != 1 || ev.Total != 1 {
			t.Errorf("event %d frame = %+v, want the unit 1 of 1", i, ev)
		}
	}
	if got[3].Detail.Version != "2.0.0" {
		t.Errorf("done version = %q, want 2.0.0", got[3].Detail.Version)
	}
}

// TestPlanAddOfflineProgress pins the add-flow resolve reporting: with the
// GitHub client offline the unit reports resolving and its terminal
// failure, and the error keeps the "cannot resolve" wording.
func TestPlanAddOfflineProgress(t *testing.T) {
	gh, err := github.New(github.Options{Offline: true, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("github client: %v", err)
	}
	res := &resolver.Resolver{GH: gh}
	ref, err := resolver.ParseRepo("acme/Demo")
	if err != nil {
		t.Fatalf("parse repo: %v", err)
	}
	var got []ProgressEvent
	_, err = PlanAdd(context.Background(), res, client.FlavorForever, ref, "stable", func(ev ProgressEvent) { got = append(got, ev) })
	if err == nil {
		t.Fatal("PlanAdd succeeded offline")
	}
	if !strings.Contains(err.Error(), "cannot resolve acme/Demo") {
		t.Errorf("error = %q, want the cannot-resolve wording", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events (%+v), want resolving + failed", len(got), got)
	}
	if got[0].Phase != PhaseResolving || got[1].Phase != PhaseFailed {
		t.Errorf("phases = %q, %q; want resolving, failed", got[0].Phase, got[1].Phase)
	}
	if got[0].Unit != (Unit{Flavor: client.FlavorForever, AddonID: "Demo"}) {
		t.Errorf("unit = %+v", got[0].Unit)
	}
}

// TestInstallPlanSourceBuildProgress drives the add-flow install of a
// source-pkgmeta plan: the unit reports download, install and its terminal
// completion with the planned version.
func TestInstallPlanSourceBuildProgress(t *testing.T) {
	requireGit(t)
	rec := state.AddonState{
		ID:          "Demo",
		DisplayName: "Demo",
		Source:      state.SourceRef{Kind: state.SourceGitHub, Slug: "acme/Demo"},
		Tracking:    "stable",
	}
	testState(t, &rec)
	repo := filepath.Join(t.TempDir(), "Demo")
	commitRepo(t, repo, map[string]string{
		"Demo.toc": "## Interface: 16001\n## Version: v1.2.3\n",
		"core.lua": "-- body\n",
	})
	plan := resolver.Plan{Strategy: state.StrategySourcePkgmeta, URL: "file://" + repo, Ref: "master"}
	var got []ProgressEvent
	if err := InstallPlan(context.Background(), nil, client.FlavorForever, rec, plan, false, func(ev ProgressEvent) { got = append(got, ev) }); err != nil {
		t.Fatalf("InstallPlan: %v", err)
	}
	want := []Phase{PhaseDownloading, PhaseInstalling, PhaseDone}
	if len(got) != len(want) {
		t.Fatalf("got %d events (%+v), want %d", len(got), got, len(want))
	}
	for i, ev := range got {
		if ev.Phase != want[i] {
			t.Errorf("event %d phase = %q, want %q", i, ev.Phase, want[i])
		}
		if ev.Unit != (Unit{Flavor: client.FlavorForever, AddonID: "Demo"}) || ev.Index != 1 || ev.Total != 1 {
			t.Errorf("event %d frame = %+v, want the unit 1 of 1", i, ev)
		}
	}
	if got[2].Detail.Version != "master" {
		t.Errorf("done version = %q, want the planned ref", got[2].Detail.Version)
	}
}
