package packager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
)

// goldenSubst is the fixed substitution the golden fixtures were built with.
var goldenSubst = Substitution{
	Version:         "v1.2.3",
	Hash:            "abc1234",
	AbbreviatedHash: "def5678",
	DateISO:         "2026-10-22T12:00:00Z",
}

// readWhole returns the full content of path, failing the test on error.
func readWhole(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestSubstituteKeywordsGolden: every @project-…@ keyword expands to its own
// value in .toc, .lua and .xml files, exactly as the golden fixtures say.
func TestSubstituteKeywordsGolden(t *testing.T) {
	for _, name := range []string{"keywords.toc", "keywords.lua", "keywords.xml"} {
		t.Run(name, func(t *testing.T) {
			got := substituteBytes([]byte(readWhole(t, filepath.Join("testdata", "subst", name))), goldenSubst)
			want := readWhole(t, filepath.Join("testdata", "subst", name+".golden"))
			if string(got) != want {
				t.Errorf("substituteBytes(%s) mismatch\n got: %q\nwant: %q", name, got, want)
			}
		})
	}
}

// TestSubstituteDebugBlocksGolden: a non-debug build strips the
// "#@debug@ … #@end-debug@" block entirely and expands the
// "#@non-debug@ … #@end-non-debug@" block (markers removed, "# " stripped
// from every line in between), matching the golden fixture.
func TestSubstituteDebugBlocksGolden(t *testing.T) {
	got := substituteBytes([]byte(readWhole(t, filepath.Join("testdata", "subst", "debug.lua"))), goldenSubst)
	want := readWhole(t, filepath.Join("testdata", "subst", "debug.lua.golden"))
	if string(got) != want {
		t.Errorf("substituteBytes(debug.lua) mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestSubstituteDebugBlockEdges covers the marker edge cases: stray markers,
// unterminated blocks and comment lines that must not be uncommented.
func TestSubstituteDebugBlockEdges(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "unterminated debug block drops the rest",
			in:   "keep\n#@debug@\ngone\nstill gone",
			want: "keep",
		},
		{
			name: "stray end-debug line is kept",
			in:   "keep\n#@end-debug@\nkeep2",
			want: "keep\n#@end-debug@\nkeep2",
		},
		{
			name: "stray end-non-debug line is removed",
			in:   "keep\n#@end-non-debug@\nkeep2",
			want: "keep\nkeep2",
		},
		{
			name: "uncomment needs a blank after the hash",
			in:   "#@non-debug@\n#nospace\n#  two blanks\n#\n#@end-non-debug@",
			want: "#nospace\ntwo blanks\n#",
		},
		{
			name: "start marker line is removed even when it opens the file",
			in:   "#@non-debug@\n# code\n#@end-non-debug@\nafter",
			want: "code\nafter",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(substituteBytes([]byte(tc.in), Substitution{}))
			if got != tc.want {
				t.Errorf("substituteBytes(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSubstituteTocFlavorSplitGolden: from the same source tree the TOC
// flavor split writes "## Interface: 16001" for a Forever build and
// "## Interface: 120100" for a Retail build, leaving exactly one .toc named
// after the staged folder.
func TestSubstituteTocFlavorSplitGolden(t *testing.T) {
	cases := []struct {
		fixture string
		flavor  client.Flavor
		iface   int
		golden  string
	}{
		{"tocsplit-universal", client.FlavorForever, 16001, "golden-toc-forever.toc"},
		{"tocsplit-universal", client.FlavorMainline, 120100, "golden-toc-retail.toc"},
		{"tocsplit-flavor", client.FlavorForever, 16001, "golden-toc-forever.toc"},
		{"tocsplit-flavor", client.FlavorMainline, 120100, "golden-toc-retail.toc"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture+"/"+tc.flavor.DisplayName(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "Foo")
			if err := copyTreeFiltered(dir, filepath.Join("testdata", tc.fixture), func(string) bool { return false }); err != nil {
				t.Fatalf("copy fixture: %v", err)
			}
			if err := splitTocDir(dir, tc.flavor, tc.iface); err != nil {
				t.Fatalf("splitTocDir: %v", err)
			}
			got := readWhole(t, filepath.Join(dir, "Foo.toc"))
			want := readWhole(t, filepath.Join("testdata", tc.golden))
			if got != want {
				t.Errorf("Foo.toc mismatch\n got: %q\nwant: %q", got, want)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("read %s: %v", dir, err)
			}
			var tocs []string
			for _, entry := range entries {
				if strings.HasSuffix(strings.ToLower(entry.Name()), ".toc") {
					tocs = append(tocs, entry.Name())
				}
			}
			if len(tocs) != 1 || tocs[0] != "Foo.toc" {
				t.Errorf("remaining .toc files = %v, want exactly [Foo.toc]", tocs)
			}
		})
	}
}

// TestSubstituteTocPreservesIntentionalMultiInterface: a multi-interface
// line survives only when the folder ships an intentional flavor suffix set
// (several flavor-specific .toc files) and the line already lists the
// detected interface.
func TestSubstituteTocPreservesIntentionalMultiInterface(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Foo")
	if err := copyTreeFiltered(dir, filepath.Join("testdata", "tocsplit-multiflavor"), func(string) bool { return false }); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	if err := splitTocDir(dir, client.FlavorForever, 16001); err != nil {
		t.Fatalf("splitTocDir: %v", err)
	}
	got := readWhole(t, filepath.Join(dir, "Foo.toc"))
	want := readWhole(t, filepath.Join("testdata", "tocsplit-multiflavor", "Foo_Camelot.toc"))
	if got != want {
		t.Errorf("preserved Foo.toc mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestSubstituteValidationGateRejectsLeftoverTokens: the post-build gate
// refuses a bundle that still carries @project- tokens and a bundle whose
// staged folders lack a matching .toc or the detected interface.
func TestSubstituteValidationGateRejectsLeftoverTokens(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		wantErr string
		wantIs  error
	}{
		{
			name: "leftover @project- token",
			files: map[string]string{
				"Foo/Foo.toc": "## Interface: 16001\n",
				"Foo/Foo.lua": "local v = \"@project-version@\"\n",
			},
			wantErr: "@project-version@",
			wantIs:  ErrIncompleteSubstitution,
		},
		{
			name: "unknown @project- token",
			files: map[string]string{
				"Foo/Foo.toc": "## Interface: 16001\n## Notes: @project-foo@\n",
			},
			wantErr: "@project-foo@",
			wantIs:  ErrIncompleteSubstitution,
		},
		{
			name: "folder without matching toc",
			files: map[string]string{
				"Foo/Foo_Mainline.toc": "## Interface: 120100\n",
			},
			wantErr: "no matching Foo.toc",
		},
		{
			name: "interface mismatch",
			files: map[string]string{
				"Foo/Foo.toc": "## Interface: 120100\n",
			},
			wantErr: "not the detected interface 16001",
		},
		{
			name: "clean bundle passes",
			files: map[string]string{
				"Foo/Foo.toc":   "## Interface: 16001\n",
				"Foo/Foo.lua":   "local v = \"v1.2.3\"\n",
				"Foo/Foo.xml":   "<Ui/>\n",
				"Bar/Bar.toc":   "## Interface: 16001 120100\n",
				"Bar/Bar.lua":   "",
				"Bar/notes.txt": "docs may mention @project-version@\n",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeRepoFiles(t, root, tc.files)
			err := Validate(root, 16001)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate accepted a bundle it should reject (%s)", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate error = %q, want it to mention %q", err, tc.wantErr)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("Validate error = %v, want one wrapping %v", err, tc.wantIs)
			}
		})
	}
}

// TestSubstituteFromGitValues: the keyword values are resolved from the
// built ref — nearest tag for the version, short SHA for the hash and the
// commit date in UTC.
func TestSubstituteFromGitValues(t *testing.T) {
	requireGit(t)
	root, _ := initRepoFiles(t, map[string]string{"Foo.toc": "## Interface: 16001\n"})
	gitRun(t, root, "tag", "v1.2.3")
	s, err := SubstitutionFromGit(context.Background(), root)
	if err != nil {
		t.Fatalf("SubstitutionFromGit: %v", err)
	}
	if s.Version != "v1.2.3" {
		t.Errorf("Version = %q, want v1.2.3", s.Version)
	}
	if s.Hash == "" || s.AbbreviatedHash != s.Hash {
		t.Errorf("Hash = %q, AbbreviatedHash = %q, want one non-empty short SHA", s.Hash, s.AbbreviatedHash)
	}
	if _, err := time.Parse("2006-01-02T15:04:05Z", s.DateISO); err != nil {
		t.Errorf("DateISO = %q, want UTC ISO 8601: %v", s.DateISO, err)
	}
}
