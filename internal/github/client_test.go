package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// requestLog records the request headers seen by a stub server. Test handlers
// run on their own goroutine, so every access is guarded by a mutex.
type requestLog struct {
	mu   sync.Mutex
	reqs []loggedRequest
}

type loggedRequest struct {
	path        string
	ifNoneMatch string
	accept      string
	userAgent   string
}

// add records r and returns its 1-based sequence number.
func (l *requestLog) add(r *http.Request) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, loggedRequest{
		path:        r.URL.String(),
		ifNoneMatch: r.Header.Get("If-None-Match"),
		accept:      r.Header.Get("Accept"),
		userAgent:   r.Header.Get("User-Agent"),
	})
	return len(l.reqs)
}

func (l *requestLog) snapshot() []loggedRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]loggedRequest(nil), l.reqs...)
}

const (
	testOwner = "acme"
	testRepo  = "widget"
)

// releasesURL is the exact URL Releases requests; the cache key tests inspect
// is derived from it.
func releasesURL(base string) string {
	return base + "/repos/" + testOwner + "/" + testRepo + "/releases?per_page=10"
}

func newTestClient(t *testing.T, baseURL, cacheDir string, offline bool) *Client {
	t.Helper()
	c, err := New(Options{
		BaseURL:  baseURL,
		CacheDir: cacheDir,
		Offline:  offline,
		CacheTTL: time.Hour,
		Version:  "test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// cachedEntryAt fails the test unless a cache entry exists for rawURL.
func cachedEntryAt(t *testing.T, dir, rawURL string) *cacheEntry {
	t.Helper()
	e := loadCache(cachePath(dir, http.MethodGet, rawURL))
	if e == nil {
		t.Fatalf("no cache entry for %s", rawURL)
	}
	return e
}

// TestETagCacheStoresOn200 covers the first leg of the cache flow: a 200 is
// stored with its ETag and the request carries the documented headers but no
// If-None-Match on a cold cache.
func TestETagCacheStoresOn200(t *testing.T) {
	const body = `[{"tag_name":"v0.9.0","prerelease":true,"published_at":"2026-01-02T03:04:05Z"}]`
	log := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("X-RateLimit-Remaining", "42")
		w.Header().Set("X-RateLimit-Reset", "4102444800")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	c := newTestClient(t, srv.URL, dir, false)

	got, err := c.Releases(context.Background(), testOwner, testRepo)
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if len(got) != 1 || got[0].TagName != "v0.9.0" || !got[0].Prerelease {
		t.Fatalf("Releases = %+v, want one pre-release v0.9.0", got)
	}

	reqs := log.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(reqs))
	}
	if reqs[0].path != "/repos/acme/widget/releases?per_page=10" {
		t.Errorf("request path = %q, want /repos/acme/widget/releases?per_page=10", reqs[0].path)
	}
	if reqs[0].accept != "application/vnd.github+json" {
		t.Errorf("Accept = %q, want application/vnd.github+json", reqs[0].accept)
	}
	if !strings.HasPrefix(reqs[0].userAgent, "bfctl/") {
		t.Errorf("User-Agent = %q, want bfctl/<version>", reqs[0].userAgent)
	}
	if reqs[0].ifNoneMatch != "" {
		t.Errorf("If-None-Match = %q on a cold cache, want empty", reqs[0].ifNoneMatch)
	}

	entry := cachedEntryAt(t, dir, releasesURL(srv.URL))
	if entry.ETag != `"v1"` {
		t.Errorf("cached etag = %q, want %q", entry.ETag, `"v1"`)
	}
	if entry.Status != http.StatusOK {
		t.Errorf("cached status = %d, want 200", entry.Status)
	}
	if string(entry.Body) != body {
		t.Errorf("cached body = %q, want %q", entry.Body, body)
	}
	if entry.FetchedAt.IsZero() {
		t.Error("cached fetched_at is zero")
	}

	rate := c.Rate()
	if !rate.Known || rate.Remaining != 42 {
		t.Errorf("rate = %+v, want known with 42 remaining", rate)
	}
	if !rate.Reset.Equal(time.Unix(4102444800, 0)) {
		t.Errorf("rate reset = %v, want %v", rate.Reset, time.Unix(4102444800, 0))
	}
}

// TestETagRevalidationReplaysCachedBodyOn304 covers the second leg: a warm
// cache sends If-None-Match, a 304 replays the cached body and the
// rate-limit counter stays untouched.
func TestETagRevalidationReplaysCachedBodyOn304(t *testing.T) {
	const body = `[{"tag_name":"v1.2.3","published_at":"2026-02-03T04:05:06Z"}]`
	log := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch log.add(r) {
		case 1:
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("X-RateLimit-Remaining", "42")
			w.Header().Set("X-RateLimit-Reset", "4102444800")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, body)
		case 2:
			// A 304 consumes no quota; these headers must not
			// overwrite the snapshot from the first response.
			w.Header().Set("X-RateLimit-Remaining", "7")
			w.WriteHeader(http.StatusNotModified)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	c := newTestClient(t, srv.URL, dir, false)

	first, err := c.Releases(context.Background(), testOwner, testRepo)
	if err != nil {
		t.Fatalf("Releases (first): %v", err)
	}
	second, err := c.Releases(context.Background(), testOwner, testRepo)
	if err != nil {
		t.Fatalf("Releases (second): %v", err)
	}
	if len(second) != 1 || second[0].TagName != "v1.2.3" {
		t.Fatalf("Releases (second) = %+v, want replayed v1.2.3", second)
	}
	if len(first) != len(second) || first[0].TagName != second[0].TagName {
		t.Errorf("replayed body %+v differs from first response %+v", second, first)
	}

	reqs := log.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(reqs))
	}
	if reqs[1].ifNoneMatch != `"v1"` {
		t.Errorf("If-None-Match on second request = %q, want %q", reqs[1].ifNoneMatch, `"v1"`)
	}

	rate := c.Rate()
	if !rate.Known || rate.Remaining != 42 {
		t.Errorf("rate = %+v, want untouched snapshot with 42 remaining", rate)
	}
}

// TestETagExpiredOrAbsentFallsBackToNetwork covers the fallback leg: without
// a usable cache entry the client fetches unconditionally and refills the
// cache.
func TestETagExpiredOrAbsentFallsBackToNetwork(t *testing.T) {
	const body = `[{"tag_name":"v2.0.0","published_at":"2026-03-04T05:06:07Z"}]`
	newServer := func(t *testing.T) (*httptest.Server, *requestLog) {
		t.Helper()
		log := &requestLog{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.add(r)
			w.Header().Set("ETag", `"v2"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, body)
		}))
		t.Cleanup(srv.Close)
		return srv, log
	}

	t.Run("absent cache", func(t *testing.T) {
		srv, log := newServer(t)
		c := newTestClient(t, srv.URL, t.TempDir(), false)

		got, err := c.Releases(context.Background(), testOwner, testRepo)
		if err != nil {
			t.Fatalf("Releases: %v", err)
		}
		if len(got) != 1 || got[0].TagName != "v2.0.0" {
			t.Fatalf("Releases = %+v, want v2.0.0", got)
		}
		reqs := log.snapshot()
		if len(reqs) != 1 || reqs[0].ifNoneMatch != "" {
			t.Fatalf("requests = %+v, want one unconditional fetch", reqs)
		}
	})

	t.Run("expired cache", func(t *testing.T) {
		srv, log := newServer(t)
		dir := t.TempDir()
		old := &cacheEntry{
			ETag:      `"v0"`,
			FetchedAt: time.Now().Add(-24 * time.Hour),
			Status:    http.StatusOK,
			Body:      []byte(`[{"tag_name":"old"}]`),
		}
		if err := saveCache(cachePath(dir, http.MethodGet, releasesURL(srv.URL)), old); err != nil {
			t.Fatalf("seed cache: %v", err)
		}
		c := newTestClient(t, srv.URL, dir, false)

		got, err := c.Releases(context.Background(), testOwner, testRepo)
		if err != nil {
			t.Fatalf("Releases: %v", err)
		}
		if len(got) != 1 || got[0].TagName != "v2.0.0" {
			t.Fatalf("Releases = %+v, want fresh v2.0.0, not the expired body", got)
		}
		reqs := log.snapshot()
		if len(reqs) != 1 || reqs[0].ifNoneMatch != "" {
			t.Fatalf("requests = %+v, want one unconditional fetch (no If-None-Match)", reqs)
		}
		entry := cachedEntryAt(t, dir, releasesURL(srv.URL))
		if entry.ETag != `"v2"` {
			t.Errorf("cached etag = %q, want refreshed %q", entry.ETag, `"v2"`)
		}
		if entry.FetchedAt.Before(old.FetchedAt) {
			t.Errorf("cached fetched_at = %v, want refreshed", entry.FetchedAt)
		}
	})
}

// TestETagOfflineServesCacheOnly covers the --offline contract: the cache is
// served without any network I/O and a cold cache is a clean error.
func TestETagOfflineServesCacheOnly(t *testing.T) {
	// The base URL points nowhere near a server on purpose: an offline
	// client must never dial it.
	const offlineBase = "https://api.github.invalid"

	t.Run("warm cache served offline", func(t *testing.T) {
		dir := t.TempDir()
		seed := &cacheEntry{
			ETag:      `"v9"`,
			FetchedAt: time.Now(),
			Status:    http.StatusOK,
			Body:      []byte(`[{"tag_name":"cached"}]`),
		}
		if err := saveCache(cachePath(dir, http.MethodGet, releasesURL(offlineBase)), seed); err != nil {
			t.Fatalf("seed cache: %v", err)
		}
		c := newTestClient(t, offlineBase, dir, true)

		got, err := c.Releases(context.Background(), testOwner, testRepo)
		if err != nil {
			t.Fatalf("Releases offline: %v", err)
		}
		if len(got) != 1 || got[0].TagName != "cached" {
			t.Fatalf("Releases offline = %+v, want cached body", got)
		}
	})

	t.Run("cold cache errors offline", func(t *testing.T) {
		c := newTestClient(t, offlineBase, t.TempDir(), true)

		_, err := c.Releases(context.Background(), testOwner, testRepo)
		if !errors.Is(err, ErrOfflineNoCache) {
			t.Fatalf("Releases offline = %v, want ErrOfflineNoCache", err)
		}
	})
}

// TestTypedDecoders pins the JSON shape of the typed decoders against
// representative GitHub payloads.
func TestTypedDecoders(t *testing.T) {
	var releases []Release
	if err := json.Unmarshal([]byte(`[{
		"tag_name": "v1.0.0",
		"draft": false,
		"prerelease": false,
		"published_at": "2026-01-02T03:04:05Z",
		"assets": [{"name": "Addon.zip", "size": 1234, "browser_download_url": "https://example.invalid/Addon.zip", "content_type": "application/zip"}]
	}]`), &releases); err != nil {
		t.Fatalf("decode releases: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("decoded %d releases, want 1", len(releases))
	}
	r := releases[0]
	if r.TagName != "v1.0.0" || r.Draft || r.Prerelease {
		t.Errorf("release = %+v, want stable v1.0.0", r)
	}
	if !r.PublishedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("published_at = %v, want 2026-01-02T03:04:05Z", r.PublishedAt)
	}
	if len(r.Assets) != 1 || r.Assets[0].Name != "Addon.zip" || r.Assets[0].Size != 1234 ||
		r.Assets[0].BrowserDownloadURL != "https://example.invalid/Addon.zip" ||
		r.Assets[0].ContentType != "application/zip" {
		t.Errorf("assets = %+v, want one Addon.zip asset", r.Assets)
	}

	var tags []Tag
	if err := json.Unmarshal([]byte(`[{"name": "v1.0.0", "commit": {"sha": "abc123"}}]`), &tags); err != nil {
		t.Fatalf("decode tags: %v", err)
	}
	if len(tags) != 1 || tags[0].Name != "v1.0.0" || tags[0].Commit.SHA != "abc123" {
		t.Errorf("tags = %+v, want v1.0.0 at abc123", tags)
	}

	var repo Repository
	if err := json.Unmarshal([]byte(`{"default_branch": "main", "pushed_at": "2026-04-05T06:07:08Z"}`), &repo); err != nil {
		t.Fatalf("decode repository: %v", err)
	}
	if repo.DefaultBranch != "main" || !repo.PushedAt.Equal(time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)) {
		t.Errorf("repository = %+v, want main pushed 2026-04-05T06:07:08Z", repo)
	}

	var commit Commit
	if err := json.Unmarshal([]byte(`{"sha": "def456", "commit": {"committer": {"date": "2026-05-06T07:08:09Z"}}}`), &commit); err != nil {
		t.Fatalf("decode commit: %v", err)
	}
	if commit.SHA != "def456" || !commit.Commit.Committer.Date.Equal(time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)) {
		t.Errorf("commit = %+v, want def456 at 2026-05-06T07:08:09Z", commit)
	}
}

// TestParseRetryAfter pins Retry-After parsing for both header forms.
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "empty", value: "", want: 0},
		{name: "seconds", value: "5", want: 5 * time.Second},
		{name: "zero seconds", value: "0", want: 0},
		{name: "negative", value: "-3", want: 0},
		{name: "http date", value: "Mon, 01 Jun 2026 12:00:30 GMT", want: 30 * time.Second},
		{name: "past http date", value: "Mon, 01 Jun 2026 11:00:00 GMT", want: 0},
		{name: "garbage", value: "soon", want: 0},
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.value, now); got != tc.want {
			t.Errorf("%s: parseRetryAfter(%q) = %v, want %v", tc.name, tc.value, got, tc.want)
		}
	}
}

// TestRateLimitErrorHint pins the wait guidance rendered for a rate-limit
// failure: Retry-After when GitHub sent one, the X-RateLimit-Reset window end
// otherwise, and a fallback pointer when no timing was given at all.
func TestRateLimitErrorHint(t *testing.T) {
	now := time.Date(2026, 10, 22, 18, 40, 0, 0, time.UTC)
	reset := now.Add(42*time.Minute + 10*time.Second)
	cases := []struct {
		name string
		err  *RateLimitError
		want []string // every substring must appear in the hint
	}{
		{
			name: "reset in the future",
			err:  &RateLimitError{Reset: reset},
			want: []string{"quota resets at", reset.Local().Format("Mon 15:04:05"), "in 42m10s"},
		},
		{
			name: "reset already past",
			err:  &RateLimitError{Reset: now.Add(-time.Minute)},
			want: []string{"retry now"},
		},
		{
			name: "retry-after preferred over reset",
			err:  &RateLimitError{RetryAfter: 90 * time.Second, Reset: reset},
			want: []string{"retry after 1m30s"},
		},
		{
			name: "no timing at all",
			err:  &RateLimitError{},
			want: []string{"rate_limit"},
		},
	}
	for _, tc := range cases {
		got := tc.err.hint(now)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: hint = %q, want it to contain %q", tc.name, got, want)
			}
		}
		if msg := tc.err.Error(); !strings.HasPrefix(msg, "github: rate limited; ") {
			t.Errorf("%s: Error() = %q, want the rate-limited prefix", tc.name, msg)
		}
	}
}

// TestRateLimitHint covers the wrapped-error lookup the CLI uses to decide
// whether a failure was a rate limit and how long to wait.
func TestRateLimitHint(t *testing.T) {
	wrapped := fmt.Errorf("resolve acme/widget: %w", &RateLimitError{RetryAfter: time.Minute})
	hint, ok := RateLimitHint(wrapped)
	if !ok || !strings.Contains(hint, "retry after 1m0s") {
		t.Fatalf("RateLimitHint(wrapped) = %q, %v; want the retry-after guidance", hint, ok)
	}
	if hint, ok := RateLimitHint(errors.New("boom")); ok {
		t.Fatalf("RateLimitHint(other) = %q, %v; want ok == false", hint, ok)
	}
}

// TestRateLimitedResponseSurfacesReset drives a real 403 rate-limit response
// through the client and checks the error tells the caller how long to wait.
func TestRateLimitedResponseSurfacesReset(t *testing.T) {
	reset := fmt.Sprintf("%d", time.Now().Add(30*time.Minute).Unix())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", reset)
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, t.TempDir(), false)
	_, err := c.Releases(context.Background(), testOwner, testRepo)
	if !IsRateLimited(err) {
		t.Fatalf("Releases error = %v, want a rate-limit error", err)
	}
	hint, ok := RateLimitHint(err)
	if !ok || !strings.Contains(hint, "quota resets at") || !strings.Contains(hint, "in ") {
		t.Fatalf("RateLimitHint = %q, %v; want reset guidance", hint, ok)
	}
	if msg := err.Error(); !strings.Contains(msg, hint) {
		t.Errorf("Error() = %q, want it to carry the hint %q", msg, hint)
	}
}
