package packager

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/itp5b5/blessforge/internal/gitops"
	"github.com/itp5b5/blessforge/internal/paths"
)

// Spec is one source build request: the addon repository and the ref to
// build from.
type Spec struct {
	Owner, Repo string // cache identity: ~/.cache/bfctl/repos/{owner}_{repo}
	URL         string // git remote of the addon repository
	Ref         string // tag, branch or commit to build ("" = default branch)
}

// Options tunes Build. Zero values select the shared caches under
// ~/.cache/bfctl and a fresh temporary output directory.
type Options struct {
	// OutDir is the directory the finished top-level addon folders are
	// composed into — the tree internal/installer consumes as its staging
	// input. It must be empty or not exist yet so a failed build never
	// leaves a partial install behind. When empty, a fresh temporary
	// directory is created.
	OutDir string

	// ReposDir is the shallow working-copy cache
	// (~/.cache/bfctl/repos by default).
	ReposDir string

	// Externals is the external cache libraries are fetched through
	// (the shared ~/.cache/bfctl/externals cache by default).
	Externals *ExternalCache
}

// Result is one successful build.
type Result struct {
	// Root holds one top-level folder per addon: the package-as folder
	// plus every move-folders destination, ready for the installer.
	Root string
	// Folders are the top-level addon folder names, sorted.
	Folders []string
	// Meta is the parsed .pkgmeta (empty when the repository has none).
	Meta *PkgMeta
}

// Build composes the addon package for spec into opts.OutDir:
//
//  1. materialize the shallow working copy at ~/.cache/bfctl/repos/{owner}_{repo};
//  2. export the tree with git archive (the export can never carry a .git);
//  3. parse .pkgmeta and strip ignored paths (gitignore semantics, plus the
//     always-stripped .git, .github and .pkgmeta);
//  4. fetch each external through the shared cache and copy it to its
//     destination path inside the package;
//  5. run the ignore patterns again so they also strip external content;
//  6. rename the package folder to package-as (the TOC base name);
//  7. move-folders: move each listed subtree to its top-level sibling
//     folder — a colliding destination is a hard error;
//  8. commit the finished top-level folders into OutDir.
//
// Every step works in a private temporary directory and only step 8 writes
// OutDir, so any failure leaves no partial install behind.
func Build(ctx context.Context, spec Spec, opts Options) (*Result, error) {
	if spec.URL == "" {
		return nil, errors.New("packager: repository URL is required")
	}
	if spec.Owner == "" || spec.Repo == "" {
		return nil, errors.New("packager: repository owner and name are required")
	}

	reposDir := opts.ReposDir
	if reposDir == "" {
		var err error
		reposDir, err = paths.CacheSubdir("repos")
		if err != nil {
			return nil, fmt.Errorf("resolve working-copy cache directory: %w", err)
		}
	}
	externals := opts.Externals
	if externals == nil {
		var err error
		externals, err = NewExternalCache()
		if err != nil {
			return nil, err
		}
	}
	outDir, err := prepareOutDir(opts.OutDir)
	if err != nil {
		return nil, err
	}

	work, err := os.MkdirTemp("", "bfctl-build-")
	if err != nil {
		return nil, fmt.Errorf("create build directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	compose := filepath.Join(work, "compose")

	wc, err := workingCopy(ctx, spec, reposDir)
	if err != nil {
		return nil, err
	}
	if err := exportTree(ctx, wc, compose); err != nil {
		return nil, err
	}
	meta, err := LoadPkgMeta(compose)
	if err != nil {
		return nil, err
	}
	matcher, err := compileIgnore(meta.Ignore)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPkgMeta, err)
	}
	if err := stripIgnored(compose, matcher); err != nil {
		return nil, err
	}

	dests := make([]string, 0, len(meta.Externals))
	for dest := range meta.Externals {
		dests = append(dests, dest)
	}
	sort.Strings(dests)
	for _, dest := range dests {
		ext := meta.Externals[dest]
		target := filepath.Join(compose, filepath.FromSlash(dest))
		if _, err := os.Stat(target); err == nil {
			return nil, fmt.Errorf("%w: externals: destination %q already exists in the repository — remove the vendored copy or the external", ErrInvalidPkgMeta, dest)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("externals: %q: %w", dest, err)
		}
		if err := externals.Materialize(ctx, ext, target); err != nil {
			return nil, fmt.Errorf("externals: %q: %w", dest, err)
		}
	}

	// The ignore patterns also strip external content (BigWigsMods/packager
	// re-applies ignore after checkout for the same reason).
	if err := stripIgnored(compose, matcher); err != nil {
		return nil, err
	}

	pkgName := meta.PackageAs
	if pkgName == "" {
		pkgName = spec.Repo
	}
	if err := applyPackageAs(compose, pkgName); err != nil {
		return nil, err
	}
	if err := applyMoveFolders(compose, meta, pkgName); err != nil {
		return nil, err
	}

	folders, err := commitTree(outDir, compose)
	if err != nil {
		return nil, err
	}
	return &Result{Root: outDir, Folders: folders, Meta: meta}, nil
}

// prepareOutDir resolves the compose target: an explicit directory must be
// empty (or not exist yet), and no partial tree is ever written to it.
func prepareOutDir(outDir string) (string, error) {
	if outDir == "" {
		dir, err := os.MkdirTemp("", "bfctl-pkg-")
		if err != nil {
			return "", fmt.Errorf("create output directory: %w", err)
		}
		return dir, nil
	}
	entries, err := os.ReadDir(outDir)
	if err == nil && len(entries) > 0 {
		return "", fmt.Errorf("packager: output directory %s is not empty", outDir)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect output directory %s: %w", outDir, err)
	}
	return outDir, nil
}

// workingCopy returns the shallow working copy of spec's repository at
// ~/.cache/bfctl/repos/{owner}_{repo}, checked out at spec.Ref. The copy is
// created once and then kept up to date with "git fetch --depth=1 origin
// <ref> && git checkout" as the plan specifies.
func workingCopy(ctx context.Context, spec Spec, reposDir string) (string, error) {
	dir := filepath.Join(reposDir, spec.Owner+"_"+spec.Repo)
	if err := os.MkdirAll(reposDir, 0o700); err != nil {
		return "", fmt.Errorf("create working-copy cache %s: %w", reposDir, err)
	}
	_, err := os.Stat(filepath.Join(dir, ".git"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.RemoveAll(dir); err != nil {
			return "", fmt.Errorf("clear stale working copy %s: %w", dir, err)
		}
		if spec.Ref == "" {
			if _, err := runGit(ctx, "", "clone", "--quiet", "--depth", "1", "--", spec.URL, dir); err != nil {
				return "", fmt.Errorf("clone %s: %w", spec.URL, err)
			}
			return dir, nil
		}
		if err := gitops.ShallowClone(ctx, spec.URL, spec.Ref, dir); err != nil {
			return "", err
		}
		return dir, nil
	case err != nil:
		return "", fmt.Errorf("inspect working copy %s: %w", dir, err)
	}

	ref := spec.Ref
	if ref == "" {
		ref = "HEAD" // the remote's default branch
	}
	if _, err := runGit(ctx, dir, "fetch", "--quiet", "--depth", "1", "origin", ref); err != nil {
		return "", fmt.Errorf("update working copy %s to %s (remove %s to re-clone): %w", dir, ref, dir, err)
	}
	if _, err := runGit(ctx, dir, "checkout", "--quiet", "--force", "--detach", "FETCH_HEAD"); err != nil {
		return "", fmt.Errorf("check out %s in working copy %s: %w", ref, dir, err)
	}
	return dir, nil
}

// exportTree exports the working copy's tree into dest with "git archive" —
// the export never contains .git metadata by construction.
func exportTree(ctx context.Context, wcDir, dest string) error {
	out, err := runGit(ctx, wcDir, "archive", "--format=tar", "HEAD")
	if err != nil {
		return fmt.Errorf("export %s: %w", wcDir, err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("create export directory %s: %w", dest, err)
	}
	tr := tar.NewReader(bytes.NewReader(out))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read git archive of %s: %w", wcDir, err)
		}
		target := filepath.Join(dest, filepath.FromSlash(hdr.Name))
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("git archive entry %q escapes the package", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFile(target, tr, hdr.FileInfo().Mode().Perm()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("git archive entry %q: only regular files and directories are allowed in an addon package", hdr.Name)
		}
	}
}

// writeFile writes one exported file with the given permission bits.
func writeFile(dst string, r io.Reader, perm os.FileMode) error {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// runGit runs one git invocation and returns its stdout. stderr is folded
// into the error; prompts are disabled so a misconfigured remote fails
// instead of blocking on input.
func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("%w; install git and retry", gitops.ErrGitMissing)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// alwaysStrip names the VCS and packaging metadata that is stripped from
// every package at any depth, regardless of the ignore patterns.
func alwaysStrip(name string) bool {
	return name == ".git" || name == ".github" || name == PkgMetaName || name == pkgMetaAltName
}

// stripIgnored removes every path under root the matcher strips (gitignore
// semantics: the last matching pattern decides, so "!" re-includes) plus the
// always-stripped metadata names. Matched directories are removed with their
// whole subtree, exactly like gitignore excludes them, and directories left
// empty by the strip are pruned — git tracks no empty directories, so the
// export only ever contains them as strip leftovers.
func stripIgnored(root string, m *ignoreMatcher) error {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if alwaysStrip(d.Name()) || m.match(rel, d.IsDir()) {
			if err := os.RemoveAll(p); err != nil {
				return fmt.Errorf("strip %s: %w", rel, err)
			}
			if d.IsDir() {
				return fs.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return pruneEmptyDirs(root)
}

// pruneEmptyDirs removes empty directories under root (but never root
// itself), deepest first.
func pruneEmptyDirs(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != root {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		entries, err := os.ReadDir(dirs[i])
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			if err := os.Remove(dirs[i]); err != nil {
				return fmt.Errorf("prune empty directory %s: %w", dirs[i], err)
			}
		}
	}
	return nil
}

// ignoreMatcher holds compiled gitignore-style .pkgmeta "ignore" patterns.
type ignoreMatcher struct {
	patterns []ignorePattern
}

// ignorePattern is one compiled pattern.
type ignorePattern struct {
	negate   bool
	dirOnly  bool
	baseOnly bool     // pattern without a slash: match base names at any depth
	segs     []string // glob segments; "**" matches any run of segments
}

// compileIgnore compiles gitignore-style patterns. A pattern that is not a
// valid glob fails with an error wrapping ErrInvalidPkgMeta.
func compileIgnore(patterns []string) (*ignoreMatcher, error) {
	m := &ignoreMatcher{}
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		pat := ignorePattern{}
		if rest, ok := strings.CutPrefix(p, "!"); ok {
			pat.negate = true
			p = rest
		}
		if rest, ok := strings.CutSuffix(p, "/"); ok {
			pat.dirOnly = true
			p = rest
		}
		if p == "" {
			return nil, fmt.Errorf("ignore: pattern %q matches nothing", raw)
		}
		pat.baseOnly = !strings.Contains(p, "/")
		if strings.HasPrefix(p, "/") {
			p = strings.TrimPrefix(p, "/") // leading "/" anchors to the root
			pat.baseOnly = false
		}
		pat.segs = strings.Split(p, "/")
		for _, seg := range pat.segs {
			if seg == "" {
				return nil, fmt.Errorf("ignore: pattern %q contains an empty path segment", raw)
			}
			if seg == "**" {
				continue
			}
			if _, err := path.Match(seg, seg); err != nil {
				return nil, fmt.Errorf("ignore: pattern %q: %w", raw, err)
			}
		}
		m.patterns = append(m.patterns, pat)
	}
	return m, nil
}

// match reports whether the package-relative path rel is stripped. The last
// matching pattern decides, so a later "!" pattern re-includes a path;
// patterns ending in "/" only match directories.
func (m *ignoreMatcher) match(rel string, isDir bool) bool {
	stripped := false
	for _, p := range m.patterns {
		if p.dirOnly && !isDir {
			continue
		}
		if p.matches(rel) {
			stripped = !p.negate
		}
	}
	return stripped
}

// matches tests one pattern against a package-relative path.
func (p ignorePattern) matches(rel string) bool {
	var segs []string
	if p.baseOnly {
		segs = []string{path.Base(rel)}
	} else {
		segs = strings.Split(rel, "/")
	}
	return matchSegments(p.segs, segs)
}

// matchSegments matches pattern segments against path segments, where a
// "**" segment matches any run of segments (including none).
func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegments(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// applyPackageAs wraps the exported tree in one top-level folder named name
// ("package-as"): the folder name is the TOC base name the TOC flavor work in
// ISSUE-10 consumes. The content is parked in a temporary folder first so a
// repository that itself contains a top-level entry named like the package
// cannot collide with the rename.
func applyPackageAs(root, name string) error {
	tmp, err := os.MkdirTemp(root, ".pkg-")
	if err != nil {
		return fmt.Errorf("collect package content: %w", err)
	}
	tmpName := filepath.Base(tmp)
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == tmpName {
			continue
		}
		if err := os.Rename(filepath.Join(root, entry.Name()), filepath.Join(tmp, entry.Name())); err != nil {
			return fmt.Errorf("collect package content %q: %w", entry.Name(), err)
		}
	}
	if err := os.Rename(tmp, filepath.Join(root, name)); err != nil {
		return fmt.Errorf("rename package folder to %q: %w", name, err)
	}
	return nil
}

// applyMoveFolders moves each move-folders subtree to its top-level sibling
// folder and prunes the parent folders left empty, matching the reference
// implementation. Sources are resolved the way BigWigsMods/packager writes
// them — "Monkey/Modules/Suit" includes the package folder as its first
// component — and a source without that prefix is accepted as shorthand for
// a subtree of the package. A destination that already exists or is used
// twice is a hard error: bfctl never overwrites an addon folder.
func applyMoveFolders(root string, meta *PkgMeta, pkgName string) error {
	srcs := make([]string, 0, len(meta.MoveFolders))
	for src := range meta.MoveFolders {
		srcs = append(srcs, src)
	}
	sort.Strings(srcs)
	used := make(map[string]bool, len(srcs))
	for _, src := range srcs {
		dest := meta.MoveFolders[src]
		from := filepath.Join(root, filepath.FromSlash(src))
		if _, err := os.Stat(from); errors.Is(err, os.ErrNotExist) {
			from = filepath.Join(root, pkgName, filepath.FromSlash(src))
		}
		if _, err := os.Stat(from); err != nil {
			return fmt.Errorf("move-folders: source %q does not exist in the package", src)
		}
		to := filepath.Join(root, dest)
		if used[dest] {
			return fmt.Errorf("move-folders: destination %q is used more than once", dest)
		}
		if _, err := os.Stat(to); err == nil {
			return fmt.Errorf("move-folders: destination %q collides with an existing folder", dest)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("move-folders: destination %q: %w", dest, err)
		}
		used[dest] = true
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("move-folders: move %q to %q: %w", src, dest, err)
		}
		pruneEmptyParents(filepath.Dir(from), root, filepath.Join(root, pkgName))
	}
	return nil
}

// pruneEmptyParents removes directories left empty by a move-folders move,
// walking up from dir until a non-empty folder or one of the protected
// boundaries (the package folder and the compose root) is reached.
func pruneEmptyParents(dir, root, pkgDir string) {
	for dir != root && dir != pkgDir {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// commitTree moves the finished top-level folders from compose into outDir,
// rolling back its own moves if any fails, so the installer's staging input
// is either complete or untouched. Renames fall back to copy+remove when the
// build directory and the staging directory live on different filesystems.
func commitTree(outDir, compose string) ([]string, error) {
	entries, err := os.ReadDir(compose)
	if err != nil {
		return nil, fmt.Errorf("read composed package: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output directory %s: %w", outDir, err)
	}
	var folders, moved []string
	rollback := func() {
		for _, m := range moved {
			_ = os.RemoveAll(m)
		}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			rollback()
			return nil, fmt.Errorf("composed package contains unexpected file %q", entry.Name())
		}
		src := filepath.Join(compose, entry.Name())
		dst := filepath.Join(outDir, entry.Name())
		if err := os.Rename(src, dst); err != nil {
			if err := copyTreeFiltered(dst, src, func(string) bool { return false }); err != nil {
				_ = os.RemoveAll(dst)
				rollback()
				return nil, fmt.Errorf("compose %q into %s: %w", entry.Name(), outDir, err)
			}
			_ = os.RemoveAll(src)
		}
		moved = append(moved, dst)
		folders = append(folders, entry.Name())
	}
	if len(folders) == 0 {
		rollback()
		return nil, errors.New("packager: the package contains no addon folders (everything was ignored?)")
	}
	sort.Strings(folders)
	return folders, nil
}
