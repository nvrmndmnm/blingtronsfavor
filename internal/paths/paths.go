// Package paths resolves bfctl's XDG base directories and creates them
// lazily. Everything bfctl writes lives under ~/.config/bfctl and
// ~/.cache/bfctl (or their XDG_* overrides) and is created 0700.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	appName = "bfctl"
	// dirPerm is used for every directory bfctl creates (owner only).
	dirPerm = 0o700
)

// ExpandPath resolves a leading "~" in path to the user's home directory so
// shell-quoted paths like "~/games/wow" work as expected; a bare "~" is
// likewise expanded. Other paths, including "~user" forms, are returned
// unchanged.
func ExpandPath(path string) (string, error) {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return home, nil
	}
	rest, ok := strings.CutPrefix(path, "~/")
	if !ok {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, rest), nil
}

// ConfigDir returns the bfctl configuration directory:
// $XDG_CONFIG_HOME/bfctl, falling back to ~/.config/bfctl.
func ConfigDir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, appName), nil
}

// CacheDir returns the bfctl cache directory:
// $XDG_CACHE_HOME/bfctl, falling back to ~/.cache/bfctl.
func CacheDir() (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, appName), nil
}

// StateFile returns the path of the addon state file inside the config
// directory. It does not create anything.
func StateFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// EnsureDir creates dir and its parents with 0700 if they do not exist yet.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, dirPerm)
}

// EnsureLayout creates the config and cache directories on first use.
func EnsureLayout() error {
	cfgDir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := EnsureDir(cfgDir); err != nil {
		return err
	}
	cacheDir, err := CacheDir()
	if err != nil {
		return err
	}
	return EnsureDir(cacheDir)
}

// HTTPCacheName is the cache subdirectory holding HTTP response cache
// entries (the GitHub API client's ETag cache).
const HTTPCacheName = "http"

// HTTPCacheDir returns the cache subdirectory for HTTP response cache
// entries, creating it lazily with 0700.
func HTTPCacheDir() (string, error) {
	return CacheSubdir(HTTPCacheName)
}

// CacheSubdir returns a named cache subdirectory, creating it lazily.
func CacheSubdir(name string) (string, error) {
	cacheDir, err := CacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cacheDir, name)
	if err := EnsureDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}
