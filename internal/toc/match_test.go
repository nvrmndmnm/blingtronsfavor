package toc

import (
	"path/filepath"
	"testing"

	"github.com/itp5b5/blessforge/internal/client"
)

func TestSelectAndVerdict(t *testing.T) {
	tests := []struct {
		name        string
		dir         string
		target      Target
		wantToc     string // expected selected manifest, "" for none
		wantVerdict Verdict
	}{
		{
			name:        "forever suffix matches",
			dir:         "ForeverAddon",
			target:      Target{Flavor: client.FlavorForever, Interface: 16001},
			wantToc:     "ForeverAddon_Forever.toc",
			wantVerdict: VerdictMatch,
		},
		{
			name:        "camelot suffix accepted for forever",
			dir:         "CamelotAddon",
			target:      Target{Flavor: client.FlavorForever, Interface: 16001},
			wantToc:     "CamelotAddon_Camelot.toc",
			wantVerdict: VerdictMatch,
		},
		{
			name:        "vanilla suffix for classic",
			dir:         "ClassicAddon",
			target:      Target{Flavor: client.FlavorClassic, Interface: 11509},
			wantToc:     "ClassicAddon_Vanilla.toc",
			wantVerdict: VerdictMatch,
		},
		{
			name:        "mists suffix",
			dir:         "MistsAddon",
			target:      Target{Flavor: client.FlavorMists, Interface: 50504},
			wantToc:     "MistsAddon_Mists.toc",
			wantVerdict: VerdictMatch,
		},
		{
			name:        "mainline suffix",
			dir:         "MainlineAddon",
			target:      Target{Flavor: client.FlavorMainline, Interface: 120100},
			wantToc:     "MainlineAddon_Mainline.toc",
			wantVerdict: VerdictMatch,
		},
		{
			name:        "flavor-specific beats plain",
			dir:         "PrecedenceAddon",
			target:      Target{Flavor: client.FlavorForever, Interface: 16001},
			wantToc:     "PrecedenceAddon_Forever.toc",
			wantVerdict: VerdictMatch,
		},
		{
			name:        "vanilla beats classic suffix",
			dir:         "ClassicDualAddon",
			target:      Target{Flavor: client.FlavorClassic, Interface: 11509},
			wantToc:     "ClassicDualAddon_Vanilla.toc",
			wantVerdict: VerdictMatch,
		},
		{
			name:        "multi-version toc counts as multi match",
			dir:         "MultiAddon",
			target:      Target{Flavor: client.FlavorForever, Interface: 16001},
			wantToc:     "MultiAddon.toc",
			wantVerdict: VerdictMultiMatch,
		},
		{
			name:        "missing Interface line is mismatch",
			dir:         "BrokenAddon",
			target:      Target{Flavor: client.FlavorForever, Interface: 16001},
			wantToc:     "BrokenAddon.toc",
			wantVerdict: VerdictMismatch,
		},
		{
			name:        "foreign flavor toc is mismatch",
			dir:         "MainlineAddon",
			target:      Target{Flavor: client.FlavorForever, Interface: 16001},
			wantToc:     "MainlineAddon_Mainline.toc",
			wantVerdict: VerdictMismatch,
		},
		{
			name:        "wrong interface is mismatch",
			dir:         "ForeverAddon",
			target:      Target{Flavor: client.FlavorForever, Interface: 16002},
			wantToc:     "ForeverAddon_Forever.toc",
			wantVerdict: VerdictMismatch,
		},
		{
			name:        "no toc",
			dir:         "NoTocAddon",
			target:      Target{Flavor: client.FlavorForever, Interface: 16001},
			wantToc:     "",
			wantVerdict: VerdictNoToc,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected, err := Select(filepath.Join("testdata", "addons", tt.dir), tt.target.Flavor)
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			gotToc := ""
			if selected != nil {
				gotToc = filepath.Base(selected.Path)
			}
			if gotToc != tt.wantToc {
				t.Errorf("selected toc = %q, want %q", gotToc, tt.wantToc)
			}
			if got := verdictFor(selected, &tt.target); got != tt.wantVerdict {
				t.Errorf("verdict = %v, want %v", got, tt.wantVerdict)
			}
		})
	}
}

func TestScan(t *testing.T) {
	target := &Target{Flavor: client.FlavorForever, Interface: 16001}
	addons, err := Scan(filepath.Join("testdata", "addons"), target)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	wantOrder := []string{
		"BrokenAddon",
		"CamelotAddon", "ClassicAddon", "ClassicDualAddon", "ForeverAddon",
		"MainlineAddon", "MistsAddon", "MultiAddon", "NoTocAddon", "PrecedenceAddon",
	}
	if len(addons) != len(wantOrder) {
		t.Fatalf("Scan returned %d addons, want %d", len(addons), len(wantOrder))
	}
	wantVerdicts := map[string]Verdict{
		"BrokenAddon":   VerdictMismatch,
		"CamelotAddon":  VerdictMatch,
		"ForeverAddon":  VerdictMatch,
		"MultiAddon":    VerdictMultiMatch,
		"MainlineAddon": VerdictMismatch,
		"NoTocAddon":    VerdictNoToc,
	}
	for i, addon := range addons {
		if addon.Dir != wantOrder[i] {
			t.Errorf("addons[%d] = %q, want %q", i, addon.Dir, wantOrder[i])
		}
		if want, ok := wantVerdicts[addon.Dir]; ok && addon.Verdict != want {
			t.Errorf("verdict for %s = %v, want %v", addon.Dir, addon.Verdict, want)
		}
	}
}

func TestScanWithoutTarget(t *testing.T) {
	addons, err := Scan(filepath.Join("testdata", "addons"), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, addon := range addons {
		if addon.Verdict != VerdictUnknown {
			t.Errorf("verdict for %s = %v, want %v", addon.Dir, addon.Verdict, VerdictUnknown)
		}
	}
}

func TestVerdictString(t *testing.T) {
	tests := []struct {
		v    Verdict
		want string
	}{
		{VerdictMatch, "Match"},
		{VerdictMultiMatch, "MultiMatch"},
		{VerdictMismatch, "Mismatch"},
		{VerdictNoToc, "NoToc"},
		{VerdictUnknown, "Unknown"},
	}
	for _, tt := range tests {
		if got := tt.v.String(); got != tt.want {
			t.Errorf("Verdict(%d).String() = %q, want %q", tt.v, got, tt.want)
		}
	}
}
