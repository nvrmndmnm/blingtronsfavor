package adopt

// KnownAddon is one curated mapping-table entry: an addon's published
// project IDs and folder names mapped to its canonical GitHub slug. The
// table (plan risk R6) is deliberately small and local — no remote dataset —
// and "bfctl adopt --bind folder=owner/repo" is the manual override for
// anything missing here.
//
// The ID columns (Curse, Wago) are filled in as they are verified against a
// real addon's TOC metadata: a wrong project ID can mis-bind a *different*
// addon, an empty one only costs a fallback to the name keys, so unverified
// IDs stay empty rather than guessed. Name matching (the TOC base name or
// addon directory name) is the reliable key until an ID is recorded. The
// Curse IDs below were verified against the cfwidget mirror's project id
// and title (they key SourceCfwidget bindings); Recount's stays empty —
// no mirror project matches it.
type KnownAddon struct {
	Slug  string   // canonical "owner/repo" GitHub slug
	Name  string   // display name for the binding table
	Curse string   // "## X-Curse-Project-ID", when verified
	Wago  string   // "## X-Wago-ID", when verified
	Names []string // addon folder / TOC base names this entry covers
}

// KnownAddons is the curated mapping table. Entries cover the popular
// addons whose GitHub home is unambiguous; extend it as new IDs are
// verified. Family entries list every folder name the addon ships (the
// multi-folder families adopt groups into one binding).
var KnownAddons = []KnownAddon{
	{
		Slug:  "DeadlyBossMods/DeadlyBossMods",
		Name:  "Deadly Boss Mods",
		Curse: "3358",
		Names: []string{"DBM-Core", "DBM-Core-Classic", "DBM-Core-WoTLK"},
	},
	{
		Slug:  "BigWigsMods/BigWigs",
		Name:  "BigWigs",
		Curse: "2382",
		Names: []string{"BigWigs", "BigWigs_Core", "BigWigs_Options", "BigWigs_Plugins"},
	},
	{
		Slug:  "WeakAuras/WeakAuras2",
		Name:  "WeakAuras",
		Curse: "65387",
		Names: []string{"WeakAuras", "WeakAuras2"},
	},
	{
		Slug:  "Tercioo/Details-Damage-Meter",
		Name:  "Details!",
		Curse: "61284",
		Names: []string{"Details", "Details_Compare2", "Details_DataStorage", "Details_EncounterDetails",
			"Details_RaidCheck", "Details_Streamer", "Details_TinyThreat", "Details_Vanguard"},
	},
	{
		Slug:  "Tercioo/Plater-Nameplates",
		Name:  "Plater",
		Curse: "100547",
		Names: []string{"Plater"},
	},
	{
		Slug:  "Nevcairiel/Bartender4",
		Name:  "Bartender4",
		Curse: "13501",
		Names: []string{"Bartender4"},
	},
	{
		Slug:  "Nevcairiel/TomTom",
		Name:  "TomTom",
		Curse: "18808",
		Names: []string{"TomTom"},
	},
	{
		Slug:  "Questie/Questie",
		Name:  "Questie",
		Curse: "334372",
		Names: []string{"Questie"},
	},
	{
		Slug:  "SFX-WoW/Masque",
		Name:  "Masque",
		Curse: "13592",
		Names: []string{"Masque"},
	},
	{
		Slug:  "Shadowed/ShadowedUnitFrames",
		Name:  "Shadowed Unit Frames",
		Curse: "19268",
		Names: []string{"ShadowedUnitFrames", "ShadowedUnitFrames_Options"},
	},
	{
		Slug:  "AdiAddons/AdiBags",
		Name:  "AdiBags",
		Curse: "23350",
		Names: []string{"AdiBags", "AdiBags_Config"},
	},
	{
		Slug:  "Resike/Recount",
		Name:  "Recount",
		Names: []string{"Recount", "Recount_Options"},
	},
}

// lookupKnown returns the curated entry for an addon, matching the
// extracted TOC identifiers first (Curse project ID, Wago ID) and the addon
// names second. The table is scanned linearly: it is small by design.
func lookupKnown(table []KnownAddon, curseID, wagoID, name string) (KnownAddon, bool) {
	if curseID != "" {
		for _, k := range table {
			if k.Curse != "" && k.Curse == curseID {
				return k, true
			}
		}
	}
	if wagoID != "" {
		for _, k := range table {
			if k.Wago != "" && k.Wago == wagoID {
				return k, true
			}
		}
	}
	if name != "" {
		for _, k := range table {
			for _, n := range k.Names {
				if n == name {
					return k, true
				}
			}
		}
	}
	return KnownAddon{}, false
}
