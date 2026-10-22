package github

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// cachePerm is used for every cache file the client writes (owner only).
const cachePerm = 0o600

// cacheEntry is one cached HTTP response, persisted as
// <cacheDir>/<sha256(method+url)>.json with the shape
// {"etag": …, "fetched_at": …, "status": …, "body": …}. Body holds the raw
// response bytes so a cached body replays byte for byte.
type cacheEntry struct {
	ETag      string    `json:"etag"`
	FetchedAt time.Time `json:"fetched_at"`
	Status    int       `json:"status"`
	Body      []byte    `json:"body"`
}

// expired reports whether the entry is older than ttl. Expired entries are
// refetched unconditionally instead of being revalidated with If-None-Match;
// absent or expired cache always falls back to the network.
func (e *cacheEntry) expired(ttl time.Duration, now time.Time) bool {
	return now.Sub(e.FetchedAt) >= ttl
}

// cachePath returns the cache file path for one method/url pair: the hex
// sha256 of "method+url" inside dir.
func cachePath(dir, method, rawURL string) string {
	sum := sha256.Sum256([]byte(method + rawURL))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
}

// loadCache reads the cache file at path. A missing, unreadable or corrupt
// file yields nil: the cache is an optimization and never a hard dependency
// of a request.
func loadCache(path string) *cacheEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil
	}
	return &e
}

// saveCache atomically replaces the cache file at path with e so a concurrent
// reader never observes a partial entry.
func saveCache(path string, e *cacheEntry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode cache entry: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create cache temp file: %w", err)
	}
	name := tmp.Name()
	if err := tmp.Chmod(cachePerm); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("chmod cache entry %s: %w", name, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("write cache entry %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close cache entry %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("replace cache entry %s: %w", path, err)
	}
	return nil
}
