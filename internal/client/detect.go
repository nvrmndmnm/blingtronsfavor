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
// and its parent install root, and reports the flavor, product token and
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

	// .flavor.info carries the Product token; without it the flavor is
	// inferred from the flavor directory name.
	product := ""
	if data, err := os.ReadFile(filepath.Join(resolved, flavorInfoName)); err == nil {
		product = parseFlavorInfo(string(data))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", flavorInfoName, err)
	}
	flavor, ptrFromProduct := flavorFromToken(product)
	dirFlavor, ptrFromDir := flavorFromToken(filepath.Base(resolved))
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

// parseFlavorInfo extracts the Product token from .flavor.info Key=Value
// lines. It returns "" when the file carries no Product key.
func parseFlavorInfo(data string) string {
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "Product") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// flavorFromToken maps a product token or flavor directory name to the
// nearest flavor and reports PTR/Beta markers in it. PTR/Beta tokens carry a
// "ptr"/"beta" marker or a trailing "t" ("wowt"), and map to the nearest real
// flavor (e.g. "wowxptr" -> mainline, PTR).
func flavorFromToken(token string) (Flavor, bool) {
	t := strings.ToLower(strings.TrimSpace(token))
	isPtr := strings.Contains(t, "ptr") || strings.Contains(t, "beta") ||
		(strings.HasSuffix(t, "t") && len(t) > 2)
	switch {
	case strings.Contains(t, "forever"), strings.Contains(t, "camelot"):
		return FlavorForever, isPtr
	case strings.Contains(t, "mists"), strings.Contains(t, "progression"):
		return FlavorMists, isPtr
	case strings.Contains(t, "vanilla"), strings.Contains(t, "classic"), strings.Contains(t, "era"):
		return FlavorClassic, isPtr
	default:
		return FlavorMainline, isPtr
	}
}
