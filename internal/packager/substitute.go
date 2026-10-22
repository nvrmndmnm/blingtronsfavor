package packager

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Substitution holds the values the "@project-…@" keywords expand to. It is
// resolved from the built ref with SubstitutionFromGit and applied to every
// .toc, .lua and .xml file of the package (plan R3: substitution applies to
// all three, following BigWigsMods/packager).
type Substitution struct {
	// Version replaces "@project-version@": the nearest tag
	// ("git describe --tags --always").
	Version string
	// Hash replaces "@project-hash@": the abbreviated commit SHA.
	Hash string
	// AbbreviatedHash replaces "@project-abbreviated-hash@". Same value as
	// Hash: bfctl substitutes the short SHA for both spellings.
	AbbreviatedHash string
	// DateISO replaces "@project-date-iso@": the commit date in UTC,
	// "2006-01-02T15:04:05Z" (the packager's format).
	DateISO string
}

// substExtensions are the file extensions keyword substitution and debug
// block stripping apply to.
var substExtensions = map[string]bool{
	".toc": true,
	".lua": true,
	".xml": true,
}

// SubstitutionFromGit resolves the keyword values from the git repository at
// dir: the nearest tag (falling back to the short SHA on untagged builds),
// the short commit SHA and the commit date in UTC.
func SubstitutionFromGit(ctx context.Context, dir string) (Substitution, error) {
	version, err := runGit(ctx, dir, "describe", "--tags", "--always")
	if err != nil {
		return Substitution{}, fmt.Errorf("resolve @project-version@: %w", err)
	}
	hash, err := runGit(ctx, dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return Substitution{}, fmt.Errorf("resolve @project-hash@: %w", err)
	}
	stamp, err := runGit(ctx, dir, "show", "-s", "--format=%ct", "HEAD")
	if err != nil {
		return Substitution{}, fmt.Errorf("resolve @project-date-iso@: %w", err)
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(string(stamp)), 10, 64)
	if err != nil {
		return Substitution{}, fmt.Errorf("resolve @project-date-iso@: commit timestamp %q: %w", strings.TrimSpace(string(stamp)), err)
	}
	short := strings.TrimSpace(string(hash))
	return Substitution{
		Version:         strings.TrimSpace(string(version)),
		Hash:            short,
		AbbreviatedHash: short,
		DateISO:         time.Unix(secs, 0).UTC().Format("2006-01-02T15:04:05Z"),
	}, nil
}

// substituteTree applies substituteFile to every .toc, .lua and .xml file
// under root.
func substituteTree(root string, s Substitution) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !substExtensions[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		return substituteFile(p, s)
	})
}

// substituteFile rewrites one file in place: the "@project-…@" keywords are
// replaced and the debug blocks are processed for a non-debug (release)
// build, mirroring BigWigsMods/packager's toc_filter with the debug blocks
// removed and the non-debug blocks expanded.
func substituteFile(path string, s Substitution) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out := substituteBytes(data, s)
	if string(out) == string(data) {
		return nil
	}
	return os.WriteFile(path, out, info.Mode().Perm())
}

// substituteBytes runs one file's content through the non-debug build
// transformations:
//
//  1. "#@debug@ … #@end-debug@" blocks are stripped entirely (markers and
//     content), so debug code never ships;
//  2. "#@non-debug@ … #@end-non-debug@" markers are removed and each line in
//     between is uncommented ("# " at the start of the line), so release code
//     ships active;
//  3. the @project-…@ keywords are replaced with their values.
//
// The line structure is preserved: blocks are removed line by line and the
// remaining lines keep their original endings.
func substituteBytes(data []byte, s Substitution) []byte {
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	inDebug, inNonDebug := false, false
	for _, line := range lines {
		// "#@debug@ … #@end-debug@": the whole block is deleted, markers
		// included. A start and end marker on one line still opens the
		// block, matching the reference's sed range semantics.
		if inDebug {
			inDebug = !strings.Contains(line, "#@end-debug@")
			continue
		}
		if strings.Contains(line, "#@debug@") {
			inDebug = true
			continue
		}

		// "#@non-debug@ … #@end-non-debug@": the marker lines are deleted
		// and each block line is uncommented ("# " → ""). The range starts
		// at the line carrying the start marker and ends at the line
		// carrying the end marker, so both markers are removed as well.
		if inNonDebug {
			if strings.Contains(line, "#@end-non-debug@") {
				inNonDebug = false
				continue
			}
			out = append(out, uncomment(line))
			continue
		}
		if strings.Contains(line, "#@non-debug@") {
			inNonDebug = true
			continue
		}
		if strings.Contains(line, "#@end-non-debug@") {
			// A stray end marker outside a block is still a marker line and
			// is removed, like the reference does.
			continue
		}
		out = append(out, line)
	}

	replaced := strings.NewReplacer(
		"@project-version@", s.Version,
		"@project-abbreviated-hash@", s.AbbreviatedHash,
		"@project-hash@", s.Hash,
		"@project-date-iso@", s.DateISO,
	).Replace(strings.Join(out, "\n"))
	return []byte(replaced)
}

// uncomment strips the "# " comment prefix from the start of one non-debug
// block line: "# " followed by one or more blanks loses the "#" and all the
// blanks right after it. A line that is not commented that way is returned
// unchanged.
func uncomment(line string) string {
	if !strings.HasPrefix(line, "#") {
		return line
	}
	rest := line[1:]
	stripped := strings.TrimLeft(rest, " \t")
	if stripped == rest {
		return line
	}
	return stripped
}
