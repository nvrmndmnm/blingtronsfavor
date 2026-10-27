package client

import "testing"

// TestFlavorForGameVersion checks the band table against the game version
// strings the cfwidget mirror emits for a multi-flavor release, including
// the version lines no bfctl flavor serves: those must never match, or a
// Wrath file could be installed into a Classic client.
func TestFlavorForGameVersion(t *testing.T) {
	tests := []struct {
		gv   string
		want Flavor
		ok   bool
	}{
		// retail: 12.x today, older retail lines still map to mainline
		{"12.1.5", FlavorMainline, true},
		{"12.0.0", FlavorMainline, true},
		{"7.3.5", FlavorMainline, true},
		// WoW Forever: 1.60.x
		{"1.60.1", FlavorForever, true},
		{"1.60.0", FlavorForever, true},
		// Classic Era: 1.13–1.15
		{"1.15.9", FlavorClassic, true},
		{"1.13.0", FlavorClassic, true},
		// Mists of Pandaria Classic: 5.5.x only — the 5.4.x MoP-era
		// retail lines must not be picked for it
		{"5.5.4", FlavorMists, true},
		{"5.4.2", "", false},
		// lines no flavor serves
		{"4.4.2", "", false},  // Cataclysm Classic
		{"3.4.5", "", false},  // Wrath Classic
		{"3.80.2", "", false}, // unclaimed line
		{"2.5.6", "", false},  // BC Classic
		{"2.0.3", "", false},  // historical retail
		{"1.12.1", "", false}, // pre-1.13 vanilla
		// not game versions at all
		{"", "", false},
		{"garbage", "", false},
		{"1.60", "", false},
	}
	for _, tt := range tests {
		got, ok := FlavorForGameVersion(tt.gv)
		if got != tt.want || ok != tt.ok {
			t.Errorf("FlavorForGameVersion(%q) = %q, %v; want %q, %v", tt.gv, got, ok, tt.want, tt.ok)
		}
	}
}

// TestGameVersionInterface checks the exact-interface parse a provider uses
// as a secondary match: the client's own game version counts as
// flavor-matching even when its line is not in the band table.
func TestGameVersionInterface(t *testing.T) {
	tests := []struct {
		gv   string
		want int
		ok   bool
	}{
		{"1.60.1", 16001, true},
		{"12.1.5", 120105, true},
		{"1.15.9.58123", 11509, true}, // build segment is ignored
		{"garbage", 0, false},
		{"", 0, false},
		{"1.60", 0, false},
	}
	for _, tt := range tests {
		got, ok := GameVersionInterface(tt.gv)
		if got != tt.want || ok != tt.ok {
			t.Errorf("GameVersionInterface(%q) = %d, %v; want %d, %v", tt.gv, got, ok, tt.want, tt.ok)
		}
	}
}
