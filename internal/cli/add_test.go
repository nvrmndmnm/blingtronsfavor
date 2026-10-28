package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/provider"
	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
)

// TestParseWagoRef pins the accepted Wago reference shapes: the addon page
// URL — with query strings, trailing slashes and deep links stripped — and
// the direct "wago:<key>" short form. Anything else stays with the GitHub
// or git-remote paths.
func TestParseWagoRef(t *testing.T) {
	cases := map[string]string{
		"https://addons.wago.io/addons/eqol":                  "eqol",
		"https://addons.wago.io/addons/eqol/":                 "eqol",
		"https://addons.wago.io/addons/eqol?stability=stable": "eqol",
		"https://addons.wago.io/addons/eqol/versions":         "eqol",
		"https://addons.wago.io/addons/eqol/versions#latest":  "eqol",
		"https://www.addons.wago.io/addons/VBNBxKx5":          "VBNBxKx5",
		"HTTPS://ADDONS.WAGO.IO/ADDONS/Eqol":                  "Eqol",
		"wago:eqol":                                           "eqol",
		"wago:VBNBxKx5":                                       "VBNBxKx5",
		"wago:12345":                                          "12345",
		"  wago:eqol  ":                                       "eqol",
	}
	for raw, want := range cases {
		got, ok := parseWagoRef(raw)
		if !ok || got != want {
			t.Errorf("parseWagoRef(%q) = %q, %v; want %q, true", raw, got, ok, want)
		}
	}
	rejects := []string{
		"", "wago:", "wago:/", "owner/repo",
		"https://github.com/owner/repo", "https://gitlab.com/group/addon.git",
		"https://example.com/addons/eqol", "https://wago.io/eqol",
		"https://addons.wago.io/", "https://addons.wago.io/addons/",
		"https://addons.wago.io/addon/eqol", "https://addons.wago.io/addons",
	}
	for _, raw := range rejects {
		if got, ok := parseWagoRef(raw); ok {
			t.Errorf("parseWagoRef(%q) = %q, true; want a rejection", raw, got)
		}
	}
}

// TestParseCfRef pins the accepted CurseForge reference shapes: the addon
// page URL — with query strings, trailing slashes and deep links stripped —
// and the direct "cfwidget:<key>" short form (slug or numeric project ID).
// Anything else stays with the GitHub or git-remote paths.
func TestParseCfRef(t *testing.T) {
	cases := map[string]string{
		"https://www.curseforge.com/wow/addons/auctionator":               "auctionator",
		"https://www.curseforge.com/wow/addons/auctionator/":              "auctionator",
		"https://www.curseforge.com/wow/addons/auctionator/files":         "auctionator",
		"https://www.curseforge.com/wow/addons/auctionator/files/9202791": "auctionator",
		"https://www.curseforge.com/wow/addons/details?class=warrior":     "details",
		"https://curseforge.com/wow/addons/Details":                       "Details",
		"HTTPS://WWW.CURSEFORGE.COM/WOW/ADDONS/Auctionator":               "Auctionator",
		"cfwidget:61284":       "61284",
		"cfwidget:auctionator": "auctionator",
		"  cfwidget:61284  ":   "61284",
	}
	for raw, want := range cases {
		got, ok := parseCfRef(raw)
		if !ok || got != want {
			t.Errorf("parseCfRef(%q) = %q, %v; want %q, true", raw, got, ok, want)
		}
	}
	rejects := []string{
		"", "cfwidget:", "cfwidget:/", "owner/repo",
		"https://github.com/owner/repo", "https://gitlab.com/group/addon.git",
		"https://www.curseforge.com/wow/addons/", "https://www.curseforge.com/wow/addons",
		"https://www.curseforge.com/wow/addon/auctionator",
		"https://www.curseforge.com/minecraft/mc-mods/jei",
		"https://example.com/wow/addons/auctionator",
		"https://addons.wago.io/addons/eqol",
	}
	for _, raw := range rejects {
		if got, ok := parseCfRef(raw); ok {
			t.Errorf("parseCfRef(%q) = %q, true; want a rejection", raw, got)
		}
	}
}

// TestAddWagoRejectsGitFlags pins the flag contract: --branch and --strategy
// only apply to GitHub repositories, so Wago references reject them before
// touching state or the network.
func TestAddWagoRejectsGitFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--branch", "main", "wago:eqol"},
		{"--strategy", "release", "https://addons.wago.io/addons/eqol"},
	} {
		if code := runAdd(args, BuildInfo{Version: "test"}); code != ExitUsage {
			t.Errorf("runAdd(%v) = %d, want %d", args, code, ExitUsage)
		}
	}
}

// TestAddCfwidgetRejectsGitFlags pins the same flag contract for CurseForge
// references: --branch and --strategy only apply to GitHub repositories.
func TestAddCfwidgetRejectsGitFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--branch", "main", "cfwidget:61284"},
		{"--strategy", "release", "https://www.curseforge.com/wow/addons/details"},
	} {
		if code := runAdd(args, BuildInfo{Version: "test"}); code != ExitUsage {
			t.Errorf("runAdd(%v) = %d, want %d", args, code, ExitUsage)
		}
	}
}

// wagoFixtureAt reads a recorded Wago data-page fixture (shared with the
// provider tests) and substitutes the {{DOWNLOAD}} placeholder with the
// package URL, attribute-escaped the way the Inertia app embeds its payload.
func wagoFixtureAt(t *testing.T, name, download string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "provider", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return strings.ReplaceAll(string(raw), "{{DOWNLOAD}}", html.EscapeString(download))
}

// makeZip builds an in-memory zip archive from a path -> content map.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for path, content := range files {
		w, err := zw.Create(path)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", path, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", path, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// TestAddWagoInstallsAndRecords drives the Wago add path end to end against
// the recorded data-page fixture: the page URL parses to the provider key,
// the real Wago provider scrapes a stub site, and the atomic installer
// records the addon bound to the wago source — so sync keeps maintaining it
// through Wago.
func TestAddWagoInstallsAndRecords(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	if err := paths.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	st := state.New()
	st.Clients[client.FlavorMainline] = &state.ClientState{
		Flavor:    client.FlavorMainline,
		Root:      root,
		Interface: 120100,
	}
	statePath, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Open(statePath).Save(st); err != nil {
		t.Fatal(err)
	}

	pkg := makeZip(t, map[string]string{"eqol/eqol.toc": "## Interface: 120100\n"})
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/versions"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(wagoFixtureAt(t, "wago-versions.html", srv.URL+"/pkg.zip")))
		default:
			_, _ = w.Write(pkg)
		}
	}))
	t.Cleanup(srv.Close)

	key, ok := parseWagoRef("https://addons.wago.io/addons/eqol")
	if !ok {
		t.Fatal("parseWagoRef rejected the addon page URL")
	}
	prov := &provider.Wago{Base: srv.URL, HTTP: srv.Client(), Flavor: client.FlavorMainline}
	ref := state.SourceRef{Kind: state.SourceWago, Slug: key}
	up, err := service.InstallProvider(context.Background(), prov, ref, key, "wago:"+key, client.FlavorMainline, false, nil)
	if err != nil {
		t.Fatalf("InstallProvider: %v", err)
	}
	if up.Version != "5.20.0" {
		t.Errorf("upstream version = %q, want 5.20.0", up.Version)
	}

	got, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	a := got.Client(client.FlavorMainline).Addons["eqol"]
	if a == nil {
		t.Fatal("state lost the addon entry")
	}
	if a.Source.Kind != state.SourceWago || a.Source.Slug != "eqol" {
		t.Errorf("source = %+v, want the wago source recorded for later sync", a.Source)
	}
	if a.Strategy != state.StrategyDirect {
		t.Errorf("strategy = %q, want %q", a.Strategy, state.StrategyDirect)
	}
	if a.InstalledVersion != "5.20.0" || a.ETag != "5.20.0" {
		t.Errorf("installed/etag = %q/%q, want 5.20.0", a.InstalledVersion, a.ETag)
	}
	if !strings.Contains(a.Changelog, "## 5.20.0") {
		t.Errorf("changelog = %q, want the inline changelog of the installed release", a.Changelog)
	}
	if _, err := os.Stat(filepath.Join(root, "Interface", "AddOns", "eqol", "eqol.toc")); err != nil {
		t.Errorf("addon not installed: %v", err)
	}
}

// cfwidgetFixtureAt reads a recorded cfwidget payload fixture (shared with
// the provider tests).
func cfwidgetFixtureAt(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "provider", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(raw)
}

// TestAddCfwidgetInstallsAndRecords drives the CurseForge add path end to
// end against the recorded cfwidget payload fixture: the addon page URL
// parses to the mirror key, the real cfwidget provider resolves a stub
// mirror and stub CDN, and the atomic installer records the addon bound to
// the cfwidget source — so sync keeps maintaining it through the mirror.
func TestAddCfwidgetInstallsAndRecords(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	if err := paths.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	st := state.New()
	st.Clients[client.FlavorMainline] = &state.ClientState{
		Flavor:    client.FlavorMainline,
		Root:      root,
		Interface: 120100,
	}
	statePath, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Open(statePath).Save(st); err != nil {
		t.Fatal(err)
	}

	pkg := makeZip(t, map[string]string{"Details/Details.toc": "## Interface: 120100\n"})
	var cdnPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/files/"):
			cdnPath.Store(r.URL.Path)
			_, _ = w.Write(pkg)
		default:
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(cfwidgetFixtureAt(t, "cfwidget-details.json")))
		}
	}))
	t.Cleanup(srv.Close)

	key, ok := parseCfRef("https://www.curseforge.com/wow/addons/details")
	if !ok {
		t.Fatal("parseCfRef rejected the addon page URL")
	}
	prov := &provider.Cfwidget{Base: srv.URL, CDN: srv.URL, Flavor: client.FlavorMainline, Interface: 120100}
	ref := state.SourceRef{Kind: state.SourceCfwidget, Slug: key}
	up, err := service.InstallProvider(context.Background(), prov, ref, key, "cfwidget:"+key, client.FlavorMainline, false, nil)
	if err != nil {
		t.Fatalf("InstallProvider: %v", err)
	}
	if up.Version != "Details.20261015.15339.172" {
		t.Errorf("upstream version = %q, want Details.20261015.15339.172", up.Version)
	}
	if p, _ := cdnPath.Load().(string); p != "/files/9202/791/Details-Details.20261015.15339.172.zip" {
		t.Errorf("CDN path = %q, want the constructed files URL", p)
	}

	got, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	a := got.Client(client.FlavorMainline).Addons["details"]
	if a == nil {
		t.Fatal("state lost the addon entry")
	}
	if a.Source.Kind != state.SourceCfwidget || a.Source.Slug != "details" {
		t.Errorf("source = %+v, want the cfwidget source recorded for later sync", a.Source)
	}
	if a.Strategy != state.StrategyDirect {
		t.Errorf("strategy = %q, want %q", a.Strategy, state.StrategyDirect)
	}
	if a.InstalledVersion != "Details.20261015.15339.172" || a.ETag != "Details.20261015.15339.172" {
		t.Errorf("installed/etag = %q/%q, want Details.20261015.15339.172", a.InstalledVersion, a.ETag)
	}
	if !strings.Contains(a.Changelog, "curseforge.com/wow/addons/details/files/9202791") {
		t.Errorf("changelog = %q, want the per-file CurseForge page", a.Changelog)
	}
	if _, err := os.Stat(filepath.Join(root, "Interface", "AddOns", "Details", "Details.toc")); err != nil {
		t.Errorf("addon not installed: %v", err)
	}
}
