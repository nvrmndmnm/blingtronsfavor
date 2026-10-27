package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/state"
)

// Cfwidget installs addons from CurseForge through the cfwidget.com mirror
// instead of curseforge.com itself (rest-api → download). The mirror's
// project endpoint
//
//	GET https://api.cfwidget.com/{projectId}
//
// answers with the project metadata plus the exact zip filename and file id
// of every uploaded file, keyed to the game version lines each file serves
// ("files[]", "versions"), so everything a sync needs arrives in one GET —
// the API works for the numeric project ID straight from a TOC file's
// "## X-Curse-Project-ID" (the /wow/addons/{slug} form takes a project slug
// instead). Packages come from CurseForge's open CDN (mediafilez.forgecdn.net)
// at /files/{fileId/1000}/{fileId%1000}/{exactFilename} — constructed here
// by fileURL — so no CurseForge credentials or cookies are used or
// needed and links never expire.
//
// CurseForge's distribution terms are gray here: authors can toggle
// "allowModDistribution" off on the website, disabling third-party
// distribution. bfctl deliberately does NOT honor that flag. The cfwidget
// endpoint does not report it, and checking it would require scraping
// curseforge.com itself, which this provider refuses to do — the whole
// point of the mirror pipeline is to never touch the site's HTML. This is
// meant for private personal use on the player's own machine; redistribution
// of the downloaded packages is not intended. Rate and etiquette notes are
// in docs/scraping-strategy-research.md.
type Cfwidget struct {
	// HTTP is the client used for both the mirror and the CDN; nil
	// selects a bounded default.
	HTTP *http.Client

	// Base overrides the mirror root (tests point it at a stub).
	Base string

	// CDN overrides the media CDN root (tests point it at a stub).
	CDN string

	// Flavor selects the client line whose files are picked (a file is
	// picked when one of its game versions maps to the flavor's version
	// line, or names exactly the client version Interface describes).
	// Zero value takes the newest release of any version line.
	Flavor client.Flavor

	// Interface is the client interface version written into staged .toc
	// files (e.g. 16001 for a WoW Forever 1.60.1 client). A file listing
	// exactly this interface among its game versions counts as
	// flavor-matching even when its version line is not in the flavor band
	// table yet.
	Interface int

	// CacheDir holds the TTL cache of mirror payloads so a sync costs at
	// most one cfwidget call per project per CacheTTL. Empty disables the
	// cache and every Check/Fetch re-reads the mirror.
	CacheDir string

	// CacheTTL is how long a cached payload stays fresh. Zero means
	// defaultCfwidgetCacheTTL.
	CacheTTL time.Duration
}

const (
	// defaultCfwidgetBase is the cfwidget.com mirror root.
	defaultCfwidgetBase = "https://api.cfwidget.com"

	// defaultCfwidgetCDN is CurseForge's open, unsigned media CDN.
	defaultCfwidgetCDN = "https://mediafilez.forgecdn.net"

	// defaultCfwidgetCacheTTL is the default mirror payload cache
	// lifetime. The mirror crawls CurseForge for new releases within
	// hours, so a payload older than an hour is refetched without a
	// conditional header.
	defaultCfwidgetCacheTTL = time.Hour

	// maxPayloadBytes caps the mirror payload read into memory: the
	// files[] list of a heavily uploaded project runs to megabytes, and
	// a runaway body must not fill the disk or the cache.
	maxPayloadBytes = 32 << 20
)

// cfPayload mirrors the mirror's project payload fields bfctl uses. The
// endpoint reports more (categories, members, description, the full
// versions map); unknown fields are ignored on purpose. The download{}
// shortcut block is ignored too: the mirror reports an arbitrary file
// there (observed pointing at an alpha while release files existed), so
// files[] is the only file source.
type cfPayload struct {
	ID    int               `json:"id"`
	Title string            `json:"title"`
	Files []cfFile          `json:"files"`
	URLs  map[string]string `json:"urls"`
}

// cfFile is one uploaded file row of the payload's files[] list.
type cfFile struct {
	ID   int    `json:"id"`
	URL  string `json:"url"`  // per-file CurseForge page (the changelog lives there)
	Name string `json:"name"` // exact zip filename, byte for byte
	Type string `json:"type"` // "release", "beta" or "alpha"
	// Display is the author's version label ("Details.20261015.15339.172").
	Display string `json:"display"`
	// Version is the game version the file was primarily built for.
	Version string `json:"version"`
	// Versions lists every game version line the file serves.
	Versions []string `json:"versions"`
	// Uploaded is the upload timestamp ("2026-10-15T16:38:34.897+00:00").
	Uploaded time.Time `json:"uploaded_at"`
}

// Check resolves the project to its newest flavor-matching release file and
// reports the upstream state: the author's display version as the version,
// the per-file CurseForge page as the changelog (the changelog lives there,
// and it is a project page — never a downloaded link URL), and the
// constructed open-CDN URL as the download.
func (c *Cfwidget) Check(ctx context.Context, ref state.SourceRef) (Upstream, error) {
	p, err := c.payload(ctx, ref)
	if err != nil {
		return Upstream{}, err
	}
	f, err := c.pick(p)
	if err != nil {
		return Upstream{}, fmt.Errorf("cfwidget %s: %w", ref.Slug, err)
	}
	return Upstream{
		Version:   fileVersion(f),
		Changelog: fileChangelog(p, f),
		Strategy:  state.StrategyDirect,
		URL:       c.fileURL(f.ID, f.Name),
	}, nil
}

// Fetch downloads the zip of the same file Check reports and unpacks it
// into dest. The URL is rebuilt from the payload on every call — like the
// wago provider refreshes its signed links — so nothing expired can ever be
// fetched from state.
func (c *Cfwidget) Fetch(ctx context.Context, ref state.SourceRef, dest string) error {
	p, err := c.payload(ctx, ref)
	if err != nil {
		return err
	}
	f, err := c.pick(p)
	if err != nil {
		return fmt.Errorf("cfwidget %s: %w", ref.Slug, err)
	}
	if _, err := downloadAs(ctx, c.client(), c.fileURL(f.ID, f.Name), "cfwidget "+ref.Slug+" package", dest); err != nil {
		return fmt.Errorf("cfwidget %s: %w", ref.Slug, err)
	}
	return nil
}

// fileURL builds the open-CDN download URL for one uploaded file: the CDN
// serves files at /files/{fileId/1000}/{fileId%1000}/{exact filename}, so
// the link is constructed from the mirror's file id and name and never
// expires. This is the single URL builder for the cfwidget pipeline — a
// format change is a one-line fix here.
func (c *Cfwidget) fileURL(fileID int, name string) string {
	base := c.CDN
	if base == "" {
		base = defaultCfwidgetCDN
	}
	return fmt.Sprintf("%s/files/%d/%d/%s", base, fileID/1000, fileID%1000, url.PathEscape(name))
}

// pick returns the newest release file serving c.Flavor: files[] is the
// mirror's upload history and its order is not guaranteed, so the newest
// matching row is picked by upload time (file id breaks timestamp ties).
// Only "release" files count — pre-releases are never installed — and a row
// without a CDN name cannot be fetched at all.
func (c *Cfwidget) pick(p cfPayload) (cfFile, error) {
	var best cfFile
	found := false
	for _, f := range p.Files {
		if f.Type != "release" || f.Name == "" || f.ID <= 0 {
			continue
		}
		if !c.serves(f) {
			continue
		}
		if !found || newerFile(f, best) {
			best, found = f, true
		}
	}
	if !found {
		if c.Flavor == "" {
			return cfFile{}, errors.New("no release file")
		}
		return cfFile{}, fmt.Errorf("no release file serves flavor %s", c.Flavor)
	}
	return best, nil
}

// newerFile orders uploaded files by upload time, breaking ties by file id.
func newerFile(a, b cfFile) bool {
	if !a.Uploaded.Equal(b.Uploaded) {
		return a.Uploaded.After(b.Uploaded)
	}
	return a.ID > b.ID
}

// serves reports whether file f was built for c.Flavor: the mirror lists,
// per file, every game version line it serves ("12.1.5", "1.15.9",
// "1.60.1"), and f matches when one of those lines belongs to the flavor's
// version line (the interface band table) or names exactly the client
// version bfctl runs (Interface) — the latter keeps files serving a brand
// new version line usable before the band table learns it. The single
// Version field backs files whose versions[] list is empty.
func (c *Cfwidget) serves(f cfFile) bool {
	if c.Flavor == "" {
		return true // unfiltered: newest release of any version line
	}
	versions := f.Versions
	if len(versions) == 0 && f.Version != "" {
		versions = []string{f.Version}
	}
	for _, gv := range versions {
		if c.Interface != 0 {
			if iface, ok := client.GameVersionInterface(gv); ok && iface == c.Interface {
				return true
			}
		}
		if flavor, ok := client.FlavorForGameVersion(gv); ok && flavor == c.Flavor {
			return true
		}
	}
	return false
}

// fileVersion is the upstream version label: the author's display version,
// falling back to the zip filename.
func fileVersion(f cfFile) string {
	if f.Display != "" {
		return f.Display
	}
	return f.Name
}

// fileChangelog is the per-file CurseForge page — the changelog lives there
// — falling back to the project page. It is a project URL the user can
// open; never a downloaded link URL.
func fileChangelog(p cfPayload, f cfFile) string {
	if f.URL != "" {
		return f.URL
	}
	return p.URLs["curseforge"]
}

// payload fetches the mirror's project payload: one cfwidget call per
// project per CacheTTL when CacheDir is set, every call otherwise. Only
// payloads that parse are cached, so a malformed body never replays.
func (c *Cfwidget) payload(ctx context.Context, ref state.SourceRef) (cfPayload, error) {
	rawURL, err := c.projectURL(ref.Slug)
	if err != nil {
		return cfPayload{}, err
	}
	if body, ok := c.cached(rawURL); ok {
		if p, err := parseCfwidget(body); err == nil {
			return p, nil
		}
		// A corrupt cached entry falls through to a fresh fetch.
	}
	body, err := c.fetch(ctx, rawURL, ref.Slug)
	if err != nil {
		return cfPayload{}, err
	}
	p, err := parseCfwidget(body)
	if err != nil {
		return cfPayload{}, fmt.Errorf("cfwidget %s: %w", ref.Slug, err)
	}
	c.saveCache(rawURL, body) // best effort: the cache is an optimization, never a hard dependency
	return p, nil
}

// projectURL builds the mirror's project endpoint: a numeric project ID
// (the "## X-Curse-Project-ID" TOC value) hits /{id}, anything else is
// treated as a project slug and hits /wow/addons/{slug}.
func (c *Cfwidget) projectURL(slug string) (string, error) {
	s := strings.TrimSpace(slug)
	if s == "" {
		return "", errors.New("cfwidget: source carries no project id")
	}
	base := c.Base
	if base == "" {
		base = defaultCfwidgetBase
	}
	if _, err := strconv.Atoi(s); err == nil {
		return base + "/" + s, nil
	}
	return base + "/wow/addons/" + url.PathEscape(s), nil
}

// fetch reads one mirror payload body, with the scraper user agent, the
// 429/5xx backoff and the pacing gate of the shared scrapeGet. No URL is
// ever echoed into error messages.
func (c *Cfwidget) fetch(ctx context.Context, rawURL, slug string) ([]byte, error) {
	resp, err := scrapeGet(ctx, c.client(), rawURL)
	if err != nil {
		return nil, fmt.Errorf("cfwidget %s: %w", slug, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cfwidget %s: mirror returned %s", slug, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPayloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cfwidget %s: read payload: %w", slug, err)
	}
	if len(body) > maxPayloadBytes {
		return nil, fmt.Errorf("cfwidget %s: payload larger than %d bytes", slug, maxPayloadBytes)
	}
	return body, nil
}

// parseCfwidget decodes one mirror payload body.
func parseCfwidget(body []byte) (cfPayload, error) {
	var p cfPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return cfPayload{}, fmt.Errorf("malformed cfwidget payload: %w", err)
	}
	return p, nil
}

// client returns the HTTP client to scrape through.
func (c *Cfwidget) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTPClient()
}

func (c *Cfwidget) ttl() time.Duration {
	if c.CacheTTL > 0 {
		return c.CacheTTL
	}
	return defaultCfwidgetCacheTTL
}

// cfCacheEntry is one cached mirror payload, persisted as
// <cacheDir>/<sha256(method+url)>.json with the shape
// {"fetched_at": …, "status": …, "body": …} — the github HTTP cache shape
// (internal/github/cache.go) minus the ETag the TTL-only cache never uses.
// Body holds the raw response bytes so a cached body replays byte for byte.
type cfCacheEntry struct {
	FetchedAt time.Time `json:"fetched_at"`
	Status    int       `json:"status"`
	Body      []byte    `json:"body"`
}

// cachePath returns the cache file path for one payload URL: the hex sha256
// of "method+url" inside CacheDir, the internal/github/cache.go convention.
func (c *Cfwidget) cachePath(rawURL string) string {
	sum := sha256.Sum256([]byte("GET" + rawURL))
	return filepath.Join(c.CacheDir, hex.EncodeToString(sum[:])+".json")
}

// cached returns a fresh cached payload body. A missing, unreadable,
// corrupt, non-200 or expired entry yields false: the cache is an
// optimization and never a hard dependency of a request.
func (c *Cfwidget) cached(rawURL string) ([]byte, bool) {
	if c.CacheDir == "" {
		return nil, false
	}
	data, err := os.ReadFile(c.cachePath(rawURL))
	if err != nil {
		return nil, false
	}
	var e cfCacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, false
	}
	if e.Status != http.StatusOK || time.Since(e.FetchedAt) >= c.ttl() {
		return nil, false
	}
	return e.Body, true
}

// saveCache atomically replaces the cache file with the payload body so a
// concurrent reader never observes a partial entry. Write failures are
// ignored on purpose: caching is best effort.
func (c *Cfwidget) saveCache(rawURL string, body []byte) {
	if c.CacheDir == "" {
		return
	}
	data, err := json.Marshal(cfCacheEntry{FetchedAt: time.Now(), Status: http.StatusOK, Body: body})
	if err != nil {
		return
	}
	if err := os.MkdirAll(c.CacheDir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(c.CacheDir, "cfwidget-*.tmp")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Chmod(tmp.Name(), 0o600)
	_ = os.Rename(tmp.Name(), c.cachePath(rawURL))
}
