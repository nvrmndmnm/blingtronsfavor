package toc

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeToc writes a fixture manifest into a temp dir and returns its path.
func writeToc(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		content  string
		want     Toc
	}{
		{
			name:     "full metadata and file entries",
			fileName: "Full_Mainline.toc",
			content: "## Interface: 120100\n" +
				"## Title: Full\n" +
				"## Version: 1.2.3\n" +
				"## X-Curse-Project-ID: 123456\n" +
				"## X-Wago-ID: wago-1\n" +
				"## X-WoWI-ID: 99887\n" +
				"## X-Website: https://example.org/full\n" +
				"## X-Repository: https://github.com/example/full\n" +
				"# a plain comment\n" +
				"\n" +
				"Full.lua\n" +
				"Full.xml\n",
			want: Toc{
				Name:           "Full",
				Suffix:         "_Mainline",
				Interfaces:     []int{120100},
				Title:          "Full",
				Version:        "1.2.3",
				CurseProjectID: "123456",
				WagoID:         "wago-1",
				WoWIID:         "99887",
				Website:        "https://example.org/full",
				Repository:     "https://github.com/example/full",
				Files:          []string{"Full.lua", "Full.xml"},
			},
		},
		{
			name:     "multi-interface line",
			fileName: "Multi.toc",
			content:  "## Interface: 11509 16001 120100\nMulti.lua\n",
			want: Toc{
				Name:       "Multi",
				Interfaces: []int{11509, 16001, 120100},
				Files:      []string{"Multi.lua"},
			},
		},
		{
			name:     "missing Interface line",
			fileName: "Broken.toc",
			content:  "## Title: Broken\nBroken.lua\n",
			want: Toc{
				Name:  "Broken",
				Title: "Broken",
				Files: []string{"Broken.lua"},
			},
		},
		{
			name:     "duplicate Interface lines",
			fileName: "Dup.toc",
			content:  "## Interface: 16001\n## Interface: 16001 120100\nDup.lua\n",
			want: Toc{
				Name:       "Dup",
				Interfaces: []int{16001, 120100},
				Files:      []string{"Dup.lua"},
			},
		},
		{
			name:     "BOM and CRLF",
			fileName: "Bom_Camelot.toc",
			content:  "\xEF\xBB\xBF## Interface: 16001\r\n## Title: Bom\r\nBom.lua\r\n",
			want: Toc{
				Name:       "Bom",
				Suffix:     "_Camelot",
				Interfaces: []int{16001},
				Title:      "Bom",
				Files:      []string{"Bom.lua"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(writeToc(t, tt.fileName, tt.content))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got.Path = ""
			if !reflect.DeepEqual(got, &tt.want) {
				t.Errorf("Parse() = %+v, want %+v", *got, tt.want)
			}
		})
	}
}

func TestSplitName(t *testing.T) {
	tests := []struct {
		base       string
		wantName   string
		wantSuffix string
	}{
		{"Addon_Mainline", "Addon", "_Mainline"},
		{"Addon_Vanilla", "Addon", "_Vanilla"},
		{"Addon_Classic", "Addon", "_Classic"},
		{"Addon_Mists", "Addon", "_Mists"},
		{"Addon_Camelot", "Addon", "_Camelot"},
		{"Addon_Forever", "Addon", "_Forever"},
		{"Addon", "Addon", ""},
	}
	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			name, suffix := splitName(tt.base)
			if name != tt.wantName || suffix != tt.wantSuffix {
				t.Errorf("splitName(%q) = (%q, %q), want (%q, %q)", tt.base, name, suffix, tt.wantName, tt.wantSuffix)
			}
		})
	}
}
