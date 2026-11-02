package client

import (
	"os"
	"path/filepath"
	"testing"
)

// writeInstall fabricates one detectable flavor directory under root: the
// flavor directory with its .flavor.info, and the root's .build.info from
// the named fixtures.
func writeInstall(t *testing.T, root, dirName, buildInfo, flavorInfo string) string {
	t.Helper()
	flavorDir := filepath.Join(root, dirName)
	if err := os.MkdirAll(flavorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, buildInfo, filepath.Join(root, ".build.info"))
	copyFixture(t, flavorInfo, filepath.Join(flavorDir, ".flavor.info"))
	return flavorDir
}

func TestDetectInstallsFrom(t *testing.T) {
	base := t.TempDir()
	rootA := filepath.Join(base, "World of Warcraft")
	rootB := filepath.Join(base, "other", "World of Warcraft")

	forever := writeInstall(t, rootA, "_forever_", "build.info.forever", "flavor.info.forever")
	classic := writeInstall(t, rootB, "_classic_era_", "build.info.classic", "flavor.info.classic")

	// Decoys the scan must skip: a non-flavor directory under a valid root
	// and a flavor-looking directory whose root carries no .build.info.
	if err := os.MkdirAll(filepath.Join(rootA, "Interface"), 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(filepath.Join(elsewhere, "_retail_"), 0o700); err != nil {
		t.Fatal(err)
	}

	// The duplicate root is deduplicated on the resolved flavor directory.
	roots := []string{rootA, rootB, rootA, elsewhere, filepath.Join(base, "missing")}
	got := detectInstallsFrom(roots)
	if len(got) != 2 {
		t.Fatalf("detected %d installs (%+v), want 2", len(got), got)
	}
	// Sorted by flavor: classic before forever. Detect resolves symlinks,
	// so the expectations resolve the same way.
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	if got[0].FlavorDir != resolve(classic) || got[1].FlavorDir != resolve(forever) {
		t.Fatalf("order = %q, %q, want %q, %q", got[0].FlavorDir, got[1].FlavorDir, resolve(classic), resolve(forever))
	}
	if got[0].Flavor != FlavorClassic || got[1].Flavor != FlavorForever {
		t.Fatalf("flavors = %q, %q, want classic, forever", got[0].Flavor, got[1].Flavor)
	}
}

func TestDetectInstallsFromEmpty(t *testing.T) {
	if got := detectInstallsFrom(nil); len(got) != 0 {
		t.Fatalf("detected %+v from no roots, want none", got)
	}
	if got := detectInstallsFrom([]string{filepath.Join(t.TempDir(), "missing")}); len(got) != 0 {
		t.Fatalf("detected %+v from a missing root, want none", got)
	}
}

func TestCommonRootsAreAbsolute(t *testing.T) {
	for _, root := range commonRoots() {
		if !filepath.IsAbs(root) {
			t.Errorf("common root %q is not absolute", root)
		}
	}
}
