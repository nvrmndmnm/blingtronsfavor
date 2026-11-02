package packager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nvrmndmnm/blingtronsfavor/internal/gitops"
	"github.com/nvrmndmnm/blingtronsfavor/internal/paths"
)

// ErrSvnMissing is wrapped into errors returned for an svn external when no
// svn executable is found on the PATH (decision D-SVN: git externals work out
// of the box; svn externals need the subversion client).
var ErrSvnMissing = errors.New("svn executable not found in PATH")

// CacheStats is a snapshot of an ExternalCache's activity: how many fetches
// were performed and how many builds were served from the cache. "bfctl"
// fetches every external exactly once per (URL, ref); later builds reuse the
// cached checkout.
type CacheStats struct {
	Hits    int // Materialize calls served from the cache
	Fetches int // real fetches performed (cache misses)
}

// ExternalCache caches fetched externals under ~/.cache/bfctl/externals, one
// directory per (URL, ref) — named "<sha256(url)@ref>" as the plan specifies.
// The cache is safe for concurrent use; fetches of the same external are
// serialized so two parallel builds cannot check it out twice.
type ExternalCache struct {
	dir string

	mu      sync.Mutex
	hits    int
	fetches int
}

// NewExternalCache returns the shared external cache under
// ~/.cache/bfctl/externals (created lazily with 0700).
func NewExternalCache() (*ExternalCache, error) {
	dir, err := paths.CacheSubdir("externals")
	if err != nil {
		return nil, fmt.Errorf("resolve external cache directory: %w", err)
	}
	return &ExternalCache{dir: dir}, nil
}

// NewExternalCacheAt returns a cache rooted at dir. Used by tests and by
// callers that pin the cache location.
func NewExternalCacheAt(dir string) *ExternalCache {
	return &ExternalCache{dir: dir}
}

// Dir returns the cache root directory.
func (c *ExternalCache) Dir() string { return c.dir }

// Stats returns a snapshot of the cache hit/fetch counters.
func (c *ExternalCache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{Hits: c.hits, Fetches: c.fetches}
}

// entryDir returns the cache entry for ext: <cache>/<sha256(url)@ref>.
func (c *ExternalCache) entryDir(ext External) string {
	sum := sha256.Sum256([]byte(ext.URL))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+"@"+sanitizeRef(ext.Ref()))
}

// sanitizeRef renders a ref filesystem-safe while staying recognizable: every
// rune outside [A-Za-z0-9._-] (including path separators) becomes "_".
func sanitizeRef(ref string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, ref)
}

// Materialize copies external ext into dest, fetching it into the cache on
// first use and copying the cached checkout on every later use. dest must not
// exist yet; it is created (with its parents) as part of the copy. VCS
// metadata (.git, .svn) is never copied into the package.
func (c *ExternalCache) Materialize(ctx context.Context, ext External, dest string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entryDir(ext)
	_, err := os.Stat(entry)
	switch {
	case err == nil:
		c.hits++
	case errors.Is(err, os.ErrNotExist):
		if err := c.fetch(ctx, ext, entry); err != nil {
			return err
		}
		c.fetches++
	default:
		return fmt.Errorf("inspect external cache %s: %w", entry, err)
	}
	if err := copyTreeFiltered(dest, entry, isVCSMeta); err != nil {
		return fmt.Errorf("copy external %s into %s: %w", ext.URL, dest, err)
	}
	return nil
}

// fetch checks ext out into the cache entry, via a temporary sibling
// directory so a failed fetch never leaves a half-populated entry behind.
func (c *ExternalCache) fetch(ctx context.Context, ext External, entry string) error {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("create external cache %s: %w", c.dir, err)
	}
	tmp, err := os.MkdirTemp(c.dir, ".fetch-")
	if err != nil {
		return fmt.Errorf("create fetch directory in %s: %w", c.dir, err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	tree := filepath.Join(tmp, "tree")
	switch ext.Type {
	case "", "git":
		if err := fetchGitExternal(ctx, ext, tree); err != nil {
			return err
		}
	case "svn":
		if err := fetchSVNExternal(ctx, ext, tree); err != nil {
			return err
		}
	default:
		return fmt.Errorf("external %s: unsupported type %q (supported: git, svn)", ext.URL, ext.Type)
	}
	if err := os.Rename(tree, entry); err != nil {
		if _, statErr := os.Stat(entry); statErr == nil {
			return nil // lost the fetch race: the entry is complete
		}
		return fmt.Errorf("store external %s in the cache as %s: %w", ext.URL, entry, err)
	}
	return nil
}

// fetchGitExternal clones ext with depth 1: pinned externals check out the
// pinned tag/branch/commit, unpinned ones track the remote's default branch.
func fetchGitExternal(ctx context.Context, ext External, dest string) error {
	if ext.Tag == "" && ext.Branch == "" && ext.Commit == "" {
		// Unpinned external: plain shallow clone of the default branch.
		if _, err := runGit(ctx, "", "clone", "--quiet", "--depth", "1", "--", ext.URL, dest); err != nil {
			return fmt.Errorf("fetch external %s: %w", ext.URL, err)
		}
		return nil
	}
	if err := gitops.ShallowClone(ctx, ext.URL, ext.Ref(), dest); err != nil {
		return fmt.Errorf("fetch external %s at %s: %w", ext.URL, ext.Ref(), err)
	}
	return nil
}

// fetchSVNExternal exports ext through the svn binary. Tags and branches
// follow the BigWigsMods/packager svn layout: a URL under ".../trunk" is
// re-pointed at the sibling ".../tags/<tag>" / ".../branches/<branch>";
// a commit pins the revision of the URL as given.
func fetchSVNExternal(ctx context.Context, ext External, dest string) error {
	bin, err := exec.LookPath("svn")
	if err != nil {
		return fmt.Errorf("external %s is an svn repository: %w; install subversion (e.g. pacman -S subversion) or point the external at a git URL", ext.URL, ErrSvnMissing)
	}
	url := ext.URL
	args := []string{"export", "--force"}
	if ext.Commit != "" {
		args = append(args, "-r", ext.Commit)
	} else if ext.Tag != "" || ext.Branch != "" {
		kind := "branches"
		if ext.Tag != "" {
			kind = "tags"
		}
		refURL, ok := svnRefURL(ext.URL, kind, ext.Ref())
		if !ok {
			return fmt.Errorf("external %s: cannot derive the svn %s URL for %q: %q has no \"/trunk\" segment (BigWigsMods/packager svn layout)", ext.URL, kind, ext.Ref(), ext.URL)
		}
		url = refURL
	}
	args = append(args, url, dest)
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		return fmt.Errorf("svn %s: %w: %s", strings.Join(args[:len(args)-2], " "), err, msg)
	}
	return nil
}

// svnRefURL re-points a ".../trunk[/sub]" URL at the sibling
// ".../<kind>/<ref>[/sub]" URL of the same repository root.
func svnRefURL(url, kind, ref string) (string, bool) {
	idx := strings.LastIndex(url, "/trunk")
	if idx < 0 {
		return "", false
	}
	root := url[:idx]
	sub := strings.TrimPrefix(url[idx+len("/trunk"):], "/")
	refURL := root + "/" + kind + "/" + ref
	if sub != "" {
		refURL += "/" + sub
	}
	return refURL, true
}

// isVCSMeta reports whether a path component is VCS metadata that must never
// end up inside an addon package.
func isVCSMeta(name string) bool {
	return name == ".git" || name == ".svn"
}

// copyTreeFiltered copies the tree at src to dst, skipping entries whose base
// name is rejected by skip (applied at every level) and rejecting symlinks
// and other non-regular entries, like the installer's staging copy does.
func copyTreeFiltered(dst, src string, skip func(name string) bool) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := d.Name()
		if skip(name) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %q is not allowed in an external", rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%q is not a regular file", rel)
		}
		return copyFile(target, p, info.Mode().Perm())
	})
}

// copyFile copies one regular file, preserving its permission bits.
func copyFile(dst, src string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
