package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const schemaVersion = 1

type Entry struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (e Entry) Missing() bool {
	path, err := normalizePath(e.Path)
	if err != nil {
		return true
	}
	info, err := os.Stat(path)
	return err != nil || !info.IsDir()
}

type Registry struct {
	mu      sync.Mutex
	path    string
	entries map[string]Entry
}
type document struct {
	Version      int     `json:"version"`
	Repositories []Entry `json:"repositories"`
}

func Open(path string) (*Registry, error) {
	path, err := normalizePath(path)
	if err != nil {
		return nil, fmt.Errorf("registry: invalid registry path: %w", err)
	}
	r := &Registry{path: path, entries: make(map[string]Entry)}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return r, nil
		}
		return nil, fmt.Errorf("registry: cannot read %s: %w", path, err)
	}
	input, err := decode(data, path)
	if err != nil {
		return nil, err
	}
	for _, item := range input.Repositories {
		repositoryPath, err := normalizePath(filepath.FromSlash(item.Path))
		if err != nil {
			return nil, fmt.Errorf("registry: invalid repository path %q: %w", item.Path, err)
		}
		if _, exists := r.entries[repositoryPath]; exists {
			return nil, fmt.Errorf("registry: duplicate repository path %q", item.Path)
		}
		r.entries[repositoryPath] = Entry{Path: repositoryPath, Name: item.Name, Enabled: item.Enabled}
	}
	return r, nil
}
func (r *Registry) Register(dir string) (added bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dir, err = normalizePath(dir)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return false, fmt.Errorf("registry: cannot register %s: %w", dir, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("registry: repository path is not a directory: %s", dir)
	}
	previous, exists := r.entries[dir]
	next := Entry{Path: dir, Name: info.Name(), Enabled: true}
	if exists && previous == next {
		return false, nil
	}
	r.entries[dir] = next
	if err := r.writeLocked(); err != nil {
		if exists {
			r.entries[dir] = previous
		} else {
			delete(r.entries, dir)
		}
		return false, err
	}
	return !exists, nil
}
func (r *Registry) Remove(dir string) (removed bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dir, err = normalizePath(dir)
	if err != nil {
		return false, err
	}
	if _, exists := r.entries[dir]; !exists {
		return false, nil
	}
	previous := r.entries[dir]
	delete(r.entries, dir)
	if err := r.writeLocked(); err != nil {
		r.entries[dir] = previous
		return false, err
	}
	return true, nil
}
func (r *Registry) SetEnabled(dir string, enabled bool) (changed bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dir, err = normalizePath(dir)
	if err != nil {
		return false, err
	}
	previous, exists := r.entries[dir]
	if !exists || previous.Enabled == enabled {
		return false, nil
	}
	old := previous
	previous.Enabled = enabled
	r.entries[dir] = previous
	if err := r.writeLocked(); err != nil {
		r.entries[dir] = old
		return false, err
	}
	return true, nil
}
func (r *Registry) List() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listLocked()
}
func (r *Registry) listLocked() []Entry {
	entries := make([]Entry, 0, len(r.entries))
	for _, entry := range r.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}
func normalizePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		absolute = strings.ToLower(absolute)
	}
	return absolute, nil
}
func decode(data []byte, path string) (document, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var input document
	if err := decoder.Decode(&input); err != nil {
		return input, fmt.Errorf("registry: invalid JSON in %s: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return input, fmt.Errorf("registry: invalid JSON in %s: trailing data", path)
	}
	if input.Version != schemaVersion {
		return input, fmt.Errorf("registry: invalid version in %s", path)
	}
	if input.Repositories == nil {
		return input, fmt.Errorf("registry: repositories is required in %s", path)
	}
	return input, nil
}
func (r *Registry) writeLocked() error {
	directory := filepath.Dir(r.path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("registry: cannot create %s: %w", directory, err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return fmt.Errorf("registry: cannot protect %s: %w", directory, err)
	}
	entries := r.listLocked()
	for i := range entries {
		entries[i].Path = filepath.ToSlash(entries[i].Path)
	}
	data, err := json.MarshalIndent(document{Version: schemaVersion, Repositories: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("registry: cannot encode %s: %w", r.path, err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(directory, ".repositories-*.tmp")
	if err != nil {
		return fmt.Errorf("registry: cannot create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = temporary.Close(); _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, r.path)
}
