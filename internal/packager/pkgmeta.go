// Package packager builds an addon package tree from a source repository the
// way BigWigsMods/packager does for releases: it keeps a shallow working copy
// of the repository, exports it with git archive, applies the repository's
// .pkgmeta (package-as, externals, move-folders, ignore) and composes the
// resulting top-level addon folders into a staging directory that
// internal/installer can swap into Interface/AddOns atomically.
//
// The .pkgmeta shapes follow the BigWigsMods/packager reference
// implementation (release.sh), resolving plan risk R3: "externals" maps a
// destination path to {url, type, tag, branch, commit} (the shorthand
// "<dest>: <url>" is accepted too), "package-as" renames the top-level output
// folder, "move-folders" maps source to destination and "ignore" is a list of
// gitignore-style patterns. Directives the packager understands but bfctl
// does not act on yet (plain-copy, changelog-*, license-output, toc and nolib
// creation, dependency relations) are accepted and ignored, so real-world
// .pkgmeta files keep building.
package packager

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PkgMetaName is the canonical .pkgmeta file name at the repository root.
const PkgMetaName = ".pkgmeta"

// pkgMetaAltName is the fallback file name BigWigsMods/packager also reads.
const pkgMetaAltName = "pkgmeta.yaml"

// ErrInvalidPkgMeta is wrapped into every error caused by malformed or
// semantically invalid .pkgmeta content, so callers can tell a broken
// manifest from a broken fetch.
var ErrInvalidPkgMeta = errors.New("invalid .pkgmeta")

// External is one entry of the .pkgmeta "externals" map: a library fetched
// from another repository and copied into the package at the map key's
// destination path. Exactly one of Tag, Branch and Commit may be set; when
// none is set the external tracks the remote's default branch.
//
// Both .pkgmeta spellings unmarshal into this struct: the full mapping
//
//	externals:
//	  libs/LibStub:
//	    url: https://github.com/nex/LibStub.git
//	    tag: v1.0
//
// and the shorthand "<dest>: <url>". The reference implementation's extra
// external keys ("path", "curse-slug") are accepted and ignored.
type External struct {
	URL    string `yaml:"url"`
	Type   string `yaml:"type"` // "git" (default) or "svn"
	Tag    string `yaml:"tag"`
	Branch string `yaml:"branch"`
	Commit string `yaml:"commit"`
}

// UnmarshalYAML accepts both spellings of an external entry: a scalar (the
// URL shorthand) or the full mapping. Unknown mapping keys are rejected so a
// typo like "urll" fails loudly instead of silently producing an external
// without a URL; the reference implementation's "path" and "curse-slug" keys
// are recognized but not acted on.
func (e *External) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var url string
		if err := node.Decode(&url); err != nil {
			return fmt.Errorf("external must be a URL or a {url, type, tag, branch, commit} mapping: %w", err)
		}
		e.URL = url
		return nil
	}
	var raw struct {
		URL    string `yaml:"url"`
		Type   string `yaml:"type"`
		Tag    string `yaml:"tag"`
		Branch string `yaml:"branch"`
		Commit string `yaml:"commit"`
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "url", "type", "tag", "branch", "commit", "path", "curse-slug":
		default:
			return fmt.Errorf("unknown external key %q (supported: url, type, tag, branch, commit)", node.Content[i].Value)
		}
	}
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("external must be a URL or a {url, type, tag, branch, commit} mapping: %w", err)
	}
	*e = External{URL: raw.URL, Type: raw.Type, Tag: raw.Tag, Branch: raw.Branch, Commit: raw.Commit}
	return nil
}

// Ref returns the ref the external pins: the commit, tag or branch, or "HEAD"
// when the external tracks the default branch. At most one of them is set
// (enforced by validate).
func (e External) Ref() string {
	switch {
	case e.Commit != "":
		return e.Commit
	case e.Tag != "":
		return e.Tag
	case e.Branch != "":
		return e.Branch
	default:
		return "HEAD"
	}
}

// PkgMeta is the parsed .pkgmeta manifest (plan §ISSUE-09 schema).
type PkgMeta struct {
	PackageAs   string              `yaml:"package-as"`
	Externals   map[string]External `yaml:"externals"`
	MoveFolders map[string]string   `yaml:"move-folders"`
	Ignore      []string            `yaml:"ignore"`
}

// LoadPkgMeta reads and validates the .pkgmeta manifest at the repository
// root (dir). A repository without either manifest name yields an empty
// PkgMeta and no error — the manifest is optional. Malformed YAML, unknown
// external keys and invalid values fail with an error wrapping
// ErrInvalidPkgMeta.
func LoadPkgMeta(dir string) (*PkgMeta, error) {
	candidates := []string{
		filepath.Join(dir, PkgMetaName),
		filepath.Join(dir, pkgMetaAltName),
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", candidate, err)
		}
		return ParsePkgMeta(data, candidate)
	}
	return &PkgMeta{}, nil
}

// ParsePkgMeta decodes and validates .pkgmeta content. name is used in error
// messages only.
func ParsePkgMeta(data []byte, name string) (*PkgMeta, error) {
	meta := &PkgMeta{}
	if err := yaml.Unmarshal(data, meta); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidPkgMeta, name, err)
	}
	if err := meta.validate(); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidPkgMeta, name, err)
	}
	return meta, nil
}

// validate checks the semantic constraints the layout stage relies on, so a
// broken manifest fails before anything is fetched or copied.
func (m *PkgMeta) validate() error {
	if m.PackageAs != "" {
		if err := checkFolderName(m.PackageAs, "package-as"); err != nil {
			return err
		}
	}
	for dest, ext := range m.Externals {
		if err := checkRelPath(dest, fmt.Sprintf("externals destination %q", dest)); err != nil {
			return err
		}
		if ext.URL == "" {
			return fmt.Errorf("externals: %q is missing its \"url\"", dest)
		}
		switch ext.Type {
		case "", "git", "svn":
		default:
			return fmt.Errorf("externals: %q has unsupported type %q (supported: git, svn)", dest, ext.Type)
		}
		refs := 0
		for _, r := range []string{ext.Tag, ext.Branch, ext.Commit} {
			if r != "" {
				refs++
			}
		}
		if refs > 1 {
			return fmt.Errorf("externals: %q sets more than one of tag, branch and commit", dest)
		}
	}
	for src, dest := range m.MoveFolders {
		if err := checkRelPath(src, fmt.Sprintf("move-folders source %q", src)); err != nil {
			return err
		}
		if err := checkFolderName(dest, fmt.Sprintf("move-folders destination %q", dest)); err != nil {
			return err
		}
	}
	for _, pattern := range m.Ignore {
		if pattern == "" {
			return errors.New("ignore: empty pattern")
		}
	}
	return nil
}

// checkFolderName rejects anything that is not a single folder name.
func checkFolderName(name, what string) error {
	if name == "." || name == ".." || name == "" ||
		strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%s must be a single folder name, got %q", what, name)
	}
	return nil
}

// checkRelPath rejects empty, absolute and escaping ("..") paths.
func checkRelPath(p, what string) error {
	if p == "" {
		return fmt.Errorf("%s must not be empty", what)
	}
	if strings.ContainsAny(p, `\`) {
		return fmt.Errorf("%s must use \"/\" separators, got %q", what, p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%s must be relative, got %q", what, p)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%s must stay inside the package, got %q", what, p)
	}
	if clean != p {
		return fmt.Errorf("%s must be a clean relative path, got %q", what, p)
	}
	return nil
}
