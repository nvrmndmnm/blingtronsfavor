// Package client detects a World of Warcraft client install: which flavor it
// is, which product identifier it carries and which interface version its addons
// must target.
package client

import (
	"fmt"
	"strings"
)

// Flavor identifies one supported client flavor.
type Flavor string

const (
	FlavorMainline Flavor = "mainline" // Retail / Mainline
	FlavorForever  Flavor = "forever"  // WoW Forever (Camelot)
	FlavorClassic  Flavor = "classic"  // Classic Era / Vanilla
	FlavorMists    Flavor = "mists"    // Mists / Progression
)

// DisplayName returns the human-readable name used in the init badge.
func (f Flavor) DisplayName() string {
	switch f {
	case FlavorForever:
		return "Forever"
	case FlavorClassic:
		return "Classic Era"
	case FlavorMists:
		return "Mists"
	default:
		return "Retail"
	}
}

// flavorAliases maps the flavor spellings seen in the wild — release.json
// metadata keys, zip-name flavor tags, CLI flags, client directory names —
// to the canonical Flavor. "forever" and "camelot" name the same client
// (WoW Forever, also called Camelot) and are used interchangeably, so every
// spelling of either folds to FlavorForever; the same folding gives the
// other flavors their accepted spellings.
var flavorAliases = map[string]Flavor{
	"mainline":          FlavorMainline,
	"retail":            FlavorMainline,
	"main":              FlavorMainline,
	"classic":           FlavorClassic,
	"vanilla":           FlavorClassic,
	"era":               FlavorClassic,
	"classic_era":       FlavorClassic,
	"mists":             FlavorMists,
	"mop":               FlavorMists,
	"pandaria":          FlavorMists,
	"mists_of_pandaria": FlavorMists,
	"forever":           FlavorForever,
	"camelot":           FlavorForever,
	"wow_forever":       FlavorForever,
	"wow_camelot":       FlavorForever,
}

// FlavorForKey maps one flavor spelling to its canonical Flavor. It is the
// single alias table behind CLI flags, release.json flavor keys and zip-name
// flavor tags, so "camelot" and "forever" are interchangeable everywhere.
func FlavorForKey(s string) (Flavor, bool) {
	f, ok := flavorAliases[normalizeFlavorKey(s)]
	return f, ok
}

// normalizeFlavorKey lowercases s and folds separators so the spellings
// "Classic Era", "classic-era" and "classic_era" share one key.
func normalizeFlavorKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '.', '/':
			return '_'
		}
		return r
	}, s)
	return strings.Trim(s, "_")
}

// Version is a parsed client version such as 12.1.0.59201 or 1.60.1.58900.
type Version struct {
	Major, Minor, Patch, Build int
}

// String renders the dotted 4-segment form.
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d.%d", v.Major, v.Minor, v.Patch, v.Build)
}

// Short renders major.minor.patch.
func (v Version) Short() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Interface computes the TOC interface number shared by every flavor:
// major*10000 + minor*100 + patch (e.g. 1.60.1 -> 16001, 12.1.0 -> 120100).
func (v Version) Interface() int {
	return v.Major*10000 + v.Minor*100 + v.Patch
}

// newerThan reports whether v sorts after other segment by segment.
func (v Version) newerThan(other Version) bool {
	for _, pair := range [][2]int{{v.Major, other.Major}, {v.Minor, other.Minor}, {v.Patch, other.Patch}, {v.Build, other.Build}} {
		if pair[0] != pair[1] {
			return pair[0] > pair[1]
		}
	}
	return false
}

// ClientInstall describes a detected client install.
type ClientInstall struct {
	Root, FlavorDir string
	Flavor          Flavor
	Product         string  // .flavor.info product identifier
	Version         Version // {Major, Minor, Patch, Build int}
	Interface       int     // e.g. 16001
	IsPtr           bool    // PTR/Beta install
}

// Badge renders the line "bfctl init" prints, e.g.
// "Forever 1.60.1 (Interface 16001)".
func (c *ClientInstall) Badge() string {
	return fmt.Sprintf("%s %s (Interface %d)", c.Flavor.DisplayName(), c.Version.Short(), c.Interface)
}
