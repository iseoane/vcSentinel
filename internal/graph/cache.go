package graph

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	graphCacheVersion = 3
	maxGraphCacheSize = 16 << 20
	maxCacheEntries   = 8
)

type graphCache struct {
	Version int                   `json:"version"`
	Entries map[string]cacheEntry `json:"entries"`
}

type cacheEntry struct {
	TreeOID  string              `json:"tree_oid"`
	Imports  map[string][]string `json:"imports"`
	Files    map[string]string   `json:"files"`
	Tests    map[string]string   `json:"tests"`
	Consumed []string            `json:"consumed"`
}

var cacheMutex sync.Mutex

func lockCache(root *os.Root) (func(), error) {
	const name = ".write.lock"
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unsafe lock file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := openLockFile(root, name)
	if err != nil {
		return nil, err
	}
	fileInfo, errFile := file.Stat()
	pathInfo, errPath := root.Lstat(name)
	if errFile != nil || errPath != nil || !fileInfo.Mode().IsRegular() ||
		pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(fileInfo, pathInfo) {
		_ = file.Close()
		return nil, fmt.Errorf("unsafe lock file")
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		acquired, err := tryLockFile(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if acquired {
			return func() {
				_ = unlockFile(file)
				_ = file.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("lock wait timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func decodeCache(data []byte) (graphCache, bool) {
	var c graphCache
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	ok := dec.Decode(&c) == nil && dec.Decode(&struct{}{}) == io.EOF
	return c, ok
}

func safeOID(oid string) bool {
	_, err := hex.DecodeString(oid)
	return (len(oid) == 40 || len(oid) == 64) && err == nil && strings.ToLower(oid) == oid
}

func cacheRoot(dir string) (*os.Root, error) {
	common, err := gitSnapshot(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	common = strings.TrimSpace(common)
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	root, err := os.OpenRoot(common)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	path := filepath.Join("vas-sentinel", "graph")
	if err := root.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	app, appErr := root.Lstat("vas-sentinel")
	info, infoErr := root.Lstat(path)
	if appErr != nil || infoErr != nil || app.Mode()&os.ModeSymlink != 0 || !app.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("unsafe cache directory")
	}
	graph, err := root.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return graph, nil
}

func validCachePath(path string) bool {
	native := filepath.FromSlash(path)
	return path != "" && path != "." && path != ".." && !filepath.IsAbs(native) && !strings.HasPrefix(path, "../") && filepath.ToSlash(filepath.Clean(native)) == path
}

func (c graphCache) valid(oid string) bool {
	if c.Version != graphCacheVersion || !safeOID(oid) || len(c.Entries) == 0 || len(c.Entries) > maxCacheEntries {
		return false
	}
	for fingerprint, entry := range c.Entries {
		if len(fingerprint) != 64 || entry.TreeOID != oid || !entry.valid() {
			return false
		}
	}
	return true
}

func (c cacheEntry) valid() bool {
	if len(c.Files) == 0 {
		return false
	}
	for _, path := range c.Consumed {
		if !validCachePath(path) {
			return false
		}
	}
	for pkg, imports := range c.Imports {
		if pkg == "" || !optionalValid(imports) {
			return false
		}
	}
	for path, pkg := range c.Files {
		if pkg == "" || !validCachePath(path) {
			return false
		}
	}
	for pkg, path := range c.Tests {
		if pkg == "" || !strings.HasPrefix(path, "./") || path != "./." && !validCachePath(strings.TrimPrefix(path, "./")) {
			return false
		}
	}
	return true
}

func (p *nativeProvider) loadCache() ([]string, bool, error) {
	if !safeOID(p.identity) {
		return nil, false, nil
	}
	root, err := cacheRoot(p.dir)
	if err != nil {
		return nil, false, nil
	}
	defer root.Close()
	name := p.identity + ".json"
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("unsafe cache entry")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	limited := &io.LimitedReader{R: file, N: maxGraphCacheSize + 1}
	data, err := io.ReadAll(limited)
	if err != nil || len(data) > maxGraphCacheSize {
		return nil, false, nil
	}
	c, ok := decodeCache(data)
	if !ok || !c.valid(p.identity) {
		return nil, false, nil
	}
	entry, ok := c.Entries[p.context.fingerprint()]
	if !ok {
		return nil, false, nil
	}
	p.imports, p.files, p.tests = cloneImports(entry.Imports), cloneMap(entry.Files), cloneMap(entry.Tests)
	for i, path := range entry.Consumed {
		entry.Consumed[i] = filepath.Join(p.dir, filepath.FromSlash(path))
	}
	return entry.Consumed, true, nil
}

func (p *nativeProvider) saveCache(consumed []string) error {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()
	return p.saveCacheWithoutMutex(consumed)
}

func (p *nativeProvider) saveCacheWithoutMutex(consumed []string) error {
	// Best-effort: failing to purge expired entries must never prevent
	// saving the cache that was just computed.
	_ = PurgeGraphCache(p.dir, GraphCacheRetention)
	relativePaths := make([]string, 0, len(consumed))
	for _, file := range consumed {
		relative, err := filepath.Rel(p.dir, file)
		if err != nil || !validCachePath(filepath.ToSlash(relative)) {
			return nil
		}
		relativePaths = append(relativePaths, filepath.ToSlash(relative))
	}
	sortUnique(&relativePaths)
	root, err := cacheRoot(p.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	unlock, err := lockCache(root)
	if err != nil {
		return err
	}
	defer unlock()
	c := graphCache{Version: graphCacheVersion, Entries: map[string]cacheEntry{}}
	if previous, ok := p.readValidCacheFrom(root); ok {
		c = previous
	}
	fingerprint := p.context.fingerprint()
	if _, exists := c.Entries[fingerprint]; !exists && len(c.Entries) >= maxCacheEntries {
		keys := make([]string, 0, len(c.Entries))
		for key := range c.Entries {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		delete(c.Entries, keys[0])
	}
	c.Entries[fingerprint] = cacheEntry{p.identity, cloneImports(p.imports), cloneMap(p.files), cloneMap(p.tests), relativePaths}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil || len(data)+1 > maxGraphCacheSize {
		return fmt.Errorf("cache exceeds the limit")
	}
	temp := fmt.Sprintf(".graph-%d.tmp", time.Now().UnixNano())
	defer root.Remove(temp)
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil || file.Close() != nil {
		return fmt.Errorf("cache write failed")
	}
	name := p.identity + ".json"
	if err := root.Rename(temp, name); err == nil {
		return nil
	}
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe existing cache entry")
		}
		if err := root.Remove(name); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return root.Rename(temp, name)
}

func (p *nativeProvider) readValidCache() (graphCache, bool) {
	root, err := cacheRoot(p.dir)
	if err != nil {
		return graphCache{}, false
	}
	defer root.Close()
	return p.readValidCacheFrom(root)
}

func (p *nativeProvider) readValidCacheFrom(root *os.Root) (graphCache, bool) {
	info, err := root.Lstat(p.identity + ".json")
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return graphCache{}, false
	}
	file, err := root.Open(p.identity + ".json")
	if err != nil {
		return graphCache{}, false
	}
	defer file.Close()
	data, err := io.ReadAll(&io.LimitedReader{R: file, N: maxGraphCacheSize + 1})
	c, ok := decodeCache(data)
	return c, err == nil && len(data) <= maxGraphCacheSize && ok && c.valid(p.identity)
}

func cloneImports(source map[string][]string) map[string][]string {
	dest := make(map[string][]string, len(source))
	for key, values := range source {
		dest[key] = clone(values)
	}
	return dest
}

func cloneMap(source map[string]string) map[string]string {
	dest := make(map[string]string, len(source))
	for key, value := range source {
		dest[key] = value
	}
	return dest
}

// GraphCacheRetention is the age at which a cache entry becomes disposable.
// It follows the same criterion as git.SnapshotRetention: the cache
// regenerates itself, so losing an entry only costs a recomputation, while
// keeping all of them for life costs nothing visible until the directory is
// already huge.
const GraphCacheRetention = 24 * time.Hour

// PurgeGraphCache removes cache entries older than age.
//
// It existed written and tested but WITHOUT A SINGLE CALLER in production,
// so the cache grew without bound. saveCacheWithoutMutex now invokes it:
// purging at write time bounds the residue by usage, the same pattern
// internal/validation/candidate.go uses to invoke git.PurgeSnapshots.
func PurgeGraphCache(dir string, age time.Duration) error {
	root, err := cacheRoot(dir)
	if err != nil {
		return err
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	defer root.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		oid := strings.TrimSuffix(entry.Name(), ".json")
		info, infoErr := entry.Info()
		if infoErr == nil && entry.Name() == oid+".json" && safeOID(oid) && info.Mode().IsRegular() && time.Since(info.ModTime()) >= age {
			_ = root.Remove(entry.Name())
		}
	}
	return nil
}
