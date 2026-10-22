package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	buildInfoName  = ".build.info"
	flavorInfoName = ".flavor.info"
)

// ErrBuildInfoMissing is wrapped into errors returned by Detect when the
// install root has no .build.info file.
var ErrBuildInfoMissing = errors.New(".build.info not found")

// Detect inspects the flavor directory (e.g. ".../World of Warcraft/_forever_")
// and its parent install root, and reports the flavor, product identifier and
// interface version of that client.
func Detect(flavorDir string) (*ClientInstall, error) {
	resolved, err := filepath.Abs(flavorDir)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", flavorDir, err)
	}
	if link, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = link // tolerate symlinked install paths
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("inspect flavor dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", resolved)
	}
	root := filepath.Dir(resolved)

	// .flavor.info carries the Product identifier; without it the flavor is
	// inferred from the flavor directory name.
	product := ""
	if data, err := os.ReadFile(filepath.Join(resolved, flavorInfoName)); err == nil {
		product = parseFlavorInfo(string(data))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", flavorInfoName, err)
	}
	flavor, ptrFromProduct := flavorFromProduct(product)
	dirFlavor, ptrFromDir := flavorFromProduct(filepath.Base(resolved))
	if product == "" {
		flavor = dirFlavor
	}

	data, err := os.ReadFile(filepath.Join(root, buildInfoName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrBuildInfoMissing, filepath.Join(root, buildInfoName))
		}
		return nil, fmt.Errorf("read %s: %w", buildInfoName, err)
	}
	rows, err := parseBuildInfo(string(data))
	if err != nil {
		return nil, err
	}
	version, err := selectBuildRow(rows, product, flavor)
	if err != nil {
		return nil, err
	}
	return &ClientInstall{
		Root:      root,
		FlavorDir: resolved,
		Flavor:    flavor,
		Product:   product,
		Version:   version,
		Interface: version.Interface(),
		IsPtr:     ptrFromProduct || ptrFromDir,
	}, nil
}

// parseFlavorInfo extracts the product identifier from .flavor.info. The file
// carries one two-line record per entry: a "Key!TYPE:idx" header line
// followed by the value line, e.g.
//
//	Product Flavor!STRING:0
//	wowf
//
// Key=Value lines are tolerated as a fallback. It returns "" when the file
// carries no product key.
func parseFlavorInfo(data string) string {
	lines := strings.Split(data, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if key, _, ok := strings.Cut(line, "!"); ok {
			if !isProductKey(key) {
				continue
			}
			for _, value := range lines[i+1:] {
				if value = strings.TrimSpace(value); value != "" {
					return value
				}
			}
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok && isProductKey(key) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// isProductKey reports whether a .flavor.info key names the product identifier.
func isProductKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "product", "product flavor":
		return true
	}
	return false
}

// flavorFromProduct maps a product identifier or flavor directory name to
// the nearest flavor and reports PTR/Beta markers in it. PTR/Beta product
// identifiers carry a "ptr"/"beta" marker or a trailing "t" ("wowt"), and map
// to the nearest real flavor (e.g. "wowxptr" -> mainline, PTR).
func flavorFromProduct(product string) (Flavor, bool) {
	t := strings.ToLower(strings.TrimSpace(product))
	isPtr := strings.Contains(t, "ptr") || strings.Contains(t, "beta") ||
		(strings.HasSuffix(t, "t") && len(t) > 2)
	switch {
	case t == "wowf", strings.Contains(t, "forever"), strings.Contains(t, "camelot"):
		return FlavorForever, isPtr
	case strings.Contains(t, "mists"), strings.Contains(t, "progression"):
		return FlavorMists, isPtr
	case strings.Contains(t, "vanilla"), strings.Contains(t, "classic"), strings.Contains(t, "era"):
		return FlavorClassic, isPtr
	default:
		return FlavorMainline, isPtr
	}
}
