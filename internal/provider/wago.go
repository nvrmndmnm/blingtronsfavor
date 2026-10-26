package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/state"
)

// defaultWagoBase is the Wago Addons site. Every page embeds the Inertia
// "data-page" JSON blob server-side in the HTML, so plain HTTP GETs return
// machine-readable data — no API key, no JavaScript, no session (research
// report §1). The download step follows a pre-signed, expiring link scraped
// from the versions page: Wago's robots.txt disallows "/download/", and the
// owner accepted that gray zone for private personal use only (XD-15,
// 2026-10-26) — revisit before bfctl is ever shared or distributed.
const defaultWagoBase = "https://addons.wago.io"

// wagoMaxPageBytes caps a scraped versions page; the payload is far smaller
// than this even for addons with long changelogs.
const wagoMaxPageBytes = 8 << 20

// Wago resolves Wago Addons through the site's Inertia data-page payload.
// The source slug is the TOC "## X-Wago-ID" value — the short ID ("VBNBxKx5")
// or the legacy numeric ID both work as the URL key — or a plain addon slug.
type Wago struct {
	// Base overrides the site base (tests point it at a stub).
	Base string

	// Flavor selects the newest stable release that lists support for the
	// client flavor; the zero value takes the newest release unfiltered.
	Flavor client.Flavor

	// HTTP is the download client; nil selects a bounded default.
	HTTP *http.Client
}

// wagoPage is the subset of the Inertia data-page payload bfctl needs: the
// props.releases Laravel paginator of the versions endpoint.
type wagoPage struct {
	Props struct {
		Releases *struct {
			Data []wagoRelease `json:"data"`
		} `json:"releases"`
	} `json:"props"`
}

// wagoRelease is one release entry of props.releases.data: its label (the
// version to compare), the inline markdown changelog, the pre-signed
// download link and the per-release flavor support fields. Fields bfctl does
// not need are ignored.
type wagoRelease struct {
	ID        string                     // release id
	Label     string                     // version label, e.g. "5.19.0"
	Stability string                     // "stable", "beta", "alpha"
	Changelog string                     // inline markdown changelog
	CreatedAt string                     // release timestamp
	Download  string                     // pre-signed download link (expires ~2h)
	Patches   map[client.Flavor][]string // "supported_<flavor>_patches" by flavor key
}

// wagoFlavorKeys are the per-release flavor support fields observed on the
// versions payload ("supported_<key>_patches"); extra flavors bfctl does not
// serve (cata, wotlk, bc) are simply never looked up.
var wagoFlavorKeys = map[client.Flavor]string{
	client.FlavorMainline: "retail",
	client.FlavorForever:  "forever",
	client.FlavorClassic:  "classic",
	client.FlavorMists:    "mop",
}

// UnmarshalJSON decodes one release defensively: the fields bfctl needs are
// taken as strings, and each "supported_<flavor>_patches" entry is captured
// raw so an absent, null or oddly-shaped field degrades to "no information"
// instead of failing the whole payload (flavor field quirks are expected).
// A present but empty list is information: the release does not serve that
// flavor. The live data showed both shapes — WeakAuras ships
// "supported_retail_patches: null" next to populated classic fields, and
// Raider.IO is retail-only.
func (r *wagoRelease) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	str := func(key string) string {
		var s string
		if v, ok := raw[key]; ok {
			_ = json.Unmarshal(v, &s)
		}
		return s
	}
	r.ID = str("id")
	r.Label = str("label")
	r.Stability = str("stability")
	r.Changelog = str("changelog")
	r.CreatedAt = str("created_at")
	r.Download = str("download_link")
	r.Patches = nil
	for flavor, wagoKey := range wagoFlavorKeys {
		v, ok := raw["supported_"+wagoKey+"_patches"]
		if !ok || string(v) == "null" {
			continue // absent or null: no information
		}
		var list []string
		if err := json.Unmarshal(v, &list); err != nil {
			continue // oddly shaped: degrade to no information
		}
		if r.Patches == nil {
			r.Patches = make(map[client.Flavor][]string, len(wagoFlavorKeys))
		}
		r.Patches[flavor] = list
	}
	return nil
}

// supports reports whether the release lists support for flavor: an absent,
// null or unparsable flavor field counts as supporting it (degrade gracefully
// over a missing field), a present non-empty patch list is a yes, and a
// present empty list is a no.
func (r *wagoRelease) supports(flavor client.Flavor) bool {
	if flavor == "" || r.Patches == nil {
		return true
	}
	list, ok := r.Patches[flavor]
	return !ok || len(list) > 0
}

// base returns the site base the provider scrapes.
func (w *Wago) base() string {
	if w.Base != "" {
		return w.Base
	}
	return defaultWagoBase
}

// versionsURL renders the stable versions endpoint for one addon key.
func (w *Wago) versionsURL(slug string) string {
	return w.base() + "/addons/" + url.PathEscape(slug) + "/versions?stability=stable"
}

// Check reports the newest stable release of the addon: the release label as
// the version, its inline markdown changelog and a fresh pre-signed download
// link. The link is short-lived (it expires roughly two hours after the page
// is served) and must be used promptly — it is never cached or persisted.
func (w *Wago) Check(ctx context.Context, ref state.SourceRef) (Upstream, error) {
	rel, err := w.release(ctx, ref.Slug)
	if err != nil {
		return Upstream{}, err
	}
	changelog := rel.Changelog
	if changelog == "" {
		changelog = w.base() + "/addons/" + url.PathEscape(ref.Slug)
	}
	return Upstream{
		Version:   rel.Label,
		Changelog: changelog,
		Strategy:  state.StrategyDirect,
		URL:       rel.Download,
	}, nil
}

// Fetch re-reads the versions page for a fresh pre-signed link and streams
// the package into dest as top-level addon folders.
func (w *Wago) Fetch(ctx context.Context, ref state.SourceRef, dest string) error {
	rel, err := w.release(ctx, ref.Slug)
	if err != nil {
		return err
	}
	// Error messages carry the addon key, never the signed link.
	_, err = downloadAs(ctx, w.client(), rel.Download, "wago "+ref.Slug+" package", dest)
	return err
}

// release scrapes the versions page and returns the newest stable release
// that supports w.Flavor. The page is re-read on every call: signed download
// links are only valid shortly after page load and must never be cached.
func (w *Wago) release(ctx context.Context, slug string) (wagoRelease, error) {
	if slug == "" {
		return wagoRelease{}, errors.New("wago: source carries no addon id")
	}
	page, err := w.page(ctx, slug)
	if err != nil {
		return wagoRelease{}, err
	}
	releases := page.Props.Releases
	if releases == nil {
		return wagoRelease{}, fmt.Errorf("wago %s: versions page carries no releases payload (unknown addon or changed page shape)", slug)
	}
	for _, rel := range releases.Data {
		if rel.Label == "" || rel.Download == "" {
			continue
		}
		if !rel.supports(w.Flavor) {
			continue
		}
		return rel, nil
	}
	if len(releases.Data) == 0 {
		return wagoRelease{}, fmt.Errorf("wago %s: no stable releases", slug)
	}
	return wagoRelease{}, fmt.Errorf("wago %s: no stable release supports flavor %q", slug, w.Flavor)
}

// page fetches and parses one versions page's data-page payload.
func (w *Wago) page(ctx context.Context, slug string) (wagoPage, error) {
	resp, err := scrapeGet(ctx, w.client(), w.versionsURL(slug))
	if err != nil {
		return wagoPage{}, fmt.Errorf("wago %s: %w", slug, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return wagoPage{}, fmt.Errorf("wago %s: %s", slug, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, wagoMaxPageBytes))
	if err != nil {
		return wagoPage{}, fmt.Errorf("wago %s: read versions page: %w", slug, err)
	}
	raw, err := wagoDataPage(body)
	if err != nil {
		return wagoPage{}, fmt.Errorf("wago %s: %w", slug, err)
	}
	var page wagoPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return wagoPage{}, fmt.Errorf("wago %s: decode data-page payload: %w", slug, err)
	}
	return page, nil
}

// wagoDataPage extracts the Inertia "data-page" JSON blob from a page's
// HTML. The attribute value is HTML-escaped, so no raw quote can appear
// inside it and the closing quote ends the payload reliably.
func wagoDataPage(page []byte) ([]byte, error) {
	const attr = `data-page="`
	i := strings.Index(string(page), attr)
	if i < 0 {
		return nil, errors.New("page carries no data-page payload")
	}
	rest := page[i+len(attr):]
	end := bytes.IndexByte(rest, '"')
	if end < 0 {
		return nil, errors.New("unterminated data-page payload")
	}
	raw := html.UnescapeString(string(rest[:end]))
	return []byte(raw), nil
}

// client returns the HTTP client to scrape through.
func (w *Wago) client() *http.Client {
	if w.HTTP != nil {
		return w.HTTP
	}
	return defaultHTTPClient()
}
