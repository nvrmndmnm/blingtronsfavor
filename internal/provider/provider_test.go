package provider

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
)

// requireGit skips the git-backed tests when git is not installed.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required for the git-direct provider tests: %v", err)
	}
}

// gitRun runs git in dir and returns its trimmed stdout, failing the test on
// any error. Commits carry an explicit identity so the tests do not depend
// on the machine's git configuration.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String())
}

// commitFiles writes the path -> content map under root, commits it and tags
// the commit with every given tag. It returns the repository path.
func commitFiles(t *testing.T, files map[string]string, tags ...string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	for path, content := range files {
		dst := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatalf("create %s: %v", dst, err)
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", dst, err)
		}
	}
	gitRun(t, root, "init", "--quiet")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "-c", "user.name=bfctl-test", "-c", "user.email=bfctl-test@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture")
	for _, tag := range tags {
		gitRun(t, root, "tag", tag)
	}
	return root
}

// fileURL renders a local path as a file:// URL git can clone.
func fileURL(path string) string {
	return "file://" + filepath.ToSlash(path)
}

// makeZip builds an in-memory zip archive from a path -> content map.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for path, content := range files {
		w, err := zw.Create(path)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", path, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", path, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// TestFor checks the provider registry: every implemented kind maps to its
// provider and the kinds internal/resolver or the user handle elsewhere are
// explicit errors.
func TestFor(t *testing.T) {
	cases := []struct {
		kind state.SourceKind
		want string
	}{
		{state.SourceTukui, "*provider.Tukui"},
		{state.SourceWoWI, "*provider.WoWI"},
		{state.SourceWago, "*provider.Wago"},
		{state.SourceGit, "*provider.GitDirect"},
	}
	for _, c := range cases {
		p, err := For(c.kind, Options{})
		if err != nil {
			t.Fatalf("For(%s): %v", c.kind, err)
		}
		if got := fmt.Sprintf("%T", p); got != c.want {
			t.Errorf("For(%s) = %s, want %s", c.kind, got, c.want)
		}
	}
	for _, kind := range []state.SourceKind{state.SourceGitHub, state.SourceManual, state.SourceKind("bogus")} {
		if _, err := For(kind, Options{}); err == nil {
			t.Errorf("For(%s) succeeded, want an error", kind)
		}
	}
}

// TestTukuiCheck exercises the Tukui package lookup for the standalone UIs
// and catalog addons, plus the failure modes the provider must surface.
func TestTukuiCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("ui") == "elvui":
			_, _ = w.Write([]byte(`{"name":"ElvUI","version":"13.82","changelog":"https://www.tukui.org/ui/elvui/changelog","download":"https://www.tukui.org/downloads/elvui-13.82.zip"}`))
		case r.URL.Query().Get("addons") == "42":
			_, _ = w.Write([]byte(`{"name":"Addon 42","version":"2.0","web_url":"https://www.tukui.org/addons.php?id=42","download":"https://www.tukui.org/addons.php?id=42&download"}`))
		case r.URL.Query().Get("addons") == "broken":
			_, _ = w.Write([]byte(`{"name":"Broken"}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := &Tukui{API: srv.URL, HTTP: srv.Client()}
	up, err := p.Check(context.Background(), state.SourceRef{Kind: state.SourceTukui, Slug: "elvui"})
	if err != nil {
		t.Fatalf("check elvui: %v", err)
	}
	if up.Version != "13.82" || up.URL != "https://www.tukui.org/downloads/elvui-13.82.zip" {
		t.Errorf("check elvui = %+v, want version 13.82 and the package URL", up)
	}
	if up.Changelog != "https://www.tukui.org/ui/elvui/changelog" {
		t.Errorf("changelog = %q", up.Changelog)
	}
	if up.Strategy != state.StrategyDirect {
		t.Errorf("strategy = %q, want %q", up.Strategy, state.StrategyDirect)
	}

	up, err = p.Check(context.Background(), state.SourceRef{Kind: state.SourceTukui, Slug: "42"})
	if err != nil {
		t.Fatalf("check addon 42: %v", err)
	}
	if up.Version != "2.0" || up.Changelog != "https://www.tukui.org/addons.php?id=42" {
		t.Errorf("check addon 42 = %+v", up)
	}

	if _, err := p.Check(context.Background(), state.SourceRef{Kind: state.SourceTukui, Slug: "broken"}); err == nil {
		t.Error("check broken succeeded, want missing metadata error")
	}
	if _, err := p.Check(context.Background(), state.SourceRef{Kind: state.SourceTukui, Slug: "missing"}); err == nil {
		t.Error("check missing succeeded, want HTTP error")
	}
	if _, err := p.Check(context.Background(), state.SourceRef{Kind: state.SourceTukui}); err == nil {
		t.Error("check without slug succeeded, want error")
	}
}

// TestTukuiFetch downloads and unpacks a Tukui package, stripping the
// single wrapper directory.
func TestTukuiFetch(t *testing.T) {
	pkg := makeZip(t, map[string]string{
		"ElvUI-13.82/ElvUI/ElvUI.toc": "## Interface: 120100\n",
		"ElvUI-13.82/ElvUI/ElvUI.lua": "-- fixture\n",
	})
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php":
			_, _ = w.Write([]byte(`{"name":"ElvUI","version":"13.82","download":"` + srv.URL + `/elvui.zip"}`))
		case "/elvui.zip":
			_, _ = w.Write(pkg)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dest := t.TempDir()
	p := &Tukui{API: srv.URL + "/api.php", HTTP: srv.Client()}
	if err := p.Fetch(context.Background(), state.SourceRef{Kind: state.SourceTukui, Slug: "elvui"}, dest); err != nil {
		t.Fatalf("fetch elvui: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "ElvUI", "ElvUI.toc")); err != nil {
		t.Errorf("unpacked payload missing ElvUI/ElvUI.toc: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "ElvUI-13.82")); !os.IsNotExist(err) {
		t.Error("wrapper directory was not stripped")
	}
}

// TestWoWICheck exercises the WoWI lookup keyed by the TOC X-WoWI-ID,
// including the authentication restriction the provider fails soft on.
func TestWoWICheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/addons/12345.json":
			_, _ = w.Write([]byte(`{"title":"Some Addon","UIVersion":"3.2.1","download":"https://cdn.wowinterface.com/12345/someaddon.zip"}`))
		case "/addons/99.json":
			http.Error(w, "auth required", http.StatusUnauthorized)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := &WoWI{API: srv.URL + "/addons", HTTP: srv.Client()}
	up, err := p.Check(context.Background(), state.SourceRef{Kind: state.SourceWoWI, Slug: "12345"})
	if err != nil {
		t.Fatalf("check 12345: %v", err)
	}
	if up.Version != "3.2.1" || up.URL != "https://cdn.wowinterface.com/12345/someaddon.zip" {
		t.Errorf("check 12345 = %+v", up)
	}
	if want := "https://www.wowinterface.com/downloads/info12345.html"; up.Changelog != want {
		t.Errorf("changelog = %q, want %q", up.Changelog, want)
	}

	_, err = p.Check(context.Background(), state.SourceRef{Kind: state.SourceWoWI, Slug: "99"})
	if err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Errorf("check 99 = %v, want an authentication hint", err)
	}
	if _, err := p.Check(context.Background(), state.SourceRef{Kind: state.SourceWoWI}); err == nil {
		t.Error("check without ID succeeded, want error")
	}
}

// TestWoWIFetch downloads and unpacks a WoWI package.
func TestWoWIFetch(t *testing.T) {
	pkg := makeZip(t, map[string]string{
		"SomeAddon/SomeAddon.toc": "## Interface: 120100\n",
		"SomeAddon/Core.lua":      "-- fixture\n",
	})
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/addons/12345.json":
			_, _ = w.Write([]byte(`{"title":"Some Addon","UIVersion":"3.2.1","download":"` + srv.URL + `/pkg.zip"}`))
		case "/pkg.zip":
			_, _ = w.Write(pkg)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dest := t.TempDir()
	p := &WoWI{API: srv.URL + "/addons", HTTP: srv.Client()}
	if err := p.Fetch(context.Background(), state.SourceRef{Kind: state.SourceWoWI, Slug: "12345"}, dest); err != nil {
		t.Fatalf("fetch 12345: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "SomeAddon", "SomeAddon.toc")); err != nil {
		t.Errorf("unpacked payload missing SomeAddon/SomeAddon.toc: %v", err)
	}
}

// TestGitDirectCheckTags checks that an untagged fallback never beats a
// version tag and that the newest stable tag wins over prereleases.
func TestGitDirectCheckTags(t *testing.T) {
	requireGit(t)
	repo := commitFiles(t, map[string]string{"README.md": "fixture\n"}, "v1.2.3", "v1.10.0", "v2.0.0-rc1", "nightly")
	g := &GitDirect{}
	up, err := g.Check(context.Background(), state.SourceRef{Kind: state.SourceGit, URL: fileURL(repo)})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if up.Version != "v1.10.0" || up.Ref != "v1.10.0" {
		t.Errorf("check = %+v, want v1.10.0", up)
	}
	if up.Strategy != state.StrategyDirect {
		t.Errorf("strategy = %q, want %q", up.Strategy, state.StrategyDirect)
	}
}

// TestGitDirectCheckUntagged falls back to the branch HEAD at its short
// commit when the repository has no version tags.
func TestGitDirectCheckUntagged(t *testing.T) {
	requireGit(t)
	repo := commitFiles(t, map[string]string{"README.md": "fixture\n"})
	branch := gitRun(t, repo, "symbolic-ref", "--short", "HEAD")
	sha := gitRun(t, repo, "rev-parse", "HEAD")

	g := &GitDirect{}
	up, err := g.Check(context.Background(), state.SourceRef{Kind: state.SourceGit, URL: fileURL(repo)})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if up.Ref != branch {
		t.Errorf("ref = %q, want %q", up.Ref, branch)
	}
	if up.Version != sha[:8] {
		t.Errorf("version = %q, want %q", up.Version, sha[:8])
	}
}

// TestGitDirectFetchBuildsPackage runs the full packager pipeline over a
// local repository and checks the staged bundle lands in dest.
func TestGitDirectFetchBuildsPackage(t *testing.T) {
	requireGit(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	repo := commitFiles(t, map[string]string{
		"Foo.toc":  "## Interface: 11509\nFoo.lua\n",
		"Foo.lua":  "-- fixture\n",
		".pkgmeta": "package-as: Foo\n",
	}, "v0.1.0")

	dest := filepath.Join(t.TempDir(), "out")
	g := &GitDirect{Flavor: client.FlavorClassic, Interface: 11509}
	if err := g.Fetch(context.Background(), state.SourceRef{Kind: state.SourceGit, URL: fileURL(repo)}, dest); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	tocs, err := filepath.Glob(filepath.Join(dest, "Foo", "*.toc"))
	if err != nil {
		t.Fatalf("glob staged toc: %v", err)
	}
	if len(tocs) == 0 {
		t.Fatalf("no staged .toc under %s", filepath.Join(dest, "Foo"))
	}
	data, err := os.ReadFile(tocs[0])
	if err != nil {
		t.Fatalf("read staged toc: %v", err)
	}
	if !strings.Contains(string(data), "11509") {
		t.Errorf("staged toc = %q, want the detected interface 11509", data)
	}
}

// TestGitDirectCheckErrors covers the argument and remote failures.
func TestGitDirectCheckErrors(t *testing.T) {
	g := &GitDirect{}
	if _, err := g.Check(context.Background(), state.SourceRef{Kind: state.SourceGit}); err == nil {
		t.Error("check without URL succeeded, want error")
	}
	if err := g.Fetch(context.Background(), state.SourceRef{Kind: state.SourceGit, URL: "file:///nonexistent/repo"}, t.TempDir()); err == nil {
		t.Error("fetch of a missing remote succeeded, want error")
	}
}

// TestRepoIdentity checks the cache identity derived from remote URLs.
func TestRepoIdentity(t *testing.T) {
	cases := []struct{ url, owner, repo string }{
		{"https://github.com/owner/repo", "owner", "repo"},
		{"https://github.com/owner/repo.git", "owner", "repo"},
		{"https://gitlab.com/group/sub/repo", "sub", "repo"},
		{"file:///srv/git/addon.git", "git", "addon"},
	}
	for _, c := range cases {
		owner, repo := repoIdentity(c.url)
		if owner != c.owner || repo != c.repo {
			t.Errorf("repoIdentity(%q) = (%q, %q), want (%q, %q)", c.url, owner, repo, c.owner, c.repo)
		}
	}
}
