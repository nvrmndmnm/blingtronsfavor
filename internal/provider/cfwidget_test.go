package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/state"
)

// cfwidgetFixture reads a recorded mirror payload fixture.
func cfwidgetFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(raw)
}

// serveCfwidget serves the given payload body on every request and returns
// the server and its request log.
func serveCfwidget(t *testing.T, payload func() string) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var reqs []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(payload()))
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func cfRef(slug string) state.SourceRef {
	return state.SourceRef{Kind: state.SourceCfwidget, Slug: slug}
}

// TestCfwidgetCheckPicksNewestFlavorFile exercises the recorded payload
// shape end to end: per flavor the newest release file serving that
// version line wins, newer pre-releases and newer files of unserved lines
// are never picked, and every checked flavor resolves the same multi-line
// package the mirror lists.
func TestCfwidgetCheckPicksNewestFlavorFile(t *testing.T) {
	fastScrape(t)
	srv, _ := serveCfwidget(t, func() string { return cfwidgetFixture(t, "cfwidget-details.json") })

	want := Upstream{
		Version:   "Details.20261015.15339.172",
		Changelog: "https://www.curseforge.com/wow/addons/details/files/9202791",
		Strategy:  state.StrategyDirect,
		URL:       "https://mediafilez.forgecdn.net/files/9202/791/Details-Details.20261015.15339.172.zip",
	}
	for _, flavor := range []client.Flavor{client.FlavorMainline, client.FlavorForever, client.FlavorClassic, client.FlavorMists} {
		c := &Cfwidget{Base: srv.URL, Flavor: flavor}
		got, err := c.Check(context.Background(), cfRef("61284"))
		if err != nil {
			t.Fatalf("check %s: %v", flavor, err)
		}
		if got != want {
			t.Errorf("check %s = %+v, want %+v", flavor, got, want)
		}
	}

	// Unfiltered takes the newest release of any version line: the
	// Cataclysm-only row, dated after the beta — and still not the beta.
	c := &Cfwidget{Base: srv.URL}
	got, err := c.Check(context.Background(), cfRef("61284"))
	if err != nil {
		t.Fatalf("check unfiltered: %v", err)
	}
	if got.Version != "Details.20261017.15401.174" {
		t.Errorf("unfiltered version = %q, want the newest release of any line", got.Version)
	}
}

// TestCfwidgetUnservedLinesAndPrerelease: a project whose release files
// only serve version lines no bfctl flavor serves, and whose only
// flavor-matching file is a beta, yields no file at all — pre-releases are
// never installed and unserved lines are never forced onto a client.
func TestCfwidgetUnservedLinesAndPrerelease(t *testing.T) {
	fastScrape(t)
	srv, _ := serveCfwidget(t, func() string { return cfwidgetFixture(t, "cfwidget-unserved.json") })

	for _, flavor := range []client.Flavor{client.FlavorMainline, client.FlavorClassic, client.FlavorForever, client.FlavorMists} {
		c := &Cfwidget{Base: srv.URL, Flavor: flavor}
		_, err := c.Check(context.Background(), cfRef("40001"))
		if err == nil || !strings.Contains(err.Error(), "no release file serves flavor") {
			t.Errorf("check %s = %v, want a no-release-file error", flavor, err)
		}
	}
}

// TestCfwidgetExactInterfaceMatch pins the risk-note fallback: a file
// serving only a version line the flavor band table does not know yet is
// still picked when it names exactly the client version bfctl runs.
func TestCfwidgetExactInterfaceMatch(t *testing.T) {
	fastScrape(t)
	payload := `{"id":40004,"title":"Forever38","files":[{"id":8000001,` +
		`"url":"https://www.curseforge.com/wow/addons/forever38/files/8000001",` +
		`"display":"F-1","name":"F-1.zip","type":"release","version":"3.80.2",` +
		`"versions":["3.80.2"],"uploaded_at":"2026-10-01T00:00:00.000+00:00"}],` +
		`"urls":{"curseforge":"https://www.curseforge.com/wow/addons/forever38"}}`
	srv, _ := serveCfwidget(t, func() string { return payload })

	// The band table alone never matches 3.80.2.
	c := &Cfwidget{Base: srv.URL, Flavor: client.FlavorForever}
	if _, err := c.Check(context.Background(), cfRef("40004")); err == nil {
		t.Error("check without Interface picked a 3.80.2-only file")
	}
	// A client whose own game version is 3.80.2 gets it via the exact
	// interface match.
	c = &Cfwidget{Base: srv.URL, Flavor: client.FlavorForever, Interface: 38002}
	got, err := c.Check(context.Background(), cfRef("40004"))
	if err != nil {
		t.Fatalf("check with Interface: %v", err)
	}
	if got.Version != "F-1" {
		t.Errorf("version = %q, want F-1", got.Version)
	}
}

// TestCfwidgetErrorShapes pins the mirror's failure shapes: the empty body
// 404 of an unknown project, a malformed payload, a payload with no files,
// and a source ref without a project id. No URL is echoed into any error.
func TestCfwidgetErrorShapes(t *testing.T) {
	fastScrape(t)

	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // the mirror answers 404 with an empty body
	}))
	t.Cleanup(notFound.Close)
	c := &Cfwidget{Base: notFound.URL, Flavor: client.FlavorMainline}
	_, err := c.Check(context.Background(), cfRef("999999"))
	if err == nil || !strings.Contains(err.Error(), "mirror returned 404") {
		t.Errorf("404 error = %v, want a mirror-returned-404 error", err)
	}

	srv, _ := serveCfwidget(t, func() string { return cfwidgetFixture(t, "cfwidget-malformed.json") })
	dir := t.TempDir()
	c = &Cfwidget{Base: srv.URL, CacheDir: dir, Flavor: client.FlavorMainline}
	_, err = c.Check(context.Background(), cfRef("40002"))
	if err == nil || !strings.Contains(err.Error(), "malformed cfwidget payload") {
		t.Errorf("malformed error = %v, want a malformed-payload error", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("cache dir = %v, want no entry for a malformed payload", entries)
	}

	srv2, _ := serveCfwidget(t, func() string { return cfwidgetFixture(t, "cfwidget-empty.json") })
	c = &Cfwidget{Base: srv2.URL, Flavor: client.FlavorMainline}
	_, err = c.Check(context.Background(), cfRef("40003"))
	if err == nil || !strings.Contains(err.Error(), "no release file") {
		t.Errorf("empty error = %v, want a no-release-file error", err)
	}

	c = &Cfwidget{Base: srv.URL, Flavor: client.FlavorMainline}
	_, err = c.Check(context.Background(), cfRef("  "))
	if err == nil || !strings.Contains(err.Error(), "carries no project id") {
		t.Errorf("empty-slug error = %v, want a no-project-id error", err)
	}
}

// TestCfwidgetFileURL pins the open-CDN URL builder: one place constructs
// the /files/{fileId/1000}/{fileId%1000}/{exact filename} link, filename
// escaping included.
func TestCfwidgetFileURL(t *testing.T) {
	tests := []struct {
		id   int
		name string
		want string
	}{
		{9202791, "Details-Details.20261015.15339.172.zip",
			"https://mediafilez.forgecdn.net/files/9202/791/Details-Details.20261015.15339.172.zip"},
		{1234, "a b.zip", "https://mediafilez.forgecdn.net/files/1/234/a%20b.zip"},
		{1000500, "x%y.zip", "https://mediafilez.forgecdn.net/files/1000/500/x%25y.zip"},
	}
	c := &Cfwidget{}
	for _, tt := range tests {
		if got := c.fileURL(tt.id, tt.name); got != tt.want {
			t.Errorf("fileURL(%d, %q) = %q, want %q", tt.id, tt.name, got, tt.want)
		}
	}
	// A CDN override (tests) replaces the root.
	c = &Cfwidget{CDN: "http://cdn.invalid"}
	if got := c.fileURL(1, "p.zip"); got != "http://cdn.invalid/files/0/1/p.zip" {
		t.Errorf("fileURL with override = %q", got)
	}
}

// TestCfwidgetFetchContract checks the wire contract of a fetch: one GET
// per mirror payload with the scraper User-Agent, the constructed CDN path
// with the exact filename, and the zip streamed through the shared unpack
// path.
func TestCfwidgetFetchContract(t *testing.T) {
	fastScrape(t)
	pkg := makeZip(t, map[string]string{"Details/Details.toc": "## Interface: 120105\n"})
	var mirrorGets, cdnGets atomic.Int32
	var cdnPath, mirrorUA atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/61284"):
			mirrorGets.Add(1)
			mirrorUA.Store(r.UserAgent())
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(cfwidgetFixture(t, "cfwidget-details.json")))
		case strings.HasPrefix(r.URL.Path, "/files/"):
			cdnGets.Add(1)
			cdnPath.Store(r.URL.Path)
			_, _ = w.Write(pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := &Cfwidget{Base: srv.URL, CDN: srv.URL, Flavor: client.FlavorMainline}
	dest := t.TempDir()
	if err := c.Fetch(context.Background(), cfRef("61284"), dest); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "Details", "Details.toc"))
	if err != nil {
		t.Fatalf("read unpacked toc: %v", err)
	}
	if !strings.Contains(string(got), "Interface: 120105") {
		t.Errorf("unpacked toc = %q", got)
	}
	if n := mirrorGets.Load(); n != 1 {
		t.Errorf("mirror GETs = %d, want 1", n)
	}
	if n := cdnGets.Load(); n != 1 {
		t.Errorf("CDN GETs = %d, want 1", n)
	}
	if p, _ := cdnPath.Load().(string); p != "/files/9202/791/Details-Details.20261015.15339.172.zip" {
		t.Errorf("CDN path = %q, want the constructed files URL", p)
	}
	if ua, _ := mirrorUA.Load().(string); ua != scrapeUserAgent {
		t.Errorf("mirror User-Agent = %q, want %q", ua, scrapeUserAgent)
	}
}

// TestCfwidgetCacheTTL pins the load hygiene: within the TTL a sync costs
// zero cfwidget calls per project, an expired payload is refetched, and a
// slug-keyed request goes to the documented slug form.
func TestCfwidgetCacheTTL(t *testing.T) {
	fastScrape(t)
	var gets atomic.Int32
	srv, _ := serveCfwidget(t, func() string {
		gets.Add(1)
		return cfwidgetFixture(t, "cfwidget-details.json")
	})

	dir := t.TempDir()
	c := &Cfwidget{Base: srv.URL, CacheDir: dir, Flavor: client.FlavorMainline}
	for i := 0; i < 3; i++ {
		if _, err := c.Check(context.Background(), cfRef("61284")); err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
	}
	if n := gets.Load(); n != 1 {
		t.Errorf("mirror GETs = %d, want 1 (payload served from the TTL cache)", n)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("cache dir = %v, want exactly one entry", entries)
	}

	// A client whose TTL expired on arrival refetches.
	c = &Cfwidget{Base: srv.URL, CacheDir: dir, CacheTTL: time.Nanosecond, Flavor: client.FlavorMainline}
	if _, err := c.Check(context.Background(), cfRef("61284")); err != nil {
		t.Fatalf("expired check: %v", err)
	}
	if n := gets.Load(); n != 2 {
		t.Errorf("mirror GETs = %d, want 2 (expired entry refetched)", n)
	}

	// The slug form goes to /wow/addons/{slug}.
	srv2, reqs := serveCfwidget(t, func() string {
		return cfwidgetFixture(t, "cfwidget-details.json")
	})
	c2 := &Cfwidget{Base: srv2.URL, Flavor: client.FlavorMainline}
	if _, err := c2.Check(context.Background(), cfRef("details")); err != nil {
		t.Fatalf("slug check: %v", err)
	}
	if p := (*reqs)[0].URL.Path; p != "/wow/addons/details" {
		t.Errorf("slug request path = %q, want /wow/addons/details", p)
	}
}

// TestCfwidgetRedactsErrors: fetch failures carry the addon key and the
// package label downloadAs formats, never a URL.
func TestCfwidgetRedactsErrors(t *testing.T) {
	fastScrape(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/61284") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(cfwidgetFixture(t, "cfwidget-details.json")))
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	c := &Cfwidget{Base: srv.URL, CDN: srv.URL, Flavor: client.FlavorMainline}
	err := c.Fetch(context.Background(), cfRef("61284"), t.TempDir())
	if err == nil {
		t.Fatal("fetch succeeded, want a download error")
	}
	if strings.Contains(err.Error(), srv.URL) || strings.Contains(err.Error(), "/files/") {
		t.Errorf("error leaks the download URL: %v", err)
	}
	if !strings.Contains(err.Error(), "cfwidget 61284") {
		t.Errorf("error = %v, want the project id in the message", err)
	}
	if !strings.Contains(err.Error(), "cfwidget 61284 package") {
		t.Errorf("error = %v, want the cfwidget package label", err)
	}
}
