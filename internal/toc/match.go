package toc

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/itp5b5/blessforge/internal/client"
)

// suffixesByFlavor maps every client flavor to its accepted flavor-specific
// toc suffixes in precedence order. A plain "Name.toc" is accepted for every
// flavor as a fallback; see Select.
var suffixesByFlavor = map[client.Flavor][]string{
	client.FlavorMainline: {"_Mainline"},
	client.FlavorClassic:  {"_Vanilla", "_Classic"},
	client.FlavorMists:    {"_Mists"},
	client.FlavorForever:  {"_Camelot", "_Forever"},
}

// Verdict is the compatibility verdict of one addon directory against a
// detected client.
type Verdict int

// Verdict values consumed by "bfctl status":
//
//	Match      the selected toc targets exactly the detected interface
//	MultiMatch the selected toc is multi-version and lists the detected
//	           interface among its Interface values
//	Mismatch   a toc exists but carries the wrong or no interface
//	NoToc      the addon directory contains no .toc at all
//	Unknown    no client target was available, so nothing was compared
const (
	VerdictMatch Verdict = iota
	VerdictMultiMatch
	VerdictMismatch
	VerdictNoToc
	VerdictUnknown
)

// String renders the verdict the way bfctl status prints it.
func (v Verdict) String() string {
	switch v {
	case VerdictMatch:
		return "Match"
	case VerdictMultiMatch:
		return "MultiMatch"
	case VerdictMismatch:
		return "Mismatch"
	case VerdictNoToc:
		return "NoToc"
	default:
		return "Unknown"
	}
}

// Target is the client side of a compatibility check.
type Target struct {
	Flavor    client.Flavor
	Interface int
}

// Addon is one scanned addon directory: its name, the selected .toc (nil when
// the directory ships none) and the compatibility verdict.
type Addon struct {
	Dir     string
	Toc     *Toc
	Verdict Verdict
}

// Scan walks the addon directories under dir and reports for each of them the
// selected .toc and its verdict against target. A nil target only lists the
// selection and reports VerdictUnknown. Directories are returned sorted by
// name.
func Scan(dir string, target *Target) ([]Addon, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var addons []Addon
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		flavor := client.Flavor("")
		if target != nil {
			flavor = target.Flavor
		}
		selected, err := Select(filepath.Join(dir, entry.Name()), flavor)
		if err != nil {
			return nil, err
		}
		addons = append(addons, Addon{Dir: entry.Name(), Toc: selected, Verdict: verdictFor(selected, target)})
	}
	sort.Slice(addons, func(i, j int) bool { return addons[i].Dir < addons[j].Dir })
	return addons, nil
}

// Select picks the .toc for a single addon directory with the precedence the
// spec requires: a flavor-specific "Name_<Suffix>.toc" beats the plain
// "Name.toc", which in turn beats a foreign-flavor toc. Within one flavor the
// suffix order of suffixesByFlavor breaks ties, then a toc whose base name
// matches the directory name, then alphabetical order.
func Select(dir string, flavor client.Flavor) (*Toc, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	dirName := filepath.Base(dir)
	type candidate struct {
		toc        *Toc
		rank       int // 0 flavor-specific, 1 plain, 2 foreign
		suffixRank int // precedence within the flavor's suffix list
		nameMatch  bool
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".toc") {
			continue
		}
		parsed, err := Parse(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		rank, suffixRank := 2, slices.Index(knownSuffixes, parsed.Suffix)
		switch parsed.Suffix {
		case "":
			rank, suffixRank = 1, len(knownSuffixes)
		default:
			if i := slices.Index(suffixesByFlavor[flavor], parsed.Suffix); i >= 0 {
				rank, suffixRank = 0, i
			}
		}
		candidates = append(candidates, candidate{
			toc:        parsed,
			rank:       rank,
			suffixRank: suffixRank,
			nameMatch:  strings.EqualFold(parsed.Name, dirName),
		})
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.suffixRank != b.suffixRank {
			return a.suffixRank < b.suffixRank
		}
		if a.nameMatch != b.nameMatch {
			return a.nameMatch
		}
		return a.toc.Name < b.toc.Name
	})
	return candidates[0].toc, nil
}

// verdictFor computes the compatibility verdict of a selected toc against a
// client target. A nil target yields Unknown — nothing can be compared, even
// when the directory ships no toc; a nil toc against a known target is NoToc.
func verdictFor(selected *Toc, target *Target) Verdict {
	if target == nil {
		return VerdictUnknown
	}
	if selected == nil {
		return VerdictNoToc
	}
	if len(selected.Interfaces) == 0 {
		return VerdictMismatch
	}
	if !slices.Contains(selected.Interfaces, target.Interface) {
		return VerdictMismatch
	}
	if len(selected.Interfaces) == 1 {
		return VerdictMatch
	}
	return VerdictMultiMatch
}
