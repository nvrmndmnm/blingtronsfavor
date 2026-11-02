package packager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/toc"
)

// splitTocTree runs the TOC flavor split on every top-level addon folder
// under root (the composed package: one folder per addon after package-as and
// move-folders).
func splitTocTree(root string, flavor client.Flavor, iface int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read composed package: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := splitTocDir(filepath.Join(root, entry.Name()), flavor, iface); err != nil {
			return err
		}
	}
	return nil
}

// splitTocDir makes the staged addon folder dir carry exactly one active
// .toc whose base name equals the folder name (from package-as /
// move-folders), targeted at the detected client:
//
//   - the .toc selected for flavor wins — a flavor-specific
//     "<folder>_<Suffix>.toc" beats the plain "<folder>.toc", which beats a
//     foreign-flavor toc (the same precedence "bfctl status" reports);
//   - every other .toc in the folder root is removed and the selected one is
//     renamed to "<folder>.toc";
//   - its "## Interface:" directive is rewritten to the single detected
//     interface (appended when missing).
//
// A multi-interface line is preserved only when the folder ships an
// intentional flavor suffix set — two or more flavor-specific tocs like
// "<folder>_Mainline.toc" plus "<folder>_Camelot.toc" — and the preserved
// line already lists the detected interface. Everywhere else the line is
// rewritten to exactly the detected interface, so the staged bundle always
// matches the local client.
func splitTocDir(dir string, flavor client.Flavor, iface int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read addon folder %s: %w", dir, err)
	}
	name := filepath.Base(dir)
	var tocs []*toc.Toc
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".toc") {
			continue
		}
		parsed, err := toc.Parse(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		tocs = append(tocs, parsed)
	}
	if len(tocs) == 0 {
		return fmt.Errorf("staged addon folder %s has no .toc file", name)
	}
	active, err := toc.Select(dir, flavor)
	if err != nil {
		return fmt.Errorf("select .toc for %s: %w", name, err)
	}

	// An intentional flavor suffix set: the author ships a family of
	// flavor-specific tocs ("Foo_Mainline.toc", "Foo_Camelot.toc", …).
	flavorSet := 0
	for _, t := range tocs {
		if t.Suffix != "" {
			flavorSet++
		}
	}

	for _, t := range tocs {
		if t.Path == active.Path {
			continue
		}
		if err := os.Remove(t.Path); err != nil {
			return fmt.Errorf("remove inactive %s: %w", filepath.Base(t.Path), err)
		}
	}
	target := filepath.Join(dir, name+".toc")
	if active.Path != target {
		if err := os.Rename(active.Path, target); err != nil {
			return fmt.Errorf("rename %s to %s: %w", filepath.Base(active.Path), name+".toc", err)
		}
	}

	if flavorSet >= 2 && len(active.Interfaces) > 1 && slices.Contains(active.Interfaces, iface) {
		// Intentional multi-interface line on a flavor-suffixed family: the
		// author's interface set is deliberate and already covers the
		// detected client, so it is preserved as written.
		return nil
	}
	if err := toc.RewriteInterface(target, iface); err != nil {
		return fmt.Errorf("rewrite ## Interface of %s: %w", name+".toc", err)
	}
	return nil
}

// Validate is the post-build gate the staged bundle must pass before the
// installer is called: every top-level addon folder carries its matching
// "<folder>.toc", that toc lists the detected client's interface, and no
// "@project-" substitution token is left in any .toc, .lua or .xml file.
func Validate(root string, iface int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("validate bundle: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		tocPath := filepath.Join(root, name, name+".toc")
		if _, err := os.Stat(tocPath); err != nil {
			return fmt.Errorf("validate bundle: staged addon folder %s has no matching %s.toc", name, name)
		}
		parsed, err := toc.Parse(tocPath)
		if err != nil {
			return fmt.Errorf("validate bundle: %s.toc: %w", name, err)
		}
		if !slices.Contains(parsed.Interfaces, iface) {
			return fmt.Errorf("validate bundle: %s.toc lists interface %v, not the detected interface %d", name, parsed.Interfaces, iface)
		}
	}
	return validateSubstituted(root)
}

// validateSubstituted rejects the bundle when a .toc, .lua or .xml file still
// carries an "@project-" substitution token — a misspelled or unknown keyword
// must never reach the installed addons.
func validateSubstituted(root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !substExtensions[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if i := strings.Index(string(data), "@project-"); i >= 0 {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				rel = p
			}
			token := string(data)[i:]
			if end := strings.IndexByte(token[1:], '@'); end >= 0 {
				token = token[:end+2] // the closing "@" of the token
			} else if len(token) > 24 {
				token = token[:24]
			}
			return fmt.Errorf("validate bundle: %s still carries the substitution token %s: %w", filepath.ToSlash(rel), token, ErrIncompleteSubstitution)
		}
		return nil
	})
}

// ErrIncompleteSubstitution marks bundles that fail the leftover-token part
// of the validation gate, so callers can tell a packaging bug from a layout
// problem.
var ErrIncompleteSubstitution = errors.New("incomplete keyword substitution")
