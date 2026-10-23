package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/itp5b5/blessforge/internal/state"
)

// defaultTukuiAPI is the Tukui package endpoint both the ElvUI/Tukui UIs and
// the Tukui.org addon catalog answer on: "?ui=elvui|tukui" for the two
// standalone packages, "?addons=<id>" for catalog addons (plan risk R7 — a
// provider that stops answering degrades to a per-addon skip).
const defaultTukuiAPI = "https://www.tukui.org/api.php"

// Tukui resolves ElvUI and Tukui.org package endpoints. The source slug is
// the package name ("elvui", "tukui") or the numeric Tukui.org addon ID.
type Tukui struct {
	// API overrides the package endpoint (tests point it at a stub).
	API string

	// HTTP is the download client; nil selects a bounded default.
	HTTP *http.Client
}

// tukuiAddon is the subset of the Tukui package response bfctl needs.
type tukuiAddon struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Changelog string `json:"changelog"`
	WebURL    string `json:"web_url"`
	Download  string `json:"download"`
}

// endpoint renders the package endpoint for one slug: the standalone UIs
// ("elvui", "tukui") live under "?ui=", every other slug is a catalog addon
// ID under "?addons=".
func (t *Tukui) endpoint(slug string) string {
	base := t.API
	if base == "" {
		base = defaultTukuiAPI
	}
	switch strings.ToLower(slug) {
	case "elvui", "tukui":
		return base + "?ui=" + url.QueryEscape(slug)
	default:
		return base + "?addons=" + url.QueryEscape(slug)
	}
}

// Check looks the package up on the Tukui endpoint.
func (t *Tukui) Check(ctx context.Context, ref state.SourceRef) (Upstream, error) {
	if ref.Slug == "" {
		return Upstream{}, errors.New("tukui: source carries no package slug")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.endpoint(ref.Slug), nil)
	if err != nil {
		return Upstream{}, fmt.Errorf("tukui %s: %w", ref.Slug, err)
	}
	req.Header.Set("User-Agent", "bfctl")
	req.Header.Set("Accept", "application/json")
	resp, err := t.client().Do(req)
	if err != nil {
		return Upstream{}, fmt.Errorf("tukui %s: %w", ref.Slug, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Upstream{}, fmt.Errorf("tukui %s: %s", ref.Slug, resp.Status)
	}
	var addon tukuiAddon
	if err := json.NewDecoder(resp.Body).Decode(&addon); err != nil {
		return Upstream{}, fmt.Errorf("tukui %s: decode package metadata: %w", ref.Slug, err)
	}
	if addon.Version == "" || addon.Download == "" {
		return Upstream{}, fmt.Errorf("tukui %s: package metadata carries no version or download", ref.Slug)
	}
	changelog := addon.Changelog
	if changelog == "" {
		changelog = addon.WebURL
	}
	return Upstream{
		Version:   addon.Version,
		Changelog: changelog,
		Strategy:  state.StrategyDirect,
		URL:       addon.Download,
	}, nil
}

// Fetch downloads the current package zip into dest as top-level addon
// folders.
func (t *Tukui) Fetch(ctx context.Context, ref state.SourceRef, dest string) error {
	up, err := t.Check(ctx, ref)
	if err != nil {
		return err
	}
	_, err = download(ctx, t.client(), up.URL, dest)
	return err
}

// client returns the HTTP client to download through.
func (t *Tukui) client() *http.Client {
	if t.HTTP != nil {
		return t.HTTP
	}
	return defaultHTTPClient()
}
