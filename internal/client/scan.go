package client

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
)

// DetectInstalls scans the well-known World of Warcraft install roots for
// flavor directories and reports every install Detect validates. It feeds
// the TUI's guided-init picker ("show detected WoW roots/flavors"): the
// scan only proposes candidates, Detect is what decides — the same
// detection "bfctl init --path" runs on the picked path. Locations the
// scan cannot guess are covered by the picker's manual path entry.
func DetectInstalls() []ClientInstall {
	return detectInstallsFrom(commonRoots())
}

// detectInstallsFrom scans the given install roots: every "_"-prefixed
// subdirectory of a root is a flavor-directory candidate (the Battle.net
// naming, e.g. "_forever_" or "_ptr_"), validated through Detect. The
// result is deduplicated on the resolved flavor directory and sorted by
// flavor then path, so the picker's order is deterministic.
func detectInstallsFrom(roots []string) []ClientInstall {
	seen := make(map[string]bool)
	var out []ClientInstall
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			if !strings.HasPrefix(entry.Name(), "_") {
				continue
			}
			install, err := Detect(filepath.Join(root, entry.Name()))
			if err != nil {
				continue
			}
			if seen[install.FlavorDir] {
				continue
			}
			seen[install.FlavorDir] = true
			out = append(out, *install)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Flavor != out[j].Flavor {
			return out[i].Flavor < out[j].Flavor
		}
		return out[i].FlavorDir < out[j].FlavorDir
	})
	return out
}

// commonRoots returns the World of Warcraft install roots worth scanning
// on the running system: the Battle.net default per platform plus the
// common Wine/Steam layouts on Linux. A root that does not exist simply
// contributes nothing.
func commonRoots() []string {
	var roots []string
	add := func(ps ...string) {
		for _, p := range ps {
			if p != "" {
				roots = append(roots, p)
			}
		}
	}
	home, _ := os.UserHomeDir()
	homeRoots := func(elems ...string) []string {
		if home == "" {
			return nil
		}
		return []string{filepath.Join(append([]string{home}, elems...)...)}
	}
	switch goruntime.GOOS {
	case "windows":
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
			if base := os.Getenv(env); base != "" {
				add(filepath.Join(base, "World of Warcraft"))
			}
		}
	case "darwin":
		add("/Applications/World of Warcraft")
	}
	add(homeRoots("Games", "World of Warcraft")...)
	add(homeRoots("Games", "world-of-warcraft")...)
	add(homeRoots(".local", "share", "steam", "steamapps", "common", "World of Warcraft")...)
	add(homeRoots(".steam", "steamapps", "common", "World of Warcraft")...)
	return roots
}
