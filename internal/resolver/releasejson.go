package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/github"
)

// maxMetaBytes caps a release.json download: the metadata is a small asset
// list and anything bigger is not one.
const maxMetaBytes = 1 << 20

// MetaEntry is one asset record parsed from a release.json metadata asset —
// the file BigWigsMods/packager attaches to a release next to the packaged
// zips, naming each asset's game flavor and whether it bundles libraries.
type MetaEntry struct {
	Flavor    client.Flavor
	FlavorRaw string // the key/field spelling the metadata used
	NoLib     bool
	File      string
	URL       string
}

// flavorAliases maps normalized release.json flavor keys to client flavors.
// Which key the Forever flavor uses (camelot? forever?) is risk R4 of the
// plan — without a real recorded sample both spellings (and the packager's
// retail/vanilla/mop aliases) are accepted so neither locks us out.
var flavorAliases = map[string]client.Flavor{
	"mainline":          client.FlavorMainline,
	"retail":            client.FlavorMainline,
	"main":              client.FlavorMainline,
	"classic":           client.FlavorClassic,
	"vanilla":           client.FlavorClassic,
	"era":               client.FlavorClassic,
	"classic_era":       client.FlavorClassic,
	"mists":             client.FlavorMists,
	"mop":               client.FlavorMists,
	"pandaria":          client.FlavorMists,
	"mists_of_pandaria": client.FlavorMists,
	"forever":           client.FlavorForever,
	"camelot":           client.FlavorForever,
	"wow_forever":       client.FlavorForever,
	"wow_camelot":       client.FlavorForever,
}

// foreignFlavorKeys are flavor spellings of client flavors bfctl does not
// support (Classic progression flavors). Assets tagged with them belong to
// another flavor and must never be selected as a fallback.
var foreignFlavorKeys = map[string]bool{
	"wrath":           true,
	"wrath_classic":   true,
	"wotlk":           true,
	"bcc":             true,
	"tbc":             true,
	"burning_crusade": true,
	"cata":            true,
	"cataclysm":       true,
}

// normalizeFlavorKey lowercases s and folds separators so the spellings
// "Classic Era", "classic-era" and "classic_era" share one key.
func normalizeFlavorKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '.', '/':
			return '_'
		}
		return r
	}, s)
	return strings.Trim(s, "_")
}

// flavorForKey maps one metadata flavor key to a client flavor.
func flavorForKey(s string) (client.Flavor, bool) {
	f, ok := flavorAliases[normalizeFlavorKey(s)]
	return f, ok
}

// isForeignFlavor reports whether s names a flavor bfctl does not support.
func isForeignFlavor(s string) bool { return foreignFlavorKeys[normalizeFlavorKey(s)] }

// ParseMeta parses a release.json payload into asset records. The schema is
// tolerated rather than pinned (plan risk R4 — a real recorded sample is
// still missing): the document may be a record list, an object holding a
// record list under a well-known key, a flavor -> record(s) map, or a single
// record, and record keys accept the common spellings of flavor, file, url
// and nolib. Malformed JSON is an error; a payload without asset records
// simply yields no entries and lets the caller fall back to heuristics.
func ParseMeta(data []byte) ([]MetaEntry, error) {
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse release.json: %w", err)
	}
	return metaEntriesFrom(doc), nil
}

// metaEntriesFrom collects the asset records of one decoded JSON value:
// a list recurses element-wise, an object is one record, a record list or a
// flavor-keyed map (see metaEntriesFromDoc).
func metaEntriesFrom(item any) []MetaEntry {
	switch v := item.(type) {
	case []any:
		var out []MetaEntry
		for _, el := range v {
			out = append(out, metaEntriesFrom(el)...)
		}
		return out
	case map[string]any:
		return metaEntriesFromDoc(v)
	}
	return nil
}

// metaEntriesFromDoc collects the asset records of one JSON object.
func metaEntriesFromDoc(doc map[string]any) []MetaEntry {
	if e, ok := metaEntryFromRecord(doc); ok {
		return []MetaEntry{e}
	}
	for _, key := range []string{"assets", "releases", "files", "packages", "artifacts", "entries"} {
		if v, ok := doc[key]; ok {
			if out := metaEntriesFrom(v); len(out) > 0 {
				return out
			}
		}
	}
	var out []MetaEntry
	for key, v := range doc {
		flavor, known := flavorForKey(key)
		if !known {
			continue
		}
		for _, e := range metaEntriesFrom(v) {
			if e.Flavor == "" {
				e.Flavor, e.FlavorRaw = flavor, key
			}
			out = append(out, e)
		}
	}
	return out
}

// metaEntryFromRecord reads one asset record from a JSON object. It reports
// false when the object names no file and no URL — e.g. a container object.
func metaEntryFromRecord(m map[string]any) (MetaEntry, bool) {
	var e MetaEntry
	for _, key := range []string{"file", "filename", "name", "asset", "path", "zip"} {
		if s, ok := m[key].(string); ok && s != "" {
			e.File = s
			break
		}
	}
	for _, key := range []string{"url", "download", "download_url", "browser_download_url", "link"} {
		if s, ok := m[key].(string); ok && s != "" {
			e.URL = s
			break
		}
	}
	if e.File == "" && e.URL == "" {
		return MetaEntry{}, false
	}
	if !isZipName(e.File) && !isZipName(e.URL) {
		return MetaEntry{}, false // a container object, not an asset record
	}
	for _, key := range []string{"flavor", "flavor_string", "flavorString", "game_flavor", "flavor_name", "type", "channel"} {
		if s, ok := m[key].(string); ok && s != "" {
			if fl, known := flavorForKey(s); known {
				e.Flavor, e.FlavorRaw = fl, s
			}
			break
		}
	}
	for _, key := range []string{"nolib", "nolibs", "no_lib", "noLib"} {
		if v, ok := metaBool(m[key]); ok {
			e.NoLib = v
			break
		}
	}
	for _, key := range []string{"libraries", "libs", "bundled_libs"} {
		if v, ok := metaBool(m[key]); ok {
			e.NoLib = !v
			break
		}
	}
	return e, true
}

// metaBool reads a boolean metadata field: a JSON bool, a JSON number
// (0/1) or a string like "true"/"false".
func metaBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case float64:
		return x != 0, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
	}
	return false, false
}

// selectMeta picks the metadata entry serving flavor: nolib=false wins
// (plan §2: "select the asset matching the detected flavor with nolib:
// false"), a nolib entry is only used when the addon ships no bundled-lib
// build at all, and the file name breaks remaining ties deterministically.
func selectMeta(entries []MetaEntry, flavor client.Flavor) *MetaEntry {
	var best *MetaEntry
	for i := range entries {
		e := &entries[i]
		if e.Flavor != flavor {
			continue
		}
		switch {
		case best == nil:
		case e.NoLib != best.NoLib:
			if e.NoLib {
				continue
			}
		case metaName(e) < metaName(best):
		default:
			continue
		}
		best = e
	}
	return best
}

// metaName is the sortable identity of a metadata entry.
func metaName(e *MetaEntry) string {
	if e.File != "" {
		return e.File
	}
	return e.URL
}

// candidateFromMeta downloads the release's release.json and resolves one
// candidate from it. Any failure (download, schema, no flavor match, asset
// not attached to the release) yields nil so the caller falls back to the
// zip-name heuristics.
func (r *Resolver) candidateFromMeta(ctx context.Context, rel github.Release, metaAsset github.Asset, flavor client.Flavor) *candidate {
	data, err := r.download(ctx, metaAsset.BrowserDownloadURL, maxMetaBytes)
	if err != nil {
		return nil
	}
	entries, err := ParseMeta(data)
	if err != nil {
		return nil
	}
	entry := selectMeta(entries, flavor)
	if entry == nil {
		return nil
	}
	asset := matchAsset(rel, entry)
	if asset == nil {
		return nil
	}
	return &candidate{
		release:    rel,
		asset:      *asset,
		nolib:      entry.NoLib,
		tagged:     true,
		viaMeta:    true,
		flavorName: entry.FlavorRaw,
	}
}

// matchAsset finds the release asset a metadata entry names, by file name
// first and by download URL second. Only zip assets are installable.
func matchAsset(rel github.Release, entry *MetaEntry) *github.Asset {
	for i := range rel.Assets {
		a := &rel.Assets[i]
		if !isZipName(a.Name) {
			continue
		}
		if entry.File != "" && strings.EqualFold(a.Name, entry.File) {
			return a
		}
		if entry.File == "" && entry.URL != "" && strings.EqualFold(a.BrowserDownloadURL, entry.URL) {
			return a
		}
	}
	return nil
}

// download fetches raw bytes from url, refusing bodies larger than limit.
func (r *Resolver) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", "bfctl")
	resp, err := r.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download %s: body larger than %d bytes", url, limit)
	}
	return data, nil
}
