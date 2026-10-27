package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itp5b5/blessforge/internal/client"
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
