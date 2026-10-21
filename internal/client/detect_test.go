package client

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// copyFixture writes testdata/<name> to dest.
func copyFixture(t *testing.T, name, dest string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

type detectCase struct {
	name        string
	buildInfo   string // fixture name; "" means no .build.info
	flavorInfo  string // fixture name; "" means no .flavor.info
	dirName     string
	wantFlavor  Flavor
	wantProduct string
	wantVersion Version
	wantPtr     bool
	wantErr     bool
	wantMissing bool // error must wrap ErrBuildInfoMissing
}

func TestDetect(t *testing.T) {
	cases := []detectCase{
		{
			name:        "mainline",
			buildInfo:   "build.info.mainline",
			flavorInfo:  "flavor.info.mainline",
			dirName:     "_retail_",
			wantFlavor:  FlavorMainline,
			wantProduct: "wow",
			wantVersion: Version{Major: 12, Minor: 1, Patch: 0, Build: 59201},
		},
		{
			name:        "forever picks highest of multiple active rows",
			buildInfo:   "build.info.forever",
			flavorInfo:  "flavor.info.forever",
			dirName:     "_forever_",
			wantFlavor:  FlavorForever,
			wantProduct: "wow_forever",
			wantVersion: Version{Major: 1, Minor: 60, Patch: 1, Build: 58900},
		},
		{
			name:        "classic era five segment version",
			buildInfo:   "build.info.classic",
			flavorInfo:  "flavor.info.classic",
			dirName:     "_classic_era_",
			wantFlavor:  FlavorClassic,
			wantProduct: "wow_classic_era",
			wantVersion: Version{Major: 1, Minor: 15, Patch: 9, Build: 58123},
		},
		{
			name:        "mists three segment version",
			buildInfo:   "build.info.mists",
			flavorInfo:  "flavor.info.mists",
			dirName:     "_mists_",
			wantFlavor:  FlavorMists,
			wantProduct: "wow_mists",
			wantVersion: Version{Major: 5, Minor: 5, Patch: 4},
		},
		{
			name:        "ptr token marks IsPtr",
			buildInfo:   "build.info.ptr",
			flavorInfo:  "flavor.info.ptr",
			dirName:     "_ptr_",
			wantFlavor:  FlavorMainline,
			wantProduct: "wowt",
			wantVersion: Version{Major: 12, Minor: 2, Patch: 0, Build: 60100},
			wantPtr:     true,
		},
		{
			name:        "missing flavor.info falls back to dir name",
			buildInfo:   "build.info.forever",
			dirName:     "_forever_",
			wantFlavor:  FlavorForever,
			wantProduct: "",
			wantVersion: Version{Major: 1, Minor: 60, Patch: 1, Build: 58900},
		},
		{
			name:        "missing build.info errors",
			flavorInfo:  "flavor.info.mainline",
			dirName:     "_retail_",
			wantErr:     true,
			wantMissing: true,
		},
		{
			name:       "malformed build.info errors",
			buildInfo:  "build.info.malformed",
			flavorInfo: "flavor.info.mainline",
			dirName:    "_retail_",
			wantErr:    true,
		},
		{
			name:       "bad version errors",
			buildInfo:  "build.info.badversion",
			flavorInfo: "flavor.info.mainline",
			dirName:    "_retail_",
			wantErr:    true,
		},
		{
			name:       "no matching product row errors",
			buildInfo:  "build.info.classic",
			flavorInfo: "flavor.info.mainline",
			dirName:    "_retail_",
			wantErr:    true,
		},
		{
			name:    "nonexistent path errors",
			dirName: "_missing_",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "World of Warcraft")
			flavorDir := filepath.Join(root, tc.dirName)
			if tc.name != "nonexistent path errors" {
				if err := os.MkdirAll(flavorDir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.buildInfo != "" {
				copyFixture(t, tc.buildInfo, filepath.Join(root, ".build.info"))
			}
			if tc.flavorInfo != "" {
				copyFixture(t, tc.flavorInfo, filepath.Join(flavorDir, ".flavor.info"))
			}

			install, err := Detect(flavorDir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", install)
				}
				if tc.wantMissing && !errors.Is(err, ErrBuildInfoMissing) {
					t.Fatalf("expected ErrBuildInfoMissing, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if install.Flavor != tc.wantFlavor {
				t.Errorf("flavor = %q, want %q", install.Flavor, tc.wantFlavor)
			}
			if install.Product != tc.wantProduct {
				t.Errorf("product = %q, want %q", install.Product, tc.wantProduct)
			}
			if install.Version != tc.wantVersion {
				t.Errorf("version = %+v, want %+v", install.Version, tc.wantVersion)
			}
			if install.Interface != tc.wantVersion.Interface() {
				t.Errorf("interface = %d, want %d", install.Interface, tc.wantVersion.Interface())
			}
			if install.IsPtr != tc.wantPtr {
				t.Errorf("IsPtr = %v, want %v", install.IsPtr, tc.wantPtr)
			}
		})
	}
}

func TestDetectBadge(t *testing.T) {
	root := filepath.Join(t.TempDir(), "World of Warcraft")
	flavorDir := filepath.Join(root, "_forever_")
	if err := os.MkdirAll(flavorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, "build.info.forever", filepath.Join(root, ".build.info"))
	copyFixture(t, "flavor.info.forever", filepath.Join(flavorDir, ".flavor.info"))

	install, err := Detect(flavorDir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	const want = "Forever 1.60.1 (Interface 16001)"
	if got := install.Badge(); got != want {
		t.Errorf("Badge() = %q, want %q", got, want)
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in      string
		want    Version
		wantErr bool
	}{
		{in: "12.1.0.59201", want: Version{12, 1, 0, 59201}},
		{in: "1.60.1", want: Version{1, 60, 1, 0}},
		{in: "1.15.9.58123.2", want: Version{1, 15, 9, 58123}},
		{in: "1.2", wantErr: true},
		{in: "1.x.0", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseVersion(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseVersion(%q): expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseVersion(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseVersion(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestVersionInterface(t *testing.T) {
	cases := []struct {
		version Version
		want    int
	}{
		{Version{12, 1, 0, 59201}, 120100},
		{Version{1, 60, 1, 58900}, 16001},
		{Version{1, 15, 9, 58123}, 11509},
		{Version{5, 5, 4, 0}, 50504},
	}
	for _, tc := range cases {
		if got := tc.version.Interface(); got != tc.want {
			t.Errorf("%s.Interface() = %d, want %d", tc.version, got, tc.want)
		}
	}
}

func TestFlavorFromToken(t *testing.T) {
	cases := []struct {
		token   string
		want    Flavor
		wantPtr bool
	}{
		{"wow", FlavorMainline, false},
		{"wow_forever", FlavorForever, false},
		{"wow_classic_era", FlavorClassic, false},
		{"wow_mists", FlavorMists, false},
		{"_forever_", FlavorForever, false},
		{"_ptr_", FlavorMainline, true},
		{"wowt", FlavorMainline, true},
		{"wowxptr", FlavorMainline, true},
		{"wow_forever_beta", FlavorForever, true},
	}
	for _, tc := range cases {
		got, gotPtr := flavorFromToken(tc.token)
		if got != tc.want || gotPtr != tc.wantPtr {
			t.Errorf("flavorFromToken(%q) = (%q, %v), want (%q, %v)",
				tc.token, got, gotPtr, tc.want, tc.wantPtr)
		}
	}
}

func TestParseFlavorInfo(t *testing.T) {
	if got := parseFlavorInfo("Region=us\nProduct = wow_forever \n"); got != "wow_forever" {
		t.Errorf("parseFlavorInfo = %q, want %q", got, "wow_forever")
	}
	if got := parseFlavorInfo("Region=us\n"); got != "" {
		t.Errorf("parseFlavorInfo = %q, want empty", got)
	}
}
