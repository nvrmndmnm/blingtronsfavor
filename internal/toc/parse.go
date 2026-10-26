// Package toc parses World of Warcraft .toc addon manifests, selects the
// manifest matching a client flavor and reports whether an addon is
// compatible with the detected client.
package toc

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// utf8BOM is the UTF-8 byte order mark .toc files may start with.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// knownSuffixes are the flavor-specific toc suffixes, e.g. "DeadlyBossMods_
// Mainline.toc". The order is the fallback precedence when a directory ships
// several foreign-flavor tocs.
var knownSuffixes = []string{"_Mainline", "_Vanilla", "_Classic", "_Mists", "_Camelot", "_Forever"}

// Toc is a parsed .toc addon manifest.
type Toc struct {
	Path           string   // filesystem path of the parsed file
	Name           string   // addon base name: file name without suffix and ".toc"
	Suffix         string   // flavor suffix including the underscore; empty for Name.toc
	Interfaces     []int    // "## Interface:" values in file order, deduplicated
	Title          string   // "## Title:"
	Version        string   // "## Version:"
	CurseProjectID string   // "## X-Curse-Project-ID:"
	WagoID         string   // "## X-Wago-ID:"
	WoWIID         string   // "## X-WoWI-ID:"
	Website        string   // "## X-Website:"
	Repository     string   // "## X-Repository:"
	Files          []string // ordered file entries
}

// Parse reads and parses the .toc file at path. UTF-8 BOMs and CRLF line
// endings are tolerated; unknown directives and comment lines are ignored.
func Parse(path string) (*Toc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t := &Toc{Path: path}
	base := filepath.Base(path)
	t.Name, t.Suffix = splitName(strings.TrimSuffix(base, filepath.Ext(base)))
	for _, line := range splitLines(data) {
		if key, value, ok := splitDirective(line); ok {
			t.addDirective(key, value)
			continue
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			t.Files = append(t.Files, trimmed)
		}
	}
	return t, nil
}

// splitName splits a toc base name into the addon name and its flavor suffix,
// e.g. "DeadlyBossMods_Mainline" -> ("DeadlyBossMods", "_Mainline"). The
// suffix is matched case-insensitively and returned in its canonical
// spelling: "_camelot" and "_CAMELOT" are the "_Camelot" flavor suffix.
func splitName(base string) (name, suffix string) {
	for _, suf := range knownSuffixes {
		if len(base) >= len(suf) && strings.EqualFold(base[len(base)-len(suf):], suf) {
			return base[:len(base)-len(suf)], suf
		}
	}
	return base, ""
}

// splitLines normalizes BOM and line endings and splits the manifest into
// lines.
func splitLines(data []byte) []string {
	data = bytes.TrimPrefix(data, utf8BOM)
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.Split(normalized, "\n")
}

// splitDirective splits a "## Key: Value" metadata line. Comment lines and
// file entries yield ok == false.
func splitDirective(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "##") {
		return "", "", false
	}
	key, value, ok = strings.Cut(strings.TrimPrefix(trimmed, "##"), ":")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(key), strings.TrimSpace(value), true
}

// addDirective records one metadata directive. Duplicate Interface lines and
// multi-value Interface lines all contribute to Interfaces; non-numeric
// Interface values are ignored. Unknown keys are dropped.
func (t *Toc) addDirective(key, value string) {
	switch strings.ToLower(key) {
	case "interface":
		for _, field := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t'
		}) {
			n, err := strconv.Atoi(field)
			if err != nil || slices.Contains(t.Interfaces, n) {
				continue
			}
			t.Interfaces = append(t.Interfaces, n)
		}
	case "title":
		t.Title = value
	case "version":
		t.Version = value
	case "x-curse-project-id":
		t.CurseProjectID = value
	case "x-wago-id":
		t.WagoID = value
	case "x-wowi-id":
		t.WoWIID = value
	case "x-website":
		t.Website = value
	case "x-repository":
		t.Repository = value
	}
}
