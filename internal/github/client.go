// Package github is a minimal, unauthenticated GitHub REST API client with
// an ETag-based HTTP response cache. Every request goes through the cache
// layer: a fresh cached entry is revalidated with If-None-Match, and a 304
// replays the cached body without consuming rate-limit quota. The client
// reads no credentials from the environment or from any tool configuration;
// it talks to the public API only.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itp5b5/blessforge/internal/paths"
)

const (
	// DefaultBaseURL is the GitHub REST API root.
	DefaultBaseURL = "https://api.github.com"

	// DefaultTimeout bounds a single API request.
	DefaultTimeout = 15 * time.Second

	// DefaultCacheTTL is how long a cached response stays fresh enough to
	// revalidate with If-None-Match. Older entries are refetched without a
	// conditional header.
	DefaultCacheTTL = time.Hour

	// DefaultConcurrency bounds the number of API requests in flight per
	// Client.
	DefaultConcurrency = 4

	// acceptHeader is the media type GitHub asks for on REST calls.
	acceptHeader = "application/vnd.github+json"

	// MaxCompareFiles is GitHub's cap on the files[] list of a compare
	// response: a delta touching more files than this reports only the first
	// window, so callers cannot tell what the full delta contained.
	MaxCompareFiles = 300

	// maxBodyBytes caps how much response body is read into the cache;
	// larger bodies are truncated and will fail JSON decoding.
	maxBodyBytes = 8 << 20
)

// Options configures a Client. The zero value is ready to use.
type Options struct {
	// BaseURL overrides the API root (tests only). Defaults to
	// DefaultBaseURL.
	BaseURL string

	// HTTPClient performs the requests. Defaults to a plain http.Client;
	// per-request timeouts are applied via context either way.
	HTTPClient *http.Client

	// CacheDir is the directory holding the HTTP cache entries. Defaults
	// to paths.HTTPCacheDir().
	CacheDir string

	// Timeout bounds a single API request. Defaults to DefaultTimeout
	// (15s per request).
	Timeout time.Duration

	// CacheTTL is the freshness window for cached responses. Defaults to
	// DefaultCacheTTL.
	CacheTTL time.Duration

	// Concurrency bounds the number of requests in flight. Defaults to
	// DefaultConcurrency.
	Concurrency int

	// Offline serves cached responses only and never touches the network.
	Offline bool

	// Version is the bfctl version baked into the User-Agent header
	// ("bfctl/<version>").
	Version string
}

// RateLimit is the most recent rate-limit snapshot observed from response
// headers. Responses answered via 304 revalidation consume no quota and leave
// this snapshot untouched.
type RateLimit struct {
	Remaining int
	Reset     time.Time
	Known     bool
}

// ErrOfflineNoCache is wrapped into errors returned when a request would
// need the network but the client runs offline and has no cached response.
var ErrOfflineNoCache = errors.New("no cached response available offline")

// APIError is returned for GitHub API responses that could not satisfy a
// request, e.g. 404 for an unknown repository.
type APIError struct {
	Method  string
	URL     string
	Status  int
	Message string // GitHub's JSON "message" field, when present
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("github: %s %s: %d: %s", e.Method, e.URL, e.Status, e.Message)
	}
	return fmt.Sprintf("github: %s %s: %d", e.Method, e.URL, e.Status)
}

// RateLimitError is returned when GitHub rate-limits a request (403/429).
// RetryAfter carries the honoured Retry-After header and Reset the end of the
// current rate-limit window; callers honour Retry-After by backing off and
// fail soft per addon instead of aborting the whole run.
type RateLimitError struct {
	RetryAfter time.Duration
	Reset      time.Time
}

// Error implements the error interface.
func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("github: rate limited; retry after %s", e.RetryAfter.Round(time.Second))
	}
	return "github: rate limited"
}

// IsRateLimited reports whether err is or wraps a *RateLimitError.
func IsRateLimited(err error) bool {
	var rle *RateLimitError
	return errors.As(err, &rle)
}

// Client is a GitHub API client with an ETag cache. It is safe for concurrent
// use; requests in flight are bounded by Options.Concurrency.
type Client struct {
	httpClient *http.Client
	baseURL    string
	cacheDir   string
	userAgent  string
	timeout    time.Duration
	cacheTTL   time.Duration
	offline    bool

	sem chan struct{} // bounds concurrent requests

	mu   sync.Mutex
	rate RateLimit
}

// New builds a Client from opts. It resolves the cache directory lazily
// (creating it with 0700 on first use) but performs no network I/O.
func New(opts Options) (*Client, error) {
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	cacheDir := opts.CacheDir
	if cacheDir == "" {
		var err error
		if cacheDir, err = paths.HTTPCacheDir(); err != nil {
			return nil, fmt.Errorf("resolve http cache dir: %w", err)
		}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ttl := opts.CacheTTL
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	return &Client{
		httpClient: hc,
		baseURL:    strings.TrimRight(baseURL, "/"),
		cacheDir:   cacheDir,
		userAgent:  "bfctl/" + version,
		timeout:    timeout,
		cacheTTL:   ttl,
		offline:    opts.Offline,
		sem:        make(chan struct{}, concurrency),
	}, nil
}

// Releases returns the ten most recent releases of owner/repo. The list
// endpoint is used on purpose — /releases/latest alone would silently skip
// pre-releases and rolling builds — so pre-releases come back alongside
// stable releases.
func (c *Client) Releases(ctx context.Context, owner, repo string) ([]Release, error) {
	var releases []Release
	path := fmt.Sprintf("/repos/%s/%s/releases?per_page=10", owner, repo)
	if err := c.get(ctx, path, &releases); err != nil {
		return nil, err
	}
	return releases, nil
}

// Tags returns the ten most recent tags of owner/repo together with the
// commits they point at.
func (c *Client) Tags(ctx context.Context, owner, repo string) ([]Tag, error) {
	var tags []Tag
	path := fmt.Sprintf("/repos/%s/%s/tags?per_page=10", owner, repo)
	if err := c.get(ctx, path, &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// Repository returns repository metadata: the default branch name and the
// time of the last push.
func (c *Client) Repository(ctx context.Context, owner, repo string) (*Repository, error) {
	var repoInfo Repository
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)
	if err := c.get(ctx, path, &repoInfo); err != nil {
		return nil, err
	}
	return &repoInfo, nil
}

// HeadCommit returns the commit branch currently points at, including its
// committer timestamp.
func (c *Client) HeadCommit(ctx context.Context, owner, repo, branch string) (*Commit, error) {
	var commit Commit
	path := fmt.Sprintf("/repos/%s/%s/commits/%s", owner, repo, url.PathEscape(branch))
	if err := c.get(ctx, path, &commit); err != nil {
		return nil, err
	}
	return &commit, nil
}

// Compare compares base...head (three-dot: merge-base to head) and returns
// the changed files. GitHub caps the files list at MaxCompareFiles entries;
// a caller that needs the complete delta must treat a full window as
// incomplete.
func (c *Client) Compare(ctx context.Context, owner, repo, base, head string) (*CompareResult, error) {
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s", owner, repo, url.PathEscape(base), url.PathEscape(head))
	var cmp CompareResult
	if err := c.get(ctx, path, &cmp); err != nil {
		return nil, err
	}
	return &cmp, nil
}

// Rate reports the most recent rate-limit snapshot. Only non-304 responses
// update it: revalidations consume no quota and leave the counters alone.
func (c *Client) Rate() RateLimit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rate
}

// get performs one cached GET and decodes the JSON response body into out.
// A fresh cached entry is revalidated with If-None-Match and replayed on 304;
// an absent or expired cache entry falls back to an unconditional network
// fetch. In offline mode the cached body is served without any network I/O.
func (c *Client) get(ctx context.Context, path string, out any) error {
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()

	reqURL := c.baseURL + path
	key := cachePath(c.cacheDir, http.MethodGet, reqURL)
	entry := loadCache(key)

	if c.offline {
		if entry == nil {
			return fmt.Errorf("github: GET %s: %w", reqURL, ErrOfflineNoCache)
		}
		return json.Unmarshal(entry.Body, out)
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("github: build request for %s: %w", reqURL, err)
	}
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("User-Agent", c.userAgent)
	if entry != nil && entry.ETag != "" && !entry.expired(c.cacheTTL, time.Now()) {
		req.Header.Set("If-None-Match", entry.ETag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github: GET %s: %w", reqURL, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		// A 304 replays the cached body and consumes no rate-limit
		// quota, so the rate counters are left untouched; only the
		// freshness stamp is refreshed.
		if entry == nil {
			return fmt.Errorf("github: GET %s: 304 without a cached body", reqURL)
		}
		entry.FetchedAt = time.Now()
		if err := saveCache(key, entry); err != nil {
			return err
		}
		return json.Unmarshal(entry.Body, out)

	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		if err != nil {
			return fmt.Errorf("github: read response %s: %w", reqURL, err)
		}
		c.observeRate(resp.Header)
		next := &cacheEntry{
			ETag:      resp.Header.Get("ETag"),
			FetchedAt: time.Now(),
			Status:    resp.StatusCode,
			Body:      body,
		}
		if err := saveCache(key, next); err != nil {
			return err
		}
		return json.Unmarshal(body, out)
	}

	// Error responses are never cached; 403/429 surface the honoured
	// Retry-After so callers can back off and fail soft per addon.
	c.observeRate(resp.Header)
	if rateLimited(resp) {
		return &RateLimitError{
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
			Reset:      parseRateReset(resp.Header),
		}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	var payload struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &payload)
	return &APIError{
		Method:  http.MethodGet,
		URL:     reqURL,
		Status:  resp.StatusCode,
		Message: payload.Message,
	}
}

// acquire blocks until a request slot is free or ctx is done.
func (c *Client) acquire(ctx context.Context) error {
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("github: wait for request slot: %w", ctx.Err())
	}
}

// release frees a request slot.
func (c *Client) release() { <-c.sem }

// observeRate folds the X-RateLimit-* headers into the client snapshot.
// 304 responses must not call it: they consume no quota.
func (c *Client) observeRate(h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v := h.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.rate.Remaining = n
			c.rate.Known = true
		}
	}
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.rate.Reset = time.Unix(n, 0)
		}
	}
}

// rateLimited reports whether a response is a rate-limit response rather
// than a plain 403 permission error.
func rateLimited(resp *http.Response) bool {
	return resp.StatusCode == http.StatusTooManyRequests ||
		resp.Header.Get("Retry-After") != "" ||
		resp.Header.Get("X-RateLimit-Remaining") == "0"
}

// parseRetryAfter interprets a Retry-After header value: either delta
// seconds or an HTTP date. Missing or unparsable values yield zero.
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// parseRateReset reads the X-RateLimit-Reset header as a point in time;
// a missing or unparsable value yields the zero time.
func parseRateReset(h http.Header) time.Time {
	v := h.Get("X-RateLimit-Reset")
	if v == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0)
}
