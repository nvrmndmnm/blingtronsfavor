package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/github"
)

// maxMetaBytes caps a release.json download: the metadata is a small asset
// list and anything bigger is not one.
const maxMetaBytes = 1 << 20

// MetaEntry is one asset record parsed from a release.json metadata asset —
// the file BigWigsMods/packager attaches to a release next to the packaged
// zips, naming each asset's game flavor and whether it bundles libraries.
// A record that serves several flavors at once (a universal zip with a
// nested per-flavor metadata list) yields one entry per flavor.
type MetaEntry struct {
	Flavor    client.Flavor
	FlavorRaw string // the key/field spelling the metadata used
	NoLib     bool
	File      string
	URL       string
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

// flavorForKey maps one metadata flavor key to a client flavor. The alias
// table lives in package client — plan risk R4: "forever" and "camelot" name
// the same client and are used interchangeably (the recorded DeadlyBossMods
// release.json says "forever", Details ships "Details_Camelot.toc"), so
// metadata keys, zip-name flavor tags and CLI flags all fold one way.
func flavorForKey(s string) (client.Flavor, bool) {
	return client.FlavorForKey(s)
}

// isForeignFlavor reports whether s names a flavor bfctl does not support.
func isForeignFlavor(s string) bool { return foreignFlavorKeys[normalizeFlavorKey(s)] }

// ParseMeta parses a release.json payload into asset records. The schema is
// tolerated rather than pinned (plan risk R4): the document may be a record
// list, an object holding a record list under a well-known key, a flavor ->
// record(s) map, or a single record, and record keys accept the common
// spellings of flavor, file, url and nolib. A record may name one flavor, or
// — like the DeadlyBossMods universal package (recorded sample) — declare a
// nested per-flavor "metadata" list of every client flavor its single zip
// serves; one entry is emitted per supported flavor. Malformed JSON is an
// error; a payload without asset records simply yields no entries and lets
// the caller fall back to heuristics.
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
	if out := metaEntriesFromRecord(doc); len(out) > 0 {
		return out
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

// metaFlavorKeys are the record fields naming one game flavor.
var metaFlavorKeys = []string{"flavor", "flavor_string", "flavorString", "game_flavor", "flavor_name", "type", "channel"}

// metaFlavorListKeys are the record fields enumerating every game flavor
// one asset serves — the nested "metadata" list of the DeadlyBossMods
// universal package, whose single zip carries a TOC per flavor.
var metaFlavorListKeys = []string{"metadata", "flavor_metadata", "flavors", "game_flavors"}

// flavorEvidence is one flavor claim of an asset record: the client flavor
// it maps to and the raw spelling the metadata used.
type flavorEvidence struct {
	flavor client.Flavor
	raw    string
}

// metaEntriesFromRecord reads one asset record from a JSON object: one entry
// per game flavor the record serves — a universal package declares a nested
// per-flavor list and yields one entry per supported flavor — or one
// unflavored entry when the record names no usable flavor, for a
// flavor-keyed map to fill in. It yields nil when the object names no file
// and no URL — e.g. a container object.
func metaEntriesFromRecord(m map[string]any) []MetaEntry {
	var file, link string
	for _, key := range []string{"file", "filename", "name", "asset", "path", "zip"} {
		if s, ok := m[key].(string); ok && s != "" {
			file = s
			break
		}
	}
	for _, key := range []string{"url", "download", "download_url", "browser_download_url", "link"} {
		if s, ok := m[key].(string); ok && s != "" {
			link = s
			break
		}
	}
	if file == "" && link == "" {
		return nil
	}
	if !isZipName(file) && !isZipName(link) {
		return nil // a container object, not an asset record
	}
	nolib := metaNoLib(m)
	flavors := metaFlavors(m)
	if len(flavors) == 0 {
		return []MetaEntry{{NoLib: nolib, File: file, URL: link}}
	}
	out := make([]MetaEntry, 0, len(flavors))
	for _, f := range flavors {
		out = append(out, MetaEntry{Flavor: f.flavor, FlavorRaw: f.raw, NoLib: nolib, File: file, URL: link})
	}
	return out
}

// metaNoLib reads the record's bundled-library flag: the nolib spellings
// first, the inverted "libraries bundled" spellings overriding them.
func metaNoLib(m map[string]any) bool {
	var nolib bool
	for _, key := range []string{"nolib", "nolibs", "no_lib", "noLib"} {
		if v, ok := metaBool(m[key]); ok {
			nolib = v
			break
		}
	}
	for _, key := range []string{"libraries", "libs", "bundled_libs"} {
		if v, ok := metaBool(m[key]); ok {
			nolib = !v
			break
		}
	}
	return nolib
}

// metaFlavors collects the distinct flavor evidences of one record. The
// nested per-flavor lists enumerate every flavor the asset serves (a
// universal package lists several); failing those, one flavor field names
// the single flavor, and a record without a usable spelling yields no
// evidence — the caller then treats the record as untagged. Foreign
// spellings (wrath, cata, …) are never evidence: they name another game's
// package. Unknown spellings inside an explicit list are dropped the same
// way — the enumeration is exact and serves no client bfctl supports.
func metaFlavors(m map[string]any) []flavorEvidence {
	var out []flavorEvidence
	seen := map[client.Flavor]bool{}
	add := func(fl client.Flavor, raw string) {
		if seen[fl] {
			return
		}
		seen[fl] = true
		out = append(out, flavorEvidence{flavor: fl, raw: raw})
	}
	for _, key := range metaFlavorListKeys {
		if v, ok := m[key]; ok {
			for _, ev := range flavorEvidencesFrom(v) {
				add(ev.flavor, ev.raw)
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, key := range metaFlavorKeys {
		s, ok := m[key].(string)
		if !ok || s == "" {
			continue
		}
		if fl, known := flavorForKey(s); known {
			add(fl, s)
		}
		break // the first present spelling is the record's flavor
	}
	return out
}

// flavorEvidencesFrom collects the flavor evidences of one nested
// per-flavor list element: a string ("forever"), an object with a flavor
// field ({"flavor": "forever", "interface": 16001}) or a map keyed by
// flavor spelling. Unknown and foreign spellings are dropped.
func flavorEvidencesFrom(v any) []flavorEvidence {
	var out []flavorEvidence
	switch x := v.(type) {
	case string:
		if fl, known := flavorForKey(x); known {
			out = append(out, flavorEvidence{flavor: fl, raw: x})
		}
	case []any:
		for _, el := range x {
			out = append(out, flavorEvidencesFrom(el)...)
		}
	case map[string]any:
		for _, key := range metaFlavorKeys {
			s, ok := x[key].(string)
			if !ok || s == "" {
				continue
			}
			if fl, known := flavorForKey(s); known {
				out = append(out, flavorEvidence{flavor: fl, raw: s})
			}
			break // the first present spelling is the element's flavor
		}
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys) // map iteration order must not leak into plan reasons
		for _, key := range keys {
			if fl, known := flavorForKey(key); known {
				out = append(out, flavorEvidence{flavor: fl, raw: key})
			}
		}
	}
	return out
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
	defer func() { _ = resp.Body.Close() }()
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
