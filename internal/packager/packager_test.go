package packager

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
)

// requireGit skips the fixture-driven tests when git is not installed: the
// packager needs git for every repository operation.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required for the packager tests: %v", err)
	}
}

// gitRun runs git in dir and returns its trimmed stdout, failing the test on
// any error. Commits carry an explicit identity so the tests do not depend
// on the machine's git configuration.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// commitRepo git-inits dir, commits everything in it and returns the name of
// its branch.
func commitRepo(t *testing.T, dir string, tags ...string) string {
	t.Helper()
	gitRun(t, dir, "init", "--quiet")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "-c", "user.name=bfctl-test", "-c", "user.email=bfctl-test@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture")
	for _, tag := range tags {
		gitRun(t, dir, "tag", tag)
	}
	return gitRun(t, dir, "symbolic-ref", "--short", "HEAD")
}

// initRepo copies testdata/<name> into a fresh directory, applies the file
// overrides (path -> content), commits everything and returns the repository
// path and its branch name.
func initRepo(t *testing.T, name string, files map[string]string, tags ...string) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := copyTreeFiltered(root, filepath.Join("testdata", name), func(string) bool { return false }); err != nil {
		t.Fatalf("copy fixture %s: %v", name, err)
	}
	writeRepoFiles(t, root, files)
	return root, commitRepo(t, root, tags...)
}

// initRepoFiles builds a repository from scratch out of the given
// path -> content map and returns its path and branch name.
func initRepoFiles(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	writeRepoFiles(t, root, files)
	return root, commitRepo(t, root)
}

// writeRepoFiles writes the path -> content map under root.
func writeRepoFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		dst := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatalf("create %s: %v", dst, err)
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", dst, err)
		}
	}
}

// fileURL renders a local path as a file:// URL git can clone.
func fileURL(path string) string {
	return "file://" + filepath.ToSlash(path)
}

// fixture holds the local git repositories the happy-path build runs
// against: the addon repository and the LibStub external it pulls in.
type fixture struct {
	repoURL, repoRef string
	libstubURL       string
}

// newFixture commits testdata/fixture-repo (with the external's URL spliced
// into .pkgmeta) and testdata/external-libstub into local git repositories.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	requireGit(t)
	libstub, _ := initRepo(t, "external-libstub", nil, "v1.0")
	pkgmeta, err := os.ReadFile(filepath.Join("testdata", "fixture-repo", PkgMetaName))
	if err != nil {
		t.Fatalf("read fixture .pkgmeta: %v", err)
	}
	repo, ref := initRepo(t, "fixture-repo", map[string]string{
		PkgMetaName: strings.ReplaceAll(string(pkgmeta), "@LIBSTUB_URL@", fileURL(libstub)),
	})
	return &fixture{repoURL: fileURL(repo), repoRef: ref, libstubURL: fileURL(libstub)}
}

// testOptions returns build options with private caches, out as the compose
// target and a Classic Era client (Interface 11509) as the detected client.
func testOptions(t *testing.T, out string) Options {
	t.Helper()
	return Options{
		OutDir:    out,
		ReposDir:  filepath.Join(t.TempDir(), "repos"),
		Externals: NewExternalCacheAt(filepath.Join(t.TempDir(), "externals")),
		Flavor:    client.FlavorClassic,
		Interface: 11509,
	}
}

// treeEntries lists root as sorted slash-separated paths, directories
// suffixed with "/", failing the test on any .git metadata — the plan's
// "find … -name .git must be empty" check.
func treeEntries(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Name() == ".git" {
			t.Errorf("tree contains .git metadata: %s", rel)
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			rel += "/"
		}
		entries = append(entries, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(entries)
	return entries
}

// readGolden returns the sorted non-empty lines of testdata/<name>.
func readGolden(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	sort.Strings(lines)
	return lines
}

// assertOutEmpty fails the test unless out is empty or was never created:
// a failed build must not leave a partial install behind.
func assertOutEmpty(t *testing.T, out string) {
	t.Helper()
	entries, err := os.ReadDir(out)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("read %s: %v", out, err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	t.Fatalf("build left a partial install in %s: %v", out, names)
}

// TestBuildPackage builds the fixture repository — package-as, one git
// external (LibStub), move-folders and ignore — and compares the composed
// tree against the golden layout.
func TestBuildPackage(t *testing.T) {
	fx := newFixture(t)
	res, err := Build(context.Background(),
		Spec{Owner: "example", Repo: "foo", URL: fx.repoURL, Ref: fx.repoRef},
		testOptions(t, filepath.Join(t.TempDir(), "out")))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := treeEntries(t, res.Root), readGolden(t, "golden-tree.txt"); !slices.Equal(got, want) {
		t.Errorf("tree mismatch\n got: %v\nwant: %v", got, want)
	}
	if !slices.Equal(res.Folders, []string{"Foo", "Foo_Extra"}) {
		t.Errorf("Folders = %v, want [Foo Foo_Extra]", res.Folders)
	}
	if res.Meta == nil || res.Meta.PackageAs != "Foo" {
		t.Errorf("Meta.PackageAs = %v, want Foo", res.Meta)
	}
}

// TestExternalFetchedOnce builds twice through one cache and checks the
// external is fetched exactly once (cache hit counter), producing identical
// trees both times.
func TestExternalFetchedOnce(t *testing.T) {
	fx := newFixture(t)
	cache := NewExternalCacheAt(filepath.Join(t.TempDir(), "externals"))
	base := t.TempDir()
	spec := Spec{Owner: "example", Repo: "foo", URL: fx.repoURL, Ref: fx.repoRef}
	opts := func(name string) Options {
		return Options{
			OutDir:    filepath.Join(base, name),
			ReposDir:  filepath.Join(base, "repos"),
			Externals: cache,
			Flavor:    client.FlavorClassic,
			Interface: 11509,
		}
	}
	first, err := Build(context.Background(), spec, opts("out1"))
	if err != nil {
		t.Fatalf("first Build: %v", err)
	}
	second, err := Build(context.Background(), spec, opts("out2"))
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if stats := cache.Stats(); stats.Fetches != 1 || stats.Hits != 1 {
		t.Errorf("cache stats = %+v, want {Hits:1 Fetches:1} (LibStub fetched exactly once)", stats)
	}
	if !slices.Equal(treeEntries(t, first.Root), treeEntries(t, second.Root)) {
		t.Error("the two builds produced different trees")
	}
}

// TestBuildRejectsMalformedPkgMeta: broken YAML fails with an actionable
// error and no partial install.
func TestBuildRejectsMalformedPkgMeta(t *testing.T) {
	requireGit(t)
	repo, ref := initRepoFiles(t, map[string]string{
		"Foo.toc":  "## Interface: 11509\n",
		"Foo.lua":  "",
		".pkgmeta": "package-as: [this is not valid YAML\n",
	})
	out := filepath.Join(t.TempDir(), "out")
	_, err := Build(context.Background(),
		Spec{Owner: "example", Repo: "foo", URL: fileURL(repo), Ref: ref},
		testOptions(t, out))
	if !errors.Is(err, ErrInvalidPkgMeta) {
		t.Fatalf("Build error = %v, want one wrapping ErrInvalidPkgMeta", err)
	}
	assertOutEmpty(t, out)
}

// TestBuildRejectsExternalWithoutURL: an external without a url fails before
// anything is fetched, with an error naming the external.
func TestBuildRejectsExternalWithoutURL(t *testing.T) {
	requireGit(t)
	repo, ref := initRepoFiles(t, map[string]string{
		"Foo.toc":  "## Interface: 11509\n",
		"Foo.lua":  "",
		".pkgmeta": "externals:\n  libs/LibStub:\n    tag: v1.0\n",
	})
	out := filepath.Join(t.TempDir(), "out")
	_, err := Build(context.Background(),
		Spec{Owner: "example", Repo: "foo", URL: fileURL(repo), Ref: ref},
		testOptions(t, out))
	if !errors.Is(err, ErrInvalidPkgMeta) {
		t.Fatalf("Build error = %v, want one wrapping ErrInvalidPkgMeta", err)
	}
	if !strings.Contains(err.Error(), "url") || !strings.Contains(err.Error(), "libs/LibStub") {
		t.Errorf("Build error = %q, want it to name the missing url of libs/LibStub", err)
	}
	assertOutEmpty(t, out)
}

// TestMoveFoldersCollisions: a destination that collides with the package
// folder or is used twice fails with a hard error and no partial install.
func TestMoveFoldersCollisions(t *testing.T) {
	cases := []struct {
		name    string
		pkgmeta string
	}{
		{
			name:    "destination collides with the package folder",
			pkgmeta: "package-as: Foo\nmove-folders:\n  Foo/modules/Extra: Foo\n",
		},
		{
			name:    "destination used twice",
			pkgmeta: "package-as: Foo\nmove-folders:\n  Foo/modules/Extra: Dup\n  modules/Extra2: Dup\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireGit(t)
			repo, ref := initRepoFiles(t, map[string]string{
				"Foo.toc":              "## Interface: 11509\n",
				"Foo.lua":              "",
				"modules/Extra/x.lua":  "",
				"modules/Extra2/y.lua": "",
				".pkgmeta":             tc.pkgmeta,
			})
			out := filepath.Join(t.TempDir(), "out")
			_, err := Build(context.Background(),
				Spec{Owner: "example", Repo: "foo", URL: fileURL(repo), Ref: ref},
				testOptions(t, out))
			if err == nil || !strings.Contains(err.Error(), "move-folders") {
				t.Fatalf("Build error = %v, want a move-folders collision error", err)
			}
			assertOutEmpty(t, out)
		})
	}
}

// TestIgnoreMatcher covers the gitignore-style matcher semantics: base-name
// globs, anchored paths, directory-only patterns, "**" and negation.
func TestIgnoreMatcher(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		rel      string
		isDir    bool
		want     bool
	}{
		{"base glob matches anywhere", []string{"*.md"}, "docs/guide.md", false, true},
		{"base glob leaves other files", []string{"*.md"}, "docs/guide.txt", false, false},
		{"dir-only matches the dir", []string{"docs/"}, "docs", true, true},
		{"dir-only skips files", []string{"docs/"}, "docs", false, false},
		{"anchored path matches", []string{"modules/Debug.lua"}, "modules/Debug.lua", false, true},
		{"anchored path is not a base match", []string{"modules/Debug.lua"}, "x/modules/Debug.lua", false, false},
		{"double star spans segments", []string{"**/foo"}, "a/b/foo", true, true},
		{"double star inside a pattern", []string{"a/**/b"}, "a/x/y/b", false, true},
		{"negation re-includes", []string{"*.lua", "!keep.lua"}, "keep.lua", false, false},
		{"last match wins", []string{"!keep.lua", "*.lua"}, "keep.lua", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := compileIgnore(tc.patterns)
			if err != nil {
				t.Fatalf("compileIgnore(%v): %v", tc.patterns, err)
			}
			if got := m.match(tc.rel, tc.isDir); got != tc.want {
				t.Errorf("match(%q, isDir=%v) = %v, want %v", tc.rel, tc.isDir, got, tc.want)
			}
		})
	}
}

// TestParsePkgMeta covers the accepted .pkgmeta shapes (the reference's
// shorthand and full external spellings) and the rejected ones.
func TestParsePkgMeta(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		wantErr bool
		wantURL string
	}{
		{
			name:    "full external mapping",
			data:    "externals:\n  Libs/LibStub:\n    url: https://example.invalid/libstub.git\n    tag: v1.0\n",
			wantURL: "https://example.invalid/libstub.git",
		},
		{
			name:    "external url shorthand",
			data:    "externals:\n  Libs/LibStub: https://example.invalid/libstub.git\n",
			wantURL: "https://example.invalid/libstub.git",
		},
		{name: "unknown external key", data: "externals:\n  Libs/LibStub:\n    urll: https://example.invalid/libstub.git\n", wantErr: true},
		{name: "conflicting refs", data: "externals:\n  Libs/LibStub:\n    url: u\n    tag: a\n    branch: b\n", wantErr: true},
		{name: "unsupported external type", data: "externals:\n  Libs/LibStub:\n    url: u\n    type: hg\n", wantErr: true},
		{name: "absolute external destination", data: "externals:\n  /Libs/LibStub:\n    url: u\n", wantErr: true},
		{name: "move-folders destination with slash", data: "move-folders:\n  Foo/a: Foo/b\n", wantErr: true},
		{name: "package-as folder with slash", data: "package-as: Foo/Bar\n", wantErr: true},
		{name: "empty ignore pattern", data: "ignore:\n  - ''\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := ParsePkgMeta([]byte(tc.data), ".pkgmeta")
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidPkgMeta) {
					t.Fatalf("ParsePkgMeta error = %v, want one wrapping ErrInvalidPkgMeta", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePkgMeta: %v", err)
			}
			if got := meta.Externals["Libs/LibStub"].URL; got != tc.wantURL {
				t.Errorf("external URL = %q, want %q", got, tc.wantURL)
			}
		})
	}
}
