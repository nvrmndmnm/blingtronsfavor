package toc

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

// RewriteInterface rewrites the "## Interface:" directive of the .toc file at
// tocPath to iface. The UTF-8 BOM and the file's line endings are preserved
// and the directive keeps its position; duplicate Interface directives are
// collapsed into the rewritten one and a missing directive is appended at the
// end of the file. The file's permission bits are kept.
func RewriteInterface(tocPath string, iface int) error {
	info, err := os.Stat(tocPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(tocPath)
	if err != nil {
		return err
	}
	var prefix []byte
	if bytes.HasPrefix(data, utf8BOM) {
		prefix = utf8BOM
		data = data[len(utf8BOM):]
	}
	eol := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		eol = "\r\n"
	}

	directive := fmt.Sprintf("## Interface: %d", iface)
	lines := splitLines(data)
	out := make([]string, 0, len(lines)+2)
	replaced := false
	for _, line := range lines {
		if key, _, ok := splitDirective(line); ok && strings.EqualFold(key, "interface") {
			if replaced {
				continue // collapse duplicate directives
			}
			out = append(out, directive)
			replaced = true
			continue
		}
		out = append(out, line)
	}
	if !replaced {
		// lines (and therefore out) carries a trailing empty segment when
		// the file ends with a newline; insert before it to keep the
		// trailing newline, otherwise append one.
		if last := len(out) - 1; last >= 0 && out[last] == "" {
			out = append(out[:last], directive, "")
		} else {
			out = append(out, directive, "")
		}
	}

	result := append([]byte(nil), prefix...)
	result = append(result, strings.Join(out, eol)...)
	return os.WriteFile(tocPath, result, info.Mode().Perm())
}
