package resolver

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

	"github.com/itp5b5/blessforge/internal/state"
)

// maxAssetBytes caps a release asset download: anything larger than this is
// not a plausible addon archive.
const maxAssetBytes = 1 << 30

// Fetch downloads the plan's release asset into a temporary zip and unpacks
// it into dir, returning the payload root (see Unpack). Only zip release
// assets are supported (plan §2); the temporary zip is always removed.
func (r *Resolver) Fetch(ctx context.Context, plan Plan, dir string) (string, error) {
	if plan.Strategy != state.StrategyRelease {
		return "", fmt.Errorf("fetch %s: only the release strategy downloads assets (source builds land with M4)", plan.Strategy)
	}
	if plan.URL == "" {
		return "", errors.New("fetch: plan carries no download URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, plan.URL, nil)
	if err != nil {
		return "", fmt.Errorf("build request for %s: %w", plan.URL, err)
	}
	req.Header.Set("User-Agent", "bfctl")
	resp, err := r.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", plan.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", plan.URL, resp.Status)
	}

	tmp, err := os.CreateTemp("", "bfctl-asset-*.zip")
	if err != nil {
		return "", fmt.Errorf("create asset temp file: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := io.Copy(tmp, io.LimitReader(resp.Body, maxAssetBytes+1)); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("download %s: %w", plan.URL, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close asset temp file %s: %w", name, err)
	}
	if info, err := os.Stat(name); err == nil && info.Size() > maxAssetBytes {
		return "", fmt.Errorf("download %s: asset larger than %d bytes", plan.URL, maxAssetBytes)
	}
	return Unpack(name, dir)
}

// Unpack extracts the zip archive at zipPath into dir and returns the payload
// root: dir itself, or dir with its single top-level wrapper directory
// stripped (plan §2). A wrapper is stripped only when it is the archive's
// only top-level entry and carries no .toc of its own — "Widget-1.3.0/" around
// the real addon directories — while an addon directory like "Widget/" with
// "Widget.toc" inside is left untouched. Entries that would escape dir and
// symlink entries are rejected outright.
func Unpack(zipPath, dir string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("unpack %s: only zip release assets are supported: %w", zipPath, err)
	}
	defer zr.Close()
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
	defer rc.Close()
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

// stripWrapper removes the single top-level wrapper directory many release
// archives use, promoting its contents to the payload root.
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
