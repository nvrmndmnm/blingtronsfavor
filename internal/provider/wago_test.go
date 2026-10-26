package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
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

// fastScrape loosens the scrape pacing and retry knobs so fixture runs do
// not sleep for seconds per request.
func fastScrape(t *testing.T) {
	t.Helper()
	origInterval, origBackoff, origAttempts := scrapeInterval, scrapeBackoffBase, scrapeAttempts
	scrapeInterval, scrapeBackoffBase, scrapeAttempts = time.Millisecond, time.Millisecond, 3
	t.Cleanup(func() {
		scrapeInterval, scrapeBackoffBase, scrapeAttempts = origInterval, origBackoff, origAttempts
	})
}

// wagoFixture reads a recorded data-page fixture and substitutes the
// {{DOWNLOAD}} placeholder with the given package URL. The URL is
// HTML-escaped first: the fixture embeds its payload exactly the way the
// Inertia app does, attribute-escaped inside data-page="...".
func wagoFixture(t *testing.T, name, download string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return strings.ReplaceAll(string(raw), "{{DOWNLOAD}}", html.EscapeString(download))
}

// wagoPageOf wraps one raw props JSON blob the way the Inertia app embeds
// it, for ad-hoc payloads the recorded fixtures do not cover.
func wagoPageOf(props string) string {
	payload := fmt.Sprintf(`{"component":"Addon/Versions","props":%s,"url":"/addons/x/versions"}`, props)
	return `<html><body><div id="app" data-page="` + html.EscapeString(payload) + `"></div></body></html>`
}

// serveWago serves the given page body on the versions endpoint and returns
// the server and its request log.
func serveWago(t *testing.T, page func() string) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var reqs []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r)
		if !strings.HasSuffix(r.URL.Path, "/versions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page()))
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

// TestWagoDataPageParse exercises the payload extraction and release decode
// straight off the recorded fixture: label -> version, inline changelog and
// the per-release flavor fields.
func TestWagoDataPageParse(t *testing.T) {
	fastScrape(t)
	srv, reqs := serveWago(t, func() string {
		return wagoFixture(t, "wago-versions.html", "https://cdn.invalid/pkg.zip")
	})
	w := &Wago{Base: srv.URL, Flavor: client.FlavorMainline}
	up, err := w.Check(context.Background(), state.SourceRef{Kind: state.SourceWago, Slug: "VBNBxKx5"})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if up.Version != "5.20.0" {
		t.Errorf("version = %q, want 5.20.0", up.Version)
	}
	if !strings.Contains(up.Changelog, "## 5.20.0") || !strings.Contains(up.Changelog, "Added widget options") {
		t.Errorf("changelog = %q, want the inline markdown changelog", up.Changelog)
	}
	if up.Strategy != state.StrategyDirect {
		t.Errorf("strategy = %q, want %q", up.Strategy, state.StrategyDirect)
	}
	if up.URL != "https://cdn.invalid/pkg.zip" {
		t.Errorf("url = %q, want the signed download link", up.URL)
	}
	if len(*reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.URL.Path != "/addons/VBNBxKx5/versions" || r.URL.Query().Get("stability") != "stable" {
		t.Errorf("request = %s, want /addons/VBNBxKx5/versions?stability=stable", r.URL)
	}
	if got := r.Header.Get("User-Agent"); got != scrapeUserAgent {
		t.Errorf("user-agent = %q, want %q", got, scrapeUserAgent)
	}
}

// TestWagoFlavorSelection covers the live quirks: a null flavor field means
// "no information" (WeakAuras ships supported_retail_patches: null next to
// populated classic fields), an explicitly empty list means the release does
// not serve the flavor (Raider.IO is retail-only) and an oddly-shaped field
// degrades to "no information" instead of failing the payload.
func TestWagoFlavorSelection(t *testing.T) {
	fastScrape(t)
	props := `{"releases":{"data":[
		{"id":"a","label":"2.0.0","stability":"stable","changelog":"c","download_link":"https://cdn.invalid/a.zip",
		 "supported_retail_patches":null,"supported_classic_patches":["1.15.7"]},
		{"id":"b","label":"1.9.0","stability":"stable","changelog":"c","download_link":"https://cdn.invalid/b.zip",
		 "supported_retail_patches":["12.0.0"],"supported_classic_patches":[]},
		{"id":"c","label":"1.8.0","stability":"stable","changelog":"c","download_link":"https://cdn.invalid/c.zip",
		 "supported_retail_patches":false,"supported_classic_patches":42}
	]}}`
	cases := []struct {
		flavor client.Flavor
		want   string
	}{
		{client.FlavorMainline, "2.0.0"}, // null retail field: no info, accept
		{client.FlavorClassic, "2.0.0"},  // classic listed on the newest release
		{client.FlavorForever, "2.0.0"},  // absent field: no info, accept
	}
	for _, c := range cases {
		srv, _ := serveWago(t, func() string { return wagoPageOf(props) })
		w := &Wago{Base: srv.URL, Flavor: c.flavor}
		up, err := w.Check(context.Background(), state.SourceRef{Kind: state.SourceWago, Slug: "x"})
		if err != nil {
			t.Fatalf("check(%s): %v", c.flavor, err)
		}
		if up.Version != c.want {
			t.Errorf("check(%s) version = %q, want %q", c.flavor, up.Version, c.want)
		}
	}

	// Explicitly empty flavor lists skip releases, so a flavor none of the
	// releases list support for surfaces as a per-addon error.
	srv, _ := serveWago(t, func() string {
		return wagoPageOf(`{"releases":{"data":[
			{"id":"b","label":"1.9.0","stability":"stable","changelog":"c","download_link":"https://cdn.invalid/b.zip","supported_classic_patches":[]}]}}`)
	})
	w := &Wago{Base: srv.URL, Flavor: client.FlavorClassic}
	_, err := w.Check(context.Background(), state.SourceRef{Kind: state.SourceWago, Slug: "x"})
	if err == nil || !strings.Contains(err.Error(), "no stable release supports flavor") {
		t.Errorf("check = %v, want a no-supported-release error", err)
	}
}

// TestWagoCheckErrors covers the failure shapes the scrape must degrade on:
// empty releases, an error-shaped page, a page without a data-page payload
// and a non-200 response — every one a per-addon error, never a panic.
func TestWagoCheckErrors(t *testing.T) {
	fastScrape(t)
	cases := []struct {
		name    string
		page    string
		status  int
		wantErr string
	}{
		{"empty", wagoFixture(t, "wago-versions-empty.html", ""), 200, "no stable releases"},
		{"error-shape", wagoFixture(t, "wago-versions-error.html", ""), 200, "no releases payload"},
		{"no-data-page", wagoFixture(t, "wago-no-page.html", ""), 200, "no data-page payload"},
		{"not-found", "", 404, "404"},
		{"malformed", `<div id="app" data-page="{&quot;props&quot;:"></div>`, 200, "decode data-page payload"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.page))
		}))
		w := &Wago{Base: srv.URL}
		_, err := w.Check(context.Background(), state.SourceRef{Kind: state.SourceWago, Slug: "x"})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: check = %v, want an error containing %q", c.name, err, c.wantErr)
		}
	}

	w := &Wago{}
	if _, err := w.Check(context.Background(), state.SourceRef{Kind: state.SourceWago}); err == nil {
		t.Error("empty slug: check succeeded, want an error")
	}
}

// TestWagoFetchContract is the Fetch request-contract test: Fetch re-reads
// the versions page (fresh pre-signed link, never cached), follows the 302
// to the CDN and streams the zip through the shared unpack path.
func TestWagoFetchContract(t *testing.T) {
	fastScrape(t)
	pkg := makeZip(t, map[string]string{"WeakAuras/WeakAuras.toc": "## Interface: 120100\n"})
	var versionGets atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/versions"):
			versionGets.Add(1)
			page := wagoFixture(t, "wago-versions.html", srv.URL+"/signed/pkg.zip?expires=1793&sig=SECRETSIGNATURE")
			_, _ = w.Write([]byte(page))
		case strings.HasPrefix(r.URL.Path, "/signed/"):
			http.Redirect(w, r, srv.URL+"/cdn/pkg.zip", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/cdn/"):
			_, _ = w.Write(pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	w := &Wago{Base: srv.URL, Flavor: client.FlavorMainline}
	ref := state.SourceRef{Kind: state.SourceWago, Slug: "VBNBxKx5"}
	dest := t.TempDir()
	if err := w.Fetch(context.Background(), ref, dest); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "WeakAuras", "WeakAuras.toc"))
	if err != nil {
		t.Fatalf("read unpacked toc: %v", err)
	}
	if !strings.Contains(string(got), "Interface: 120100") {
		t.Errorf("unpacked toc = %q", got)
	}
	if n := versionGets.Load(); n != 1 {
		t.Errorf("versions page GETs = %d, want exactly 1 per Fetch (fresh link, never cached)", n)
	}
}

// TestWagoFreshLink pins the no-caching contract: every Check or Fetch call
// re-reads the versions page, so the signed link it uses is always the one
// the page just issued.
func TestWagoFreshLink(t *testing.T) {
	fastScrape(t)
	srv, reqs := serveWago(t, func() string {
		return wagoFixture(t, "wago-versions.html", "https://cdn.invalid/pkg.zip")
	})
	w := &Wago{Base: srv.URL, Flavor: client.FlavorMainline}
	ref := state.SourceRef{Kind: state.SourceWago, Slug: "VBNBxKx5"}
	for i := 0; i < 3; i++ {
		if _, err := w.Check(context.Background(), ref); err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
	}
	if len(*reqs) != 3 {
		t.Errorf("versions page GETs = %d, want 3 (one per Check)", len(*reqs))
	}
}

// TestWagoFetchRedactsSignedLink: download errors carry the addon key, never
// the signed link — those links must not reach logs, state or output.
func TestWagoFetchRedactsSignedLink(t *testing.T) {
	fastScrape(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/versions"):
			page := wagoFixture(t, "wago-versions.html", srv.URL+"/signed/pkg.zip?expires=1793&sig=SECRETSIGNATURE")
			_, _ = w.Write([]byte(page))
		default:
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	}))
	defer srv.Close()

	w := &Wago{Base: srv.URL, Flavor: client.FlavorMainline}
	err := w.Fetch(context.Background(), state.SourceRef{Kind: state.SourceWago, Slug: "VBNBxKx5"}, t.TempDir())
	if err == nil {
		t.Fatal("fetch succeeded, want a download error")
	}
	if strings.Contains(err.Error(), "SECRETSIGNATURE") || strings.Contains(err.Error(), "/signed/") {
		t.Errorf("error leaks the signed link: %v", err)
	}
	if !strings.Contains(err.Error(), "wago VBNBxKx5") {
		t.Errorf("error = %v, want the addon key in the message", err)
	}
}

// TestScrapeRetry exercises the shared scrape hygiene: transient 5xx and 429
// responses are retried with backoff and a later success wins.
func TestScrapeRetry(t *testing.T) {
	fastScrape(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch hits.Add(1) {
		case 1:
			http.Error(w, "boom", http.StatusInternalServerError)
		case 2:
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	resp, err := scrapeGet(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("scrapeGet: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %s, want 200 after retries", resp.Status)
	}
}

// TestWagoDecodesRelease keeps the defensive decode honest: unknown fields
// are ignored and the flavor patch map survives a round trip through JSON.
func TestWagoDecodesRelease(t *testing.T) {
	raw := `{"id":"x","label":"1.2.3","stability":"stable","changelog":"note",` +
		`"download_link":"https://cdn.invalid/x.zip","size":12,"ignored":{"a":1},` +
		`"supported_retail_patches":["12.0.0"],"supported_classic_patches":null}`
	var rel wagoRelease
	if err := json.Unmarshal([]byte(raw), &rel); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rel.Label != "1.2.3" || rel.Changelog != "note" || rel.Download != "https://cdn.invalid/x.zip" {
		t.Errorf("release = %+v", rel)
	}
	if !rel.supports(client.FlavorMainline) {
		t.Error("retail patches listed, want supports(mainline)")
	}
	if !rel.supports(client.FlavorClassic) {
		t.Error("null classic patches = no information, want supports(classic)")
	}
}
