package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/github"
	"github.com/itp5b5/blessforge/internal/state"
)

// ---- staleness fixture: a scripted GitHub API for the M4 detector ----

// staleAPI scripts the GitHub endpoints the staleness detector consults: the
// releases and tags lists, per-ref commits, compare results and repository
// metadata. Unscripted paths answer 404.
type staleAPI struct {
	releases      []github.Release
	tags          []github.Tag
	commits       map[string]github.Commit        // ref name or SHA -> commit
	compare       map[string]github.CompareResult // "base...head" -> result
	compareStatus map[string]int                  // "base...head" -> error status
	branch        string                          // default branch name
}

// staleTime returns a commit/publish timestamp hours ago, truncated so
// fixture deltas are exact.
func staleTime(hoursAgo int) time.Time {
	return time.Now().Add(-time.Duration(hoursAgo) * time.Hour).UTC().Truncate(time.Second)
}

// staleRelease builds a release with named zip assets.
func staleRelease(tag string, published time.Time, prerelease bool, assets ...string) github.Release {
	rel := github.Release{TagName: tag, PublishedAt: published, Prerelease: prerelease}
	for _, name := range assets {
		rel.Assets = append(rel.Assets, github.Asset{Name: name, Size: 1, BrowserDownloadURL: "https://example.invalid/" + name})
	}
	return rel
}

// staleCommit builds a commit object with a committer date.
func staleCommit(sha string, when time.Time) github.Commit {
	return github.Commit{SHA: sha, Commit: github.CommitData{Committer: github.CommitAuthor{Date: when}}}
}

// newStaleResolver wires a Resolver to a fixture server serving api.
func newStaleResolver(t *testing.T, api staleAPI) *Resolver {
	t.Helper()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Errorf("encode fixture response: %v", err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/repos/demo/addon")
		switch {
		case p == "/releases":
			writeJSON(w, api.releases)
		case p == "/tags":
			writeJSON(w, api.tags)
		case strings.HasPrefix(p, "/commits/"):
			ref := strings.TrimPrefix(p, "/commits/")
			c, ok := api.commits[ref]
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, c)
		case strings.HasPrefix(p, "/compare/"):
			key := strings.TrimPrefix(p, "/compare/")
			if status, ok := api.compareStatus[key]; ok {
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, `{"message":"boom"}`)
				return
			}
			cmp, ok := api.compare[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, cmp)
		case p == "":
			writeJSON(w, github.Repository{DefaultBranch: api.branch, PushedAt: staleTime(1)})
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh, err := github.New(github.Options{BaseURL: srv.URL, CacheDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}
	return &Resolver{GH: gh, HTTP: srv.Client()}
}

// staleReq is the standard request: the "demo/addon" fixture repository for
// the Forever flavor, tracking stable releases of the "main" branch.
func staleReq() Request {
	return Request{Owner: "demo", Repo: "addon", Flavor: client.FlavorForever}
}

// runStale resolves one staleness check against a fresh fixture.
func runStale(t *testing.T, api staleAPI, req Request) StaleDecision {
	t.Helper()
	d, err := newStaleResolver(t, api).CheckStale(context.Background(), req, api.releases)
	if err != nil {
		t.Fatalf("CheckStale(%+v): %v", req, err)
	}
	return d
}

// wantStale asserts the trigger verdict and a reason substring.
func wantStale(t *testing.T, d StaleDecision, trigger bool, reasonSub string) {
	t.Helper()
	if d.Trigger != trigger {
		t.Fatalf("Trigger = %v, want %v (reason: %s)", d.Trigger, trigger, d.Reason)
	}
	if reasonSub != "" && !strings.Contains(d.Reason, reasonSub) {
		t.Errorf("Reason = %q, want substring %q", d.Reason, reasonSub)
	}
}

// ---- trigger (a): the repository has no releases at all ----

func TestStaleTriggerNoReleases(t *testing.T) {
	api := staleAPI{
		branch: "main",
		tags:   []github.Tag{{Name: "v1.2", Commit: github.TagCommit{SHA: "sha12"}}},
		commits: map[string]github.Commit{
			"sha12": staleCommit("sha12", staleTime(5*24)),
			"main":  staleCommit("main", staleTime(10*24)),
		},
	}
	d := runStale(t, api, staleReq())
	wantStale(t, d, true, "no releases")
	if d.Reason != "no releases" {
		t.Errorf("Reason = %q, want %q", d.Reason, "no releases")
	}
	if d.Ref != "v1.2" {
		t.Errorf("Ref = %q, want newest tag %q", d.Ref, "v1.2")
	}
}

// ---- trigger (b): the latest release has no asset for the flavor ----

func TestStaleTriggerFlavorAssetMissing(t *testing.T) {
	api := staleAPI{
		branch: "main",
		// v2.0 is fresh (2 days) so trigger (c) cannot fire: the only
		// trigger is the missing Forever asset of the latest release.
		releases: []github.Release{
			staleRelease("v2.0", staleTime(2*24), false, "Addon_Mainline.zip"),
			staleRelease("v1.9", staleTime(30*24), false, "Addon_Forever.zip"),
		},
		tags: []github.Tag{{Name: "v2.0", Commit: github.TagCommit{SHA: "sha20"}}},
		commits: map[string]github.Commit{
			"sha20": staleCommit("sha20", staleTime(2*24)),
			"main":  staleCommit("main", staleTime(24)),
		},
	}
	d := runStale(t, api, staleReq())
	wantStale(t, d, true, "latest release v2.0 has no asset for flavor forever")
}

// ---- trigger (c): stale release whose delta touches the target .toc ----

// staleTocDeltaAPI is the shared trigger-(c) fixture: release v1.0 is 30
// days old, tag v1.1 landed 2 days ago and the v1.0...v1.1 delta touched the
// Forever toc.
func staleTocDeltaAPI(compare map[string]github.CompareResult) staleAPI {
	return staleAPI{
		branch: "main",
		releases: []github.Release{
			staleRelease("v1.0", staleTime(30*24), false, "Addon_Forever.zip"),
		},
		tags: []github.Tag{{Name: "v1.1", Commit: github.TagCommit{SHA: "sha11"}}},
		commits: map[string]github.Commit{
			"sha11": staleCommit("sha11", staleTime(2*24)),
			"main":  staleCommit("main", staleTime(4*24)),
		},
		compare: compare,
	}
}

func TestStaleTriggerTocDelta(t *testing.T) {
	api := staleTocDeltaAPI(map[string]github.CompareResult{
		"v1.0...v1.1": {Files: []github.CompareFile{
			{Filename: "MyAddon_Forever.toc", Status: "modified"},
			{Filename: "core.lua", Status: "modified"},
		}},
	})
	d := runStale(t, api, staleReq())
	wantStale(t, d, true, "touches the forever toc")
	if !strings.Contains(d.Reason, "v1.1 moved 28 days past release v1.0") {
		t.Errorf("Reason = %q, want the measured delta", d.Reason)
	}
	if d.Ref != "v1.1" {
		t.Errorf("Ref = %q, want %q", d.Ref, "v1.1")
	}
}

// A compare payload that is unavailable or truncated by GitHub's file window
// triggers conservatively.
func TestStaleTriggerTocDeltaUnverifiable(t *testing.T) {
	t.Run("compare error", func(t *testing.T) {
		api := staleTocDeltaAPI(nil)
		api.compareStatus = map[string]int{"v1.0...v1.1": http.StatusInternalServerError}
		d := runStale(t, api, staleReq())
		wantStale(t, d, true, "toc-delta unverifiable")
	})
	t.Run("compare window full", func(t *testing.T) {
		full := make([]github.CompareFile, github.MaxCompareFiles)
		for i := range full {
			full[i] = github.CompareFile{Filename: fmt.Sprintf("file%03d.lua", i)}
		}
		api := staleTocDeltaAPI(map[string]github.CompareResult{
			"v1.0...v1.1": {TotalCommits: 40, Files: full},
		})
		d := runStale(t, api, staleReq())
		wantStale(t, d, true, "toc-delta unverifiable")
	})
}

// ---- non-triggers ----

func TestStaleNoTriggerFreshRelease(t *testing.T) {
	api := staleAPI{
		branch: "main",
		releases: []github.Release{
			staleRelease("v2.1", staleTime(2*24), false, "Addon_Forever.zip"),
		},
		tags: []github.Tag{{Name: "v2.1", Commit: github.TagCommit{SHA: "sha21"}}},
		commits: map[string]github.Commit{
			"sha21": staleCommit("sha21", staleTime(3*24)),
			"main":  staleCommit("main", staleTime(5*24)),
		},
	}
	d := runStale(t, api, staleReq())
	wantStale(t, d, false, "release v2.1 is current")
}

// A pre-release newer than the stable release is not staleness: the author
// is actively publishing, and the pre-release's missing flavor asset never
// invalidates coverage a stable release provides.
func TestStaleNoTriggerNewerPrereleaseOnly(t *testing.T) {
	api := staleAPI{
		branch: "main",
		releases: []github.Release{
			staleRelease("v2.0-rc1", staleTime(24), true, "Addon_Mainline.zip"),
			staleRelease("v1.0", staleTime(90*24), false, "Addon_Forever.zip"),
		},
		tags: []github.Tag{{Name: "v2.0-rc1", Commit: github.TagCommit{SHA: "sharc1"}}},
		commits: map[string]github.Commit{
			"sharc1": staleCommit("sharc1", staleTime(30)),
			"main":   staleCommit("main", staleTime(40)),
		},
	}
	d := runStale(t, api, staleReq())
	wantStale(t, d, false, "pre-release v2.0-rc1 covers recent work")
}

// A tag without a release, newer than the latest release but within the
// staleness window, is not staleness.
func TestStaleNoTriggerTagWithinThreshold(t *testing.T) {
	api := staleAPI{
		branch: "main",
		releases: []github.Release{
			staleRelease("v1.0", staleTime(20*24), false, "Addon_Forever.zip"),
		},
		tags: []github.Tag{{Name: "v1.1", Commit: github.TagCommit{SHA: "sha11"}}},
		commits: map[string]github.Commit{
			"sha11": staleCommit("sha11", staleTime(10*24)),
			"main":  staleCommit("main", staleTime(12*24)),
		},
	}
	d := runStale(t, api, staleReq())
	wantStale(t, d, false, "(within 14)")
}

// A stale delta that never touches the flavor's .toc (mainline-only work in
// a monorepo) is not staleness.
func TestStaleNoTriggerTocUntouched(t *testing.T) {
	t.Run("no toc in delta", func(t *testing.T) {
		api := staleTocDeltaAPI(map[string]github.CompareResult{
			"v1.0...v1.1": {Files: []github.CompareFile{{Filename: "MyAddon.lua"}, {Filename: "README.md"}}},
		})
		d := runStale(t, api, staleReq())
		wantStale(t, d, false, "toc untouched since release v1.0")
	})
	t.Run("foreign flavor toc only", func(t *testing.T) {
		api := staleTocDeltaAPI(map[string]github.CompareResult{
			"v1.0...v1.1": {Files: []github.CompareFile{{Filename: "MyAddon_Mainline.toc"}}},
		})
		d := runStale(t, api, staleReq())
		wantStale(t, d, false, "toc untouched since release v1.0")
	})
}

// ---- config knobs: staleness_days and strategy pins ----

func TestStaleStalenessDaysOverride(t *testing.T) {
	api := staleTocDeltaAPI(map[string]github.CompareResult{
		"v1.0...v1.1": {Files: []github.CompareFile{{Filename: "MyAddon_Forever.toc"}}},
	})
	t.Run("default 14 days triggers", func(t *testing.T) {
		wantStale(t, runStale(t, api, staleReq()), true, "touches the forever toc")
	})
	t.Run("configured 60 days does not", func(t *testing.T) {
		req := staleReq()
		req.Staleness = 60 * 24 * time.Hour
		wantStale(t, runStale(t, api, req), false, "(within 60)")
	})
	t.Run("staleness_days config knob", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"staleness_days": 60}`), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		cfg, err := state.LoadConfigFile(path)
		if err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}
		if cfg.Staleness() != 60*24*time.Hour {
			t.Fatalf("Staleness() = %v, want 60 days", cfg.Staleness())
		}
		req := staleReq()
		req.Staleness = cfg.Staleness()
		wantStale(t, runStale(t, api, req), false, "(within 60)")
	})
	t.Run("unset config falls back to 14 days", func(t *testing.T) {
		cfg, err := state.LoadConfigFile(filepath.Join(t.TempDir(), "missing.json"))
		if err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}
		if got := cfg.Staleness(); got != 14*24*time.Hour {
			t.Fatalf("Staleness() = %v, want 14 days", got)
		}
	})
}

func TestStaleStrategyPinning(t *testing.T) {
	api := staleTocDeltaAPI(map[string]github.CompareResult{
		"v1.0...v1.1": {Files: []github.CompareFile{{Filename: "MyAddon_Forever.toc"}}},
	})
	t.Run("pinned to release strategy", func(t *testing.T) {
		req := staleReq()
		req.PinnedStrategy = state.StrategyRelease
		wantStale(t, runStale(t, api, req), false, "pinned to strategy release")
	})
	t.Run("tracking release", func(t *testing.T) {
		req := staleReq()
		req.Tracking = "release"
		wantStale(t, runStale(t, api, req), false, "pinned to strategy release")
	})
	t.Run("pinned to tag", func(t *testing.T) {
		req := staleReq()
		req.Tracking = "v1.1"
		wantStale(t, runStale(t, api, req), false, "pinned to tag v1.1")
	})
	t.Run("config strategy pin", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"strategies": {"demo/addon": "release"}}`), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		cfg, err := state.LoadConfigFile(path)
		if err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}
		pin, ok := cfg.PinnedStrategy("addon", "demo/addon")
		if !ok || pin != state.StrategyRelease {
			t.Fatalf("PinnedStrategy = %q, %v; want release, true", pin, ok)
		}
		req := staleReq()
		req.PinnedStrategy = pin
		wantStale(t, runStale(t, api, req), false, "pinned to strategy release")
	})
}
