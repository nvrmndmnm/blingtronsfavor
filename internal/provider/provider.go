// Package provider implements the secondary upstream providers bfctl can
// adopt and sync addons from besides GitHub releases: Tukui/ElvUI package
// endpoints, WoWInterface JSON lookups keyed by the TOC "## X-WoWI-ID", and
// arbitrary git remotes built through the full packager pipeline. The
// Provider interface keeps the resolver's routing logic shared across all of
// them (plan risk R7): every provider fails soft — a provider error surfaces
// as a per-addon warning that "bfctl sync" skips over, never as a run-ending
// failure.
package provider

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/state"
)

// maxDownloadBytes caps a provider download: anything larger is not a
// plausible addon package.
const maxDownloadBytes = 1 << 30

// downloadTimeout bounds a single provider API call or package download.
const downloadTimeout = 5 * time.Minute

// Upstream is what a provider reports for one source reference: the upstream
// version, where its changelog lives and how it is to be installed.
type Upstream struct {
	Version   string         // upstream version or tag to compare with the installed one
	Changelog string         // changelog URL for that version
	Strategy  state.Strategy // install strategy Fetch implements
	URL       string         // download URL or git remote Fetch materializes
	Ref       string         // git ref Fetch builds at (git-direct only)
}

// Provider is one upstream provider. Check reports the newest upstream for
// ref; Fetch materializes ref's payload into dest as top-level addon folders
// ready for the installer. Implementations are safe for concurrent use.
type Provider interface {
	// Check resolves ref's upstream: version, changelog URL and strategy.
	Check(ctx context.Context, ref state.SourceRef) (Upstream, error)

	// Fetch downloads ref into dest as top-level addon folders.
	Fetch(ctx context.Context, ref state.SourceRef, dest string) error
}

// Options configures provider implementations. Zero values select a plain
// HTTP client with a bounded per-request timeout and no client flavor
// context (only the git-direct packager pipeline needs the flavor and
// interface for its TOC split).
type Options struct {
	Flavor    client.Flavor // client flavor the packager's TOC split selects for
	Interface int           // client interface version written into staged .toc files
	HTTP      *http.Client  // download client, defaulted when nil
}

// client returns the HTTP client to make provider calls through.
func (o Options) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return defaultHTTPClient()
}

// defaultHTTPClient returns the bounded client provider calls fall back to.
func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: downloadTimeout}
}

// For returns the provider implementation for one source kind. GitHub
// sources are resolved by internal/resolver and have no provider here;
// manual entries have no upstream at all and cannot be checked or fetched.
func For(kind state.SourceKind, opts Options) (Provider, error) {
	switch kind {
	case state.SourceTukui:
		return &Tukui{HTTP: opts.client()}, nil
	case state.SourceWoWI:
		return &WoWI{HTTP: opts.client()}, nil
	case state.SourceGit:
		return &GitDirect{Flavor: opts.Flavor, Interface: opts.Interface}, nil
	case state.SourceGitHub:
		return nil, errors.New("provider: github sources are resolved by internal/resolver")
	case state.SourceManual:
		return nil, errors.New("provider: manual entries have no upstream; bind them with \"bfctl adopt --bind\" first")
	default:
		return nil, fmt.Errorf("provider: no implementation for source kind %q", kind)
	}
}

// download streams url into a temporary zip file and unpacks it into dest,
// returning the payload root (dest itself, or dest with its single top-level
// wrapper directory stripped — the same contract as resolver.Unpack). The
// temporary zip is always removed.
func download(ctx context.Context, httpClient *http.Client, url, dest string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", "bfctl")
	req.Header.Set("Accept", "*/*")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}

	tmp, err := os.CreateTemp("", "bfctl-provider-*.zip")
	if err != nil {
		return "", fmt.Errorf("create package temp file: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := io.Copy(tmp, io.LimitReader(resp.Body, maxDownloadBytes+1)); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close package temp file %s: %w", name, err)
	}
	if info, err := os.Stat(name); err == nil && info.Size() > maxDownloadBytes {
		return "", fmt.Errorf("download %s: package larger than %d bytes", url, maxDownloadBytes)
	}
	return unpackZip(name, dest)
}

// unpackZip extracts the zip archive at zipPath into dir and returns the
// payload root: dir itself, or dir with its single top-level wrapper
// directory stripped. Entries that would escape dir and symlink entries are
// rejected outright. (A small cousin of resolver.Unpack: the provider package
// must not import the resolver, which routes through it.)
func unpackZip(zipPath, dir string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("unpack %s: only zip packages are supported: %w", zipPath, err)
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if err := extractEntry(dir, f); err != nil {
			return "", err
		}
	}
	return stripWrapper(dir)
}

// extractEntry writes one archive entry under dir, refusing anything that
// would land outside it.
func extractEntry(dir string, f *zip.File) error {
	clean := path.Clean(strings.TrimPrefix(f.Name, "/"))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return fmt.Errorf("unpack: archive entry %q escapes the destination directory", f.Name)
	}
	target := filepath.Join(dir, filepath.FromSlash(clean))
	if rel, err := filepath.Rel(dir, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unpack: archive entry %q escapes the destination directory", f.Name)
	}
	if f.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unpack: archive entry %q is a symlink, which is not allowed in an addon archive", f.Name)
	}
	if f.FileInfo().IsDir() {
		return os.MkdirAll(target, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("unpack %s: %w", f.Name, err)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("unpack %s: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()
	perm := f.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("unpack %s: %w", f.Name, err)
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		return fmt.Errorf("unpack %s: %w", f.Name, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("unpack %s: %w", f.Name, err)
	}
	return nil
}

// stripWrapper removes the single top-level wrapper directory many packages
// use, promoting its contents to the payload root. A wrapper that is itself
// an addon directory (it carries a .toc) is left untouched.
func stripWrapper(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read payload %s: %w", dir, err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return dir, nil
	}
	wrapper := filepath.Join(dir, entries[0].Name())
	inner, err := os.ReadDir(wrapper)
	if err != nil {
		return "", fmt.Errorf("read payload %s: %w", wrapper, err)
	}
	for _, e := range inner {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".toc") {
			return dir, nil // the wrapper is an addon directory itself
		}
	}
	for _, e := range inner {
		if err := os.Rename(filepath.Join(wrapper, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			return "", fmt.Errorf("strip wrapper %s: %w", entries[0].Name(), err)
		}
	}
	if err := os.Remove(wrapper); err != nil {
		return "", fmt.Errorf("strip wrapper %s: %w", entries[0].Name(), err)
	}
	return dir, nil
}
