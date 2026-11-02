package toc

import (
	"path/filepath"
	"strings"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
)

// TouchesFlavor reports whether path names a .toc manifest that serves
// flavor: a flavor-specific toc ("Name_Forever.toc") or a plain "Name.toc",
// which the spec accepts for every flavor. Foreign-flavor tocs never touch a
// flavor's target toc. The path may be repo-relative with directories; only
// the base name is examined. Used by the staleness detector (M4) to decide
// whether a commit delta touched the target .toc.
func TouchesFlavor(path string, flavor client.Flavor) bool {
	base := filepath.Base(path)
	if !strings.HasSuffix(strings.ToLower(base), ".toc") {
		return false
	}
	_, suffix := splitName(strings.TrimSuffix(base, filepath.Ext(base)))
	if suffix == "" {
		return true // plain Name.toc serves every flavor
	}
	for _, want := range suffixesByFlavor[flavor] {
		if strings.EqualFold(suffix, want) {
			return true
		}
	}
	return false
}
