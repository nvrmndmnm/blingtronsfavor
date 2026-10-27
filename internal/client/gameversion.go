package client

// Game version ↔ flavor mapping for the mirror-scraping providers. The
// cfwidget CurseForge mirror reports, per file, the game version strings the
// file was built for ("12.1.5", "4.4.2", "1.15.9", "1.60.1"); a provider
// must map those strings to the flavor whose client line carries them.

// flavorBands map TOC interface ranges to the flavors whose client line
// carries them (Version.Interface, the "## Interface" number a TOC file
// carries):
//
//	flavor   line                     interface
//	mainline retail 12.x              60000 and up
//	mists    5.5.x (Progression)      50500–50599
//	forever  1.60.x (Forever)         16000–16999
//	classic  1.13–1.15 (Classic Era)  11300–11599
//
// Interface ranges outside every band belong to version lines no bfctl
// flavor serves (BC Classic 2.5.x, Wrath 3.4.x/3.8x, Cataclysm 4.4.x,
// historical retail 2.x–5.x, pre-1.13 vanilla) and map to ok=false: a file
// listing only such versions is never picked for any flavor, and a file
// mixing served and unserved lines still matches the served one. Bands
// cover the client lines bfctl serves today and widen with new client
// lines; GameVersionInterface lets a provider match a file naming exactly
// the client version bfctl runs even before the band table learns its line.
var flavorBands = []struct {
	flavor Flavor
	lo, hi int
}{
	{FlavorClassic, 11300, 11599},   // Classic Era: 1.13–1.15
	{FlavorForever, 16000, 16999},   // WoW Forever: 1.60.x
	{FlavorMists, 50500, 50599},     // Mists of Pandaria Classic: 5.5.x
	{FlavorMainline, 60000, 999999}, // retail: 6.x and later (12.x today)
}

// FlavorForGameVersion maps one game version string to the flavor whose
// client line carries it. ok=false for strings that are not game versions
// and for version lines no bfctl flavor serves.
func FlavorForGameVersion(gv string) (Flavor, bool) {
	v, err := parseVersion(gv)
	if err != nil {
		return "", false
	}
	for _, band := range flavorBands {
		if band.lo <= v.Interface() && v.Interface() <= band.hi {
			return band.flavor, true
		}
	}
	return "", false
}

// GameVersionInterface parses one game version string into its TOC
// interface number ("1.60.1" -> 16001) so a provider can tell that a file
// names exactly the client version bfctl runs, even when that version line
// is not in the flavor band table yet.
func GameVersionInterface(gv string) (int, bool) {
	v, err := parseVersion(gv)
	if err != nil {
		return 0, false
	}
	return v.Interface(), true
}
