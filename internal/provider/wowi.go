package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/itp5b5/blessforge/internal/state"
)

// defaultWoWIAPI is the WoWInterface addon endpoint: a JSON lookup keyed by
// the addon's WoWI ID (from the TOC "## X-WoWI-ID" line). The legacy API may
// require authentication or stop answering anonymously (plan risk R7) — the
// provider then fails soft and sync skips the addon with a warning.
const defaultWoWIAPI = "https://api.wowinterface.com/addons"

// defaultWoWIDownload is the public info page a changelog URL is rendered
// from when the API response carries none.
const defaultWoWIDownload = "https://www.wowinterface.com/downloads/info%s.html"

// WoWI resolves WoWInterface addons by their numeric project ID.
type WoWI struct {
	// API overrides the addon endpoint (tests point it at a stub).
	API string

	// HTTP is the download client; nil selects a bounded default.
	HTTP *http.Client
}

// wowiAddon is the subset of the WoWInterface addon response bfctl needs.
type wowiAddon struct {
	Title     string `json:"title"`
	UIVersion string `json:"UIVersion"`
	Download  string `json:"download"`
}

// endpoint renders the addon endpoint for one WoWI project ID.
func (w *WoWI) endpoint(id string) string {
	base := w.API
	if base == "" {
		base = defaultWoWIAPI
	}
	return base + "/" + url.PathEscape(id) + ".json"
}

// Check looks the addon up on the WoWInterface endpoint by its project ID.
func (w *WoWI) Check(ctx context.Context, ref state.SourceRef) (Upstream, error) {
	if ref.Slug == "" {
		return Upstream{}, errors.New("wowi: source carries no project ID")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.endpoint(ref.Slug), nil)
	if err != nil {
		return Upstream{}, fmt.Errorf("wowi %s: %w", ref.Slug, err)
	}
	req.Header.Set("User-Agent", "bfctl")
	req.Header.Set("Accept", "application/json")
	resp, err := w.client().Do(req)
	if err != nil {
		return Upstream{}, fmt.Errorf("wowi %s: %w", ref.Slug, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return Upstream{}, fmt.Errorf("wowi %s: %s (the WoWI API may require authentication; the addon is skipped)", ref.Slug, resp.Status)
	default:
		return Upstream{}, fmt.Errorf("wowi %s: %s", ref.Slug, resp.Status)
	}
	var addon wowiAddon
	if err := json.NewDecoder(resp.Body).Decode(&addon); err != nil {
		return Upstream{}, fmt.Errorf("wowi %s: decode addon metadata: %w", ref.Slug, err)
	}
	if addon.UIVersion == "" || addon.Download == "" {
		return Upstream{}, fmt.Errorf("wowi %s: addon metadata carries no version or download", ref.Slug)
	}
	return Upstream{
		Version:   addon.UIVersion,
		Changelog: fmt.Sprintf(defaultWoWIDownload, ref.Slug),
		Strategy:  state.StrategyDirect,
		URL:       addon.Download,
	}, nil
}

// Fetch downloads the addon's current package zip into dest as top-level
// addon folders.
func (w *WoWI) Fetch(ctx context.Context, ref state.SourceRef, dest string) error {
	up, err := w.Check(ctx, ref)
	if err != nil {
		return err
	}
	_, err = download(ctx, w.client(), up.URL, dest)
	return err
}

// client returns the HTTP client to download through.
func (w *WoWI) client() *http.Client {
	if w.HTTP != nil {
		return w.HTTP
	}
	return defaultHTTPClient()
}
