package installer

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/itp5b5/blessforge/internal/toc"
)

// stage is a temporary directory created inside the directory that contains
// Interface/AddOns. The addon payload is copied into stage/<Dir> and only
// ever reaches Interface/AddOns via rename(2) once it is fully validated,
// which is what makes an install atomic (risk R11).
type stage struct {
	dir       string
	addonsDir string
}

// newStage creates the staging directory next to addonsDir and verifies that
// both share a filesystem device (via the st_dev of their stat), because
// rename(2) across devices fails with EXDEV and there is no copy fallback:
// the rename is exactly what makes the install atomic. A symlinked or
// separately mounted Interface/AddOns is therefore rejected up front with an
// actionable error instead of failing halfway through an install.
func newStage(addonsDir string) (*stage, error) {
	parent := filepath.Dir(addonsDir)
	if err := os.MkdirAll(addonsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create addon directory %s: %w", addonsDir, err)
	}
	dir, err := os.MkdirTemp(parent, ".bfctl-stage-")
	if err != nil {
		return nil, fmt.Errorf("create staging dir next to %s: %w", addonsDir, err)
	}
	if err := requireSameDev(addonsDir, dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &stage{dir: dir, addonsDir: addonsDir}, nil
}

// cleanup removes the staging directory. It is idempotent and safe to defer.
func (s *stage) cleanup() error {
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("remove staging dir %s: %w", s.dir, err)
	}
	return nil
}

// requireSameDev fails with an EXDEV-style error unless a and b live on the
// same filesystem device, i.e. unless rename(2) between them is a rename and
// not a copy.
func requireSameDev(a, b string) error {
	devA, err := devOf(a)
	if err != nil {
		return err
	}
	devB, err := devOf(b)
	if err != nil {
		return err
	}
	if devA == devB {
		return nil
	}
	return fmt.Errorf("cannot install atomically: %s (device %d) and %s (device %d) are on different filesystems: %w — bfctl never falls back to copying because rename(2) is what makes installs atomic; keep Interface/AddOns and the bfctl cache on the same filesystem",
		a, devA, b, devB, syscall.EXDEV)
}

// devOf returns the st_dev of path, the identity of the filesystem it lives
// on. It mirrors the unix.Stat_t.Dev check the installer plan calls for,
// using the stdlib syscall package the state store already relies on.
func devOf(path string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return uint64(st.Dev), nil
}

// populate copies the addon's top-level directories from srcDir into the
// stage as stage/<Dir> and returns their names, sorted. Top-level files are
// payload metadata (README, .pkgmeta) and are skipped; symlinks anywhere in
// the payload are rejected so a malicious or malformed archive cannot plant
// one inside Interface/AddOns.
func (s *stage) populate(srcDir string) ([]string, error) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil, fmt.Errorf("read addon payload %s: %w", srcDir, err)
	}
	var names []string
	for _, entry := range entries {
		src := filepath.Join(srcDir, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("staging %s: symlink %q is not allowed in an addon payload", srcDir, entry.Name())
		}
		if !entry.IsDir() {
			continue
		}
		if err := copyTree(filepath.Join(s.dir, entry.Name()), src); err != nil {
			return nil, err
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("staging %s: addon payload contains no addon directories", srcDir)
	}
	sort.Strings(names)
	return names, nil
}

// copyTree copies the directory tree at src to dst, rejecting symlinks and
// other non-regular entries.
func copyTree(dst, src string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("staging %s: symlink %q is not allowed in an addon payload", src, rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("staging %s: %q is not a regular file", src, rel)
		}
		return copyFile(target, path, info.Mode().Perm())
	})
}

// copyFile copies one regular file, preserving its permission bits.
func copyFile(dst, src string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("staging %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("staging %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("staging %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("staging %s: %w", dst, err)
	}
	return nil
}

// validateStaged checks every staged top-level directory before anything
// touches Interface/AddOns: it must contain exactly one matching .toc — a
// .toc whose name equals the directory name — or a valid flavor set, i.e.
// one such .toc per flavor slot ("Foo.toc", "Foo_Mainline.toc", ...), and
// nothing else. Nothing is installed unless the whole payload validates.
func validateStaged(stageDir string, names []string) error {
	for _, name := range names {
		if _, err := matchingTocs(filepath.Join(stageDir, name), name); err != nil {
			return err
		}
	}
	return nil
}

// matchingTocs parses the .toc files of one staged addon directory and
// validates them against the directory name. It returns the parsed tocs in
// directory order; the error is nil only when the directory carries exactly
// one matching .toc per flavor slot. toc.Parse only reports a non-empty
// Suffix for a known flavor suffix, so a matching name plus a unique suffix
// slot is exactly the "one matching .toc or a valid flavor set" rule.
func matchingTocs(dir, name string) ([]*toc.Toc, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("staged addon directory %q: %w", name, err)
	}
	var tocs []*toc.Toc
	slotFile := map[string]string{} // lowercase flavor slot -> file name
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".toc") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("staged addon directory %q: symlink %q is not allowed in an addon payload", name, entry.Name())
		}
		t, err := toc.Parse(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("staged addon directory %q: %w", name, err)
		}
		if !strings.EqualFold(t.Name, name) {
			return nil, fmt.Errorf("staged addon directory %q contains %s, which does not match the directory name: every addon directory needs exactly one matching .toc (or a valid flavor set)",
				name, entry.Name())
		}
		slot := strings.ToLower(t.Suffix)
		if prev, dup := slotFile[slot]; dup {
			return nil, fmt.Errorf("staged addon directory %q contains %s and %s, both declaring the same flavor slot: exactly one .toc per flavor slot",
				name, prev, entry.Name())
		}
		slotFile[slot] = entry.Name()
		tocs = append(tocs, t)
	}
	if len(tocs) == 0 {
		return nil, fmt.Errorf("staged addon directory %q contains no %s.toc (or flavor-suffixed variant): every addon directory needs exactly one matching .toc (or a valid flavor set)",
			name, name)
	}
	return tocs, nil
}
