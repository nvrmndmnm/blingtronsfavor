package resolver

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/github"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
)

// ---- fixture HTTP server with recorded GitHub API payloads ----

// newFixture serves one recorded GitHub repository: the releases list at
// /repos/<owner>/<repo>/releases?per_page=10 and the release assets at
// /assets/<name>. Payloads use a {{BASE}} placeholder for the absolute asset
// URLs GitHub hands out as browser_download_url.
func newFixture(t *testing.T, releases string, assets map[string]string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"fixture-v1"`)
		_, _ = fmt.Fprint(w, strings.ReplaceAll(releases, "{{BASE}}", srv.URL))
	})
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		body, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, strings.ReplaceAll(body, "{{BASE}}", srv.URL))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newFixtureResolver wires a Resolver to the fixture server.
func newFixtureResolver(t *testing.T, srv *httptest.Server) *Resolver {
	t.Helper()
	gh, err := github.New(github.Options{BaseURL: srv.URL, CacheDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}
	return &Resolver{GH: gh, HTTP: srv.Client()}
}

// resolveFixture resolves one request against the fixture server.
func resolveFixture(t *testing.T, srv *httptest.Server, req Request) Plan {
	t.Helper()
	plan, err := newFixtureResolver(t, srv).Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("Resolve(%+v): %v", req, err)
	}
	return plan
}

// wantAsset fails the test unless plan is a release plan downloading an
// asset whose name matches wantName.
func wantAsset(t *testing.T, plan Plan, wantName string) {
	t.Helper()
	if plan.Strategy != state.StrategyRelease {
		t.Fatalf("Strategy = %q, want %q (reason: %s)", plan.Strategy, state.StrategyRelease, plan.Reason)
	}
	if !strings.HasSuffix(plan.URL, "/assets/"+wantName) {
		t.Errorf("URL = %q, want asset %q (reason: %s)", plan.URL, wantName, plan.Reason)
	}
}

// ---- recorded API payloads ----

// releasesMultiFlavor is a recorded release payload whose release.json names
// one asset per game flavor (BigWigsMods/packager metadata).
const releasesMultiFlavor = `[
  {
    "tag_name": "v1.3.0",
    "draft": false,
    "prerelease": false,
    "published_at": "2026-03-01T12:00:00Z",
    "assets": [
      {"name": "release.json", "size": 512, "browser_download_url": "{{BASE}}/assets/release.json", "content_type": "application/json"},
      {"name": "Widget-1.3.0.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget-1.3.0.zip", "content_type": "application/zip"},
      {"name": "Widget-1.3.0-nolib.zip", "size": 1024, "browser_download_url": "{{BASE}}/assets/Widget-1.3.0-nolib.zip", "content_type": "application/zip"},
      {"name": "Widget-1.3.0-camelot.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget-1.3.0-camelot.zip", "content_type": "application/zip"},
      {"name": "Widget-1.3.0-classic.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget-1.3.0-classic.zip", "content_type": "application/zip"}
    ]
  }
]`

// metaMultiFlavor is the recorded release.json of releasesMultiFlavor.
const metaMultiFlavor = `{
  "assets": [
    {"flavor": "mainline", "nolib": false, "file": "Widget-1.3.0.zip"},
    {"flavor": "mainline", "nolib": true, "file": "Widget-1.3.0-nolib.zip"},
    {"flavor": "camelot", "nolib": false, "file": "Widget-1.3.0-camelot.zip"},
    {"flavor": "vanilla", "nolib": false, "file": "Widget-1.3.0-classic.zip"}
  ]
}`

// releasesAliasForms is the same repository recorded with the schema
// spellings risk R4 leaves open: a top-level record list with "type",
// "filename" and "no_lib" keys and the "forever" flavor key.
const releasesAliasForms = `[
  {
    "tag_name": "v1.3.0",
    "draft": false,
    "prerelease": false,
    "published_at": "2026-03-01T12:00:00Z",
    "assets": [
      {"name": "release.json", "size": 512, "browser_download_url": "{{BASE}}/assets/release.json", "content_type": "application/json"},
      {"name": "Widget_1.3.0_Forever.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget_1.3.0_Forever.zip", "content_type": "application/zip"},
      {"name": "Widget_1.3.0_Forever_NoLib.zip", "size": 1024, "browser_download_url": "{{BASE}}/assets/Widget_1.3.0_Forever_NoLib.zip", "content_type": "application/zip"},
      {"name": "Widget_1.3.0_Classic.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget_1.3.0_Classic.zip", "content_type": "application/zip"}
    ]
  }
]`

const metaAliasForms = `[
  {"type": "forever", "filename": "Widget_1.3.0_Forever.zip", "no_lib": false},
  {"type": "forever", "filename": "Widget_1.3.0_Forever_NoLib.zip", "no_lib": true},
  {"type": "classic-era", "filename": "Widget_1.3.0_Classic.zip", "no_lib": false}
]`

// releasesUniversal records a universal-package release: one untagged zip
// serving several game flavors (recorded from DeadlyBossMods/DeadlyBossMods
// 12.1.17, whose release.json lists one "metadata" flavor/interface row per
// client — Forever at interface 16001 included).
const releasesUniversal = `[
  {
    "tag_name": "12.1.17",
    "draft": false,
    "prerelease": false,
    "published_at": "2026-03-01T12:00:00Z",
    "assets": [
      {"name": "release.json", "size": 460, "browser_download_url": "{{BASE}}/assets/release.json", "content_type": "application/json"},
      {"name": "DBM-Core-12.1.17.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/DBM-Core-12.1.17.zip", "content_type": "application/zip"}
    ]
  }
]`

// metaUniversal is the recorded release.json of releasesUniversal: one
// untagged zip whose nested "metadata" list enumerates every flavor it
// serves — supported ones (mists, mainline, classic, forever), foreign ones
// (titan, wrath, cata, bcc) and a duplicate mainline row.
const metaUniversal = `{
  "releases": [
    {
      "name": "DBM - Core",
      "version": "12.1.17",
      "filename": "DBM-Core-12.1.17.zip",
      "nolib": false,
      "metadata": [
        {"flavor": "mists", "interface": 50504},
        {"flavor": "titan", "interface": 38002},
        {"flavor": "mainline", "interface": 120100},
        {"flavor": "mainline", "interface": 120105},
        {"flavor": "wrath", "interface": 30405},
        {"flavor": "cata", "interface": 40402},
        {"flavor": "classic", "interface": 11509},
        {"flavor": "bcc", "interface": 20506},
        {"flavor": "forever", "interface": 16001}
      ]
    }
  ]
}`

// releasesHeuristic is a recorded release without release.json: selection
// falls back to zip-name heuristics over multi-flavor assets.
const releasesHeuristic = `[
  {
    "tag_name": "v2.0.0",
    "draft": false,
    "prerelease": false,
    "published_at": "2026-04-01T00:00:00Z",
    "assets": [
      {"name": "Widget-2.0.0.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget-2.0.0.zip", "content_type": "application/zip"},
      {"name": "Widget-2.0.0-nolib.zip", "size": 1024, "browser_download_url": "{{BASE}}/assets/Widget-2.0.0-nolib.zip", "content_type": "application/zip"},
      {"name": "Widget_2.0.0_Mainline.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget_2.0.0_Mainline.zip", "content_type": "application/zip"},
      {"name": "Widget-classic-2.0.0.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget-classic-2.0.0.zip", "content_type": "application/zip"},
      {"name": "Widget-2.0.0-Camelot.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget-2.0.0-Camelot.zip", "content_type": "application/zip"},
      {"name": "Widget-2.0.0-wrath.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget-2.0.0-wrath.zip", "content_type": "application/zip"}
    ]
  }
]`

// releasesUntaggedOnly is a recorded release with a single untagged zip —
// the packager's retail package — and nothing else.
const releasesUntaggedOnly = `[
  {
    "tag_name": "v1.0.0",
    "draft": false,
    "prerelease": false,
    "published_at": "2026-01-05T00:00:00Z",
    "assets": [
      {"name": "Widget-1.0.0.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget-1.0.0.zip", "content_type": "application/zip"}
    ]
  }
]`

// releasesLibOnly is a recorded release that ships nolib packages only — an
// addon with no bundled libraries.
const releasesLibOnly = `[
  {
    "tag_name": "v1.0.0",
    "draft": false,
    "prerelease": false,
    "published_at": "2026-01-05T00:00:00Z",
    "assets": [
      {"name": "Widget-1.0.0-nolib.zip", "size": 1024, "browser_download_url": "{{BASE}}/assets/Widget-1.0.0-nolib.zip", "content_type": "application/zip"},
      {"name": "Widget-1.0.0-camelot-nolib.zip", "size": 1024, "browser_download_url": "{{BASE}}/assets/Widget-1.0.0-camelot-nolib.zip", "content_type": "application/zip"}
    ]
  }
]`

// releasesPreOnly records a pre-release that is the only release carrying a
// Camelot/Forever flavor asset.
const releasesPreOnly = `[
  {
    "tag_name": "v1.3.0-rc1",
    "draft": false,
    "prerelease": true,
    "published_at": "2026-03-15T00:00:00Z",
    "assets": [
      {"name": "Widget-1.3.0-Camelot.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget-1.3.0-Camelot.zip", "content_type": "application/zip"}
    ]
  },
  {
    "tag_name": "v1.2.0",
    "draft": false,
    "prerelease": false,
    "published_at": "2026-03-01T00:00:00Z",
    "assets": [
      {"name": "Widget-1.2.0.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget-1.2.0.zip", "content_type": "application/zip"}
    ]
  }
]`

// releasesStableAndPre records a newer pre-release alongside an older stable
// release that both serve mainline.
const releasesStableAndPre = `[
  {
    "tag_name": "v2.0.0-rc1",
    "draft": false,
    "prerelease": true,
    "published_at": "2026-02-01T00:00:00Z",
    "assets": [
      {"name": "Widget_2.0.0_Mainline.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget_2.0.0_Mainline.zip", "content_type": "application/zip"}
    ]
  },
  {
    "tag_name": "v1.9.0",
    "draft": false,
    "prerelease": false,
    "published_at": "2026-01-01T00:00:00Z",
    "assets": [
      {"name": "Widget_1.9.0_Mainline.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget_1.9.0_Mainline.zip", "content_type": "application/zip"}
    ]
  }
]`

// releasesWithDraft records a newer draft release that must never install.
const releasesWithDraft = `[
  {
    "tag_name": "v3.0.0",
    "draft": true,
    "prerelease": false,
    "published_at": "2026-05-01T00:00:00Z",
    "assets": [
      {"name": "Widget_3.0.0_Mainline.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget_3.0.0_Mainline.zip", "content_type": "application/zip"}
    ]
  },
  {
    "tag_name": "v2.9.0",
    "draft": false,
    "prerelease": false,
    "published_at": "2026-04-01T00:00:00Z",
    "assets": [
      {"name": "Widget_2.9.0_Mainline.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget_2.9.0_Mainline.zip", "content_type": "application/zip"}
    ]
  }
]`

// releasesForeignOnly records releases whose packages target client flavors
// bfctl does not support.
const releasesForeignOnly = `[
  {
    "tag_name": "v4.0.0",
    "draft": false,
    "prerelease": false,
    "published_at": "2026-03-01T00:00:00Z",
    "assets": [
      {"name": "Widget-4.0.0-wrath.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget-4.0.0-wrath.zip", "content_type": "application/zip"},
      {"name": "Widget-4.0.0-cata.zip", "size": 2048, "browser_download_url": "{{BASE}}/assets/Widget-4.0.0-cata.zip", "content_type": "application/zip"}
    ]
  }
]`

// TestRecordedMetaFixturesParse guards the recorded release.json payloads
// against transcription errors: ParseMeta fails soft in production (a
// malformed metadata asset falls back to zip-name heuristics), so a broken
// fixture would surface as a confusing resolution failure instead of a parse
// error.
func TestRecordedMetaFixturesParse(t *testing.T) {
	for name, body := range map[string]string{
		"metaMultiFlavor": metaMultiFlavor,
		"metaAliasForms":  metaAliasForms,
		"metaUniversal":   metaUniversal,
	} {
		if _, err := ParseMeta([]byte(body)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// ---- resolution tests ----

// TestResolveReleaseJSONFlavorSelection covers plan §2 step 1: with a
// release.json present the asset is selected from the metadata, honoring the
// flavor key (including the camelot spelling of Forever) and nolib=false.
func TestResolveReleaseJSONFlavorSelection(t *testing.T) {
	srv := newFixture(t, releasesMultiFlavor, map[string]string{"release.json": metaMultiFlavor})

	for _, tc := range []struct {
		flavor client.Flavor
		asset  string
		reason string
	}{
		{client.FlavorForever, "Widget-1.3.0-camelot.zip", "camelot"},
		{client.FlavorClassic, "Widget-1.3.0-classic.zip", "vanilla"},
		{client.FlavorMainline, "Widget-1.3.0.zip", "mainline"},
	} {
		plan := resolveFixture(t, srv, Request{Owner: "acme", Repo: "widget", Flavor: tc.flavor})
		wantAsset(t, plan, tc.asset)
		if plan.Version != "v1.3.0" {
			t.Errorf("flavor %s: Version = %q, want v1.3.0", tc.flavor, plan.Version)
		}
		for _, want := range []string{"release.json", "flavor " + tc.reason} {
			if !strings.Contains(plan.Reason, want) {
				t.Errorf("flavor %s: Reason = %q, want it to mention %q", tc.flavor, plan.Reason, want)
			}
		}
	}
}

// TestResolveReleaseJSONAliasForms covers the tolerated release.json schema
// variants of risk R4: record lists with "type"/"filename"/"no_lib" keys and
// both Forever flavor spellings.
func TestResolveReleaseJSONAliasForms(t *testing.T) {
	srv := newFixture(t, releasesAliasForms, map[string]string{"release.json": metaAliasForms})

	plan := resolveFixture(t, srv, Request{Owner: "acme", Repo: "widget", Flavor: client.FlavorForever})
	wantAsset(t, plan, "Widget_1.3.0_Forever.zip")

	plan = resolveFixture(t, srv, Request{Owner: "acme", Repo: "widget", Flavor: client.FlavorClassic})
	wantAsset(t, plan, "Widget_1.3.0_Classic.zip")
}

// TestResolveUniversalPackageMetadata covers the universal-package
// release.json schema (recorded from DeadlyBossMods/DeadlyBossMods): one
// untagged zip declares a nested per-flavor "metadata" list, so every flavor
// it lists — Forever included — resolves to that zip via release.json
// instead of "source build required" (the zip name alone would be untagged
// and serve mainline only). Foreign spellings (titan, wrath, cata, bcc) in
// the list neither disqualify the package nor serve on their own.
func TestResolveUniversalPackageMetadata(t *testing.T) {
	srv := newFixture(t, releasesUniversal, map[string]string{"release.json": metaUniversal})

	for _, flavor := range []client.Flavor{client.FlavorForever, client.FlavorClassic, client.FlavorMists, client.FlavorMainline} {
		plan := resolveFixture(t, srv, Request{Owner: "acme", Repo: "widget", Flavor: flavor})
		wantAsset(t, plan, "DBM-Core-12.1.17.zip")
		if plan.Version != "12.1.17" {
			t.Errorf("flavor %s: Version = %q, want 12.1.17", flavor, plan.Version)
		}
		for _, want := range []string{"release.json", "flavor " + string(flavor)} {
			if !strings.Contains(plan.Reason, want) {
				t.Errorf("flavor %s: Reason = %q, want it to mention %q", flavor, plan.Reason, want)
			}
		}
	}

	// Without release.json the untagged zip is the mainline package and a
	// flavor only the metadata declared still needs a source build.
	srvHeuristic := newFixture(t, releasesUniversal, map[string]string{})
	plan := resolveFixture(t, srvHeuristic, Request{Owner: "acme", Repo: "widget", Flavor: client.FlavorForever})
	if plan.Strategy != state.StrategySourcePkgmeta {
		t.Fatalf("no release.json: Strategy = %q, want %q", plan.Strategy, state.StrategySourcePkgmeta)
	}
}

// TestResolveZipHeuristics covers plan §2 step 2: without release.json the
// asset comes from zip-name heuristics over multi-flavor assets, and a
// package for an unsupported flavor is never selected.
func TestResolveZipHeuristics(t *testing.T) {
	srv := newFixture(t, releasesHeuristic, map[string]string{})

	for _, tc := range []struct {
		flavor client.Flavor
		asset  string
	}{
		{client.FlavorMainline, "Widget_2.0.0_Mainline.zip"}, // flavor-tagged beats untagged
		{client.FlavorClassic, "Widget-classic-2.0.0.zip"},   // CurseForge-style name
		{client.FlavorForever, "Widget-2.0.0-Camelot.zip"},   // flavor-at-end name
	} {
		plan := resolveFixture(t, srv, Request{Owner: "acme", Repo: "widget", Flavor: tc.flavor})
		wantAsset(t, plan, tc.asset)
		if plan.Version != "v2.0.0" {
			t.Errorf("flavor %s: Version = %q, want v2.0.0", tc.flavor, plan.Version)
		}
		if !strings.Contains(plan.Reason, "zip name heuristics") {
			t.Errorf("flavor %s: Reason = %q, want it to mention zip name heuristics", tc.flavor, plan.Reason)
		}
	}
}

// TestResolveUntaggedZipServesMainlineOnly documents the untagged-zip rule:
// the packager's untagged package is the retail one, so it resolves for
// mainline and nothing else.
func TestResolveUntaggedZipServesMainlineOnly(t *testing.T) {
	srv := newFixture(t, releasesUntaggedOnly, map[string]string{})

	plan := resolveFixture(t, srv, Request{Owner: "acme", Repo: "widget", Flavor: client.FlavorMainline})
	wantAsset(t, plan, "Widget-1.0.0.zip")

	plan = resolveFixture(t, srv, Request{Owner: "acme", Repo: "widget", Flavor: client.FlavorClassic})
	if plan.Strategy != state.StrategySourcePkgmeta {
		t.Fatalf("classic: Strategy = %q, want %q", plan.Strategy, state.StrategySourcePkgmeta)
	}
}

// TestResolveNolibPreference covers "prefer nolib=false unless the addon is
// known lib-less": libs bundled wins when both exist, and a nolib-only
// release still resolves.
func TestResolveNolibPreference(t *testing.T) {
	srvBoth := newFixture(t, releasesHeuristic, map[string]string{})
	plan := resolveFixture(t, srvBoth, Request{Owner: "acme", Repo: "widget", Flavor: client.FlavorMainline})
	if strings.Contains(plan.URL, "nolib") {
		t.Errorf("URL = %q, want the libs-bundled asset", plan.URL)
	}

	srvOnly := newFixture(t, releasesLibOnly, map[string]string{})
	plan = resolveFixture(t, srvOnly, Request{Owner: "acme", Repo: "widget", Flavor: client.FlavorMainline})
	wantAsset(t, plan, "Widget-1.0.0-nolib.zip")
	plan = resolveFixture(t, srvOnly, Request{Owner: "acme", Repo: "widget", Flavor: client.FlavorForever})
	wantAsset(t, plan, "Widget-1.0.0-camelot-nolib.zip")
}

// TestResolvePreReleaseOnlyFlavorMatch covers plan §2 step 3: a pre-release
// is allowed when it is the only flavor match.
func TestResolvePreReleaseOnlyFlavorMatch(t *testing.T) {
	srv := newFixture(t, releasesPreOnly, map[string]string{})

	plan := resolveFixture(t, srv, Request{Owner: "acme", Repo: "widget", Flavor: client.FlavorForever})
	wantAsset(t, plan, "Widget-1.3.0-Camelot.zip")
	if plan.Version != "v1.3.0-rc1" {
		t.Errorf("Version = %q, want v1.3.0-rc1", plan.Version)
	}
	if !strings.Contains(plan.Reason, "pre-release (only flavor match)") {
		t.Errorf("Reason = %q, want it to mark the pre-release", plan.Reason)
	}

	// A stable release that serves the flavor wins over the pre-release.
	plan = resolveFixture(t, srv, Request{Owner: "acme", Repo: "widget", Flavor: client.FlavorMainline})
	if plan.Version != "v1.2.0" {
		t.Errorf("mainline Version = %q, want stable v1.2.0", plan.Version)
	}
}

// TestResolveStablePreferredOverNewerPreRelease pins the release order of
// plan §2 step 3: a pre-release is only allowed when it is the only flavor
// match, so an older stable release beats a newer pre-release.
func TestResolveStablePreferredOverNewerPreRelease(t *testing.T) {
	srv := newFixture(t, releasesStableAndPre, map[string]string{})

	plan := resolveFixture(t, srv, Request{Owner: "acme", Repo: "widget", Flavor: client.FlavorMainline})
	if plan.Version != "v1.9.0" {
		t.Errorf("Version = %q, want stable v1.9.0 over pre-release v2.0.0-rc1", plan.Version)
	}
}

// TestResolveSkipsDrafts: draft releases are never installable, no matter
// how new their assets are.
func TestResolveSkipsDrafts(t *testing.T) {
	srv := newFixture(t, releasesWithDraft, map[string]string{})

	plan := resolveFixture(t, srv, Request{Owner: "acme", Repo: "widget", Flavor: client.FlavorMainline})
	if plan.Version != "v2.9.0" {
		t.Errorf("Version = %q, want v2.9.0 (the draft must be skipped)", plan.Version)
	}
}

// TestResolveFallsBackToSourceBuild: when no release asset serves the flavor
// the plan is a source-pkgmeta build with a "source build required" reason
// and the tracked ref — built from source on the next install.
func TestResolveFallsBackToSourceBuild(t *testing.T) {
	srv := newFixture(t, releasesForeignOnly, map[string]string{})

	plan := resolveFixture(t, srv, Request{
		Owner: "acme", Repo: "widget", Flavor: client.FlavorMainline,
		Tracking: "develop", DefaultBranch: "trunk",
	})
	if plan.Strategy != state.StrategySourcePkgmeta {
		t.Fatalf("Strategy = %q, want %q", plan.Strategy, state.StrategySourcePkgmeta)
	}
	if !strings.Contains(plan.Reason, "source build required") {
		t.Errorf("Reason = %q, want it to say source build required", plan.Reason)
	}
	if plan.URL != "https://github.com/acme/widget.git" {
		t.Errorf("URL = %q, want the git URL", plan.URL)
	}
	if plan.Ref != "develop" {
		t.Errorf("Ref = %q, want the tracked branch develop", plan.Ref)
	}
}

// TestResolveSourcePlanVersionStamps pins the version stamp of source-pkgmeta
// plans: a tag ref stamps the tag name (tags are immutable), a branch ref
// stamps the short SHA of its HEAD commit (so callers rebuild exactly when
// the branch moves), and an unanchored plan tracks the repository's default
// branch.
func TestResolveSourcePlanVersionStamps(t *testing.T) {
	const (
		shaTag  = "1111111111111111111111111111111111111111"
		shaHead = "abcdef12abcdef12abcdef12abcdef12abcdef12"
		shaOld  = "9999999999999999999999999999999999999999"
	)
	for _, tc := range []struct {
		name       string
		api        staleAPI
		req        Request
		wantRef    string
		wantVer    string
		wantReason string
	}{
		{
			name: "tag ref stamps the tag name",
			api: staleAPI{
				branch: "master",
				tags:   []github.Tag{{Name: "v1.2", Commit: github.TagCommit{SHA: shaTag}}},
				commits: map[string]github.Commit{
					shaTag:   staleCommit(shaTag, staleTime(1)),
					"master": staleCommit(shaHead, staleTime(2)),
				},
			},
			req:        Request{Owner: "demo", Repo: "addon", Flavor: client.FlavorForever, DefaultBranch: "master"},
			wantRef:    "v1.2",
			wantVer:    "v1.2",
			wantReason: "no releases",
		},
		{
			name: "branch ref stamps the short SHA",
			api: staleAPI{
				branch: "master",
				tags:   []github.Tag{{Name: "v1.0", Commit: github.TagCommit{SHA: shaOld}}},
				commits: map[string]github.Commit{
					shaOld:   staleCommit(shaOld, staleTime(2)),
					"master": staleCommit(shaHead, staleTime(1)),
				},
			},
			req:        Request{Owner: "demo", Repo: "addon", Flavor: client.FlavorForever, DefaultBranch: "master"},
			wantRef:    "master",
			wantVer:    "abcdef12",
			wantReason: "no releases",
		},
		{
			name: "pinned plan tracks the default branch",
			api: staleAPI{
				branch:  "master",
				commits: map[string]github.Commit{"master": staleCommit(shaHead, staleTime(1))},
			},
			req: Request{
				Owner: "demo", Repo: "addon", Flavor: client.FlavorForever,
				Tracking: "stable", PinnedStrategy: state.StrategySourcePkgmeta,
			},
			wantRef:    "master",
			wantVer:    "abcdef12",
			wantReason: "pinned to strategy source-pkgmeta",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := newStaleResolver(t, tc.api).Resolve(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if plan.Strategy != state.StrategySourcePkgmeta {
				t.Fatalf("Strategy = %q, want %q (reason: %s)", plan.Strategy, state.StrategySourcePkgmeta, plan.Reason)
			}
			if plan.Ref != tc.wantRef {
				t.Errorf("Ref = %q, want %q", plan.Ref, tc.wantRef)
			}
			if plan.Version != tc.wantVer {
				t.Errorf("Version = %q, want %q", plan.Version, tc.wantVer)
			}
			if !strings.Contains(plan.Reason, tc.wantReason) {
				t.Errorf("Reason = %q, want substring %q", plan.Reason, tc.wantReason)
			}
		})
	}
}

// TestParseRepo covers every repository spelling "bfctl add" accepts.
func TestParseRepo(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		owner   string
		repo    string
		wantErr bool
	}{
		{raw: "acme/widget", owner: "acme", repo: "widget"},
		{raw: "acme/widget/", owner: "acme", repo: "widget"},
		{raw: "https://github.com/acme/widget", owner: "acme", repo: "widget"},
		{raw: "https://github.com/acme/widget.git", owner: "acme", repo: "widget"},
		{raw: "https://www.github.com/acme/widget/releases", owner: "acme", repo: "widget"},
		{raw: "git@github.com:acme/widget.git", owner: "acme", repo: "widget"},
		{raw: "ssh://git@github.com/acme/widget.git", owner: "acme", repo: "widget"},
		{raw: "https://gitlab.com/acme/widget", wantErr: true},
		{raw: "https://github.com/acme", wantErr: true},
		{raw: "acme/widget/extra", wantErr: true},
		{raw: "acme", wantErr: true},
		{raw: "", wantErr: true},
	} {
		ref, err := ParseRepo(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseRepo(%q) = %+v, want an error", tc.raw, ref)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRepo(%q): %v", tc.raw, err)
			continue
		}
		if ref.Owner != tc.owner || ref.Repo != tc.repo {
			t.Errorf("ParseRepo(%q) = %+v, want %s/%s", tc.raw, ref, tc.owner, tc.repo)
		}
	}
}

// ---- download & unpack tests ----

// makeZip builds a zip archive with the given file -> content entries.
func makeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("zip.Create(%s): %v", name, err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatalf("zip.Write(%s): %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip.Close: %v", err)
	}
	return buf.String()
}

// TestUnpackStripsWrapperDir: a single top-level wrapper directory without a
// .toc of its own is stripped from the payload.
func TestUnpackStripsWrapperDir(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "asset.zip")
	body := makeZip(t, map[string]string{
		"Widget-1.3.0/Widget/Widget.toc": "## Interface: 120100\n",
		"Widget-1.3.0/Widget/Widget.lua": "-- widget\n",
		"Widget-1.3.0/README.md":         "readme\n",
	})
	if err := os.WriteFile(zipPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	root, err := Unpack(zipPath, dir)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if root != dir {
		t.Fatalf("root = %q, want %q", root, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "Widget", "Widget.toc")); err != nil {
		t.Errorf("stripped payload misses Widget/Widget.toc: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Widget-1.3.0")); !os.IsNotExist(err) {
		t.Errorf("wrapper directory survived the strip: %v", err)
	}
}

// TestUnpackKeepsAddonDirectory: a top-level directory that carries a .toc is
// the addon directory itself and must not be stripped.
func TestUnpackKeepsAddonDirectory(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "asset.zip")
	body := makeZip(t, map[string]string{
		"Widget/Widget.toc": "## Interface: 120100\n",
		"Widget/Widget.lua": "-- widget\n",
	})
	if err := os.WriteFile(zipPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := Unpack(zipPath, dir); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Widget", "Widget.toc")); err != nil {
		t.Errorf("payload misses Widget/Widget.toc: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Widget.toc")); !os.IsNotExist(err) {
		t.Errorf("addon directory was wrongly stripped: %v", err)
	}
}

// TestUnpackRejectsEscapingEntries: archive entries that would land outside
// the payload directory are refused (zip-slip).
func TestUnpackRejectsEscapingEntries(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "asset.zip")
	body := makeZip(t, map[string]string{
		"Widget/Widget.toc": "## Interface: 120100\n",
		"../evil.toc":       "evil\n",
	})
	if err := os.WriteFile(zipPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Unpack(zipPath, t.TempDir()); err == nil {
		t.Fatal("Unpack accepted an escaping archive entry")
	}
}

// TestFetchDownloadsAndUnpacks drives the whole release download: Fetch
// streams the asset zip and returns the payload root.
func TestFetchDownloadsAndUnpacks(t *testing.T) {
	asset := makeZip(t, map[string]string{
		"Widget-1.3.0/Widget/Widget.toc": "## Interface: 120100\n",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, asset)
	}))
	defer srv.Close()

	res := &Resolver{HTTP: srv.Client()}
	dir := t.TempDir()
	root, err := res.Fetch(context.Background(), Plan{
		Strategy: state.StrategyRelease,
		Version:  "v1.3.0",
		URL:      srv.URL + "/Widget-1.3.0.zip",
	}, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Widget", "Widget.toc")); err != nil {
		t.Errorf("payload misses Widget/Widget.toc: %v", err)
	}
}

// TestFetchRefusesNonZipAssets: only zip release assets are unpacked.
func TestFetchRefusesNonZipAssets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "not an archive")
	}))
	defer srv.Close()

	res := &Resolver{HTTP: srv.Client()}
	_, err := res.Fetch(context.Background(), Plan{
		Strategy: state.StrategyRelease,
		URL:      srv.URL + "/Widget-1.3.0.tar.gz",
	}, t.TempDir())
	if err == nil {
		t.Fatal("Fetch accepted a non-zip asset")
	}
}
