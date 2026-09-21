package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/tools/go/packages"
)

func TestNativeProviderLoadsBasicGraph(t *testing.T) {
	dir := createModule(t, map[string]string{
		"go.mod":                         "module example.test/snapshot\n\ngo 1.26\n",
		"internal/git/diff.go":           "package git\n",
		"internal/git/diff_test.go":      "package git\nimport \"testing\"\nfunc TestDiff(t *testing.T) {}\n",
		"internal/review/engine.go":      "package review\nimport _ \"example.test/snapshot/internal/git\"\n",
		"internal/review/engine_test.go": "package review\nimport \"testing\"\nfunc TestEngine(t *testing.T) {}\n",
		"cmd/sentinel/main.go":           "package main\nimport _ \"example.test/snapshot/internal/review\"\nfunc main() {}\n",
		"cmd/sentinel/main_test.go":      "package main\nimport \"testing\"\nfunc TestMain(t *testing.T) {}\n",
	})
	p := providerForModule(t, dir)
	result, err := p.Analyze([]string{"internal/git/diff.go", "internal/git/diff.go"})
	if err != nil || !result.Complete() {
		t.Fatalf("analysis = %+v, error = %v, reason = %q", result, err, result.ReasonIncomplete())
	}
	scope := result.Scope()
	if !reflect.DeepEqual(scope.Packages(), []string{
		"example.test/snapshot/cmd/sentinel", "example.test/snapshot/internal/git", "example.test/snapshot/internal/review",
	}) || !reflect.DeepEqual(scope.Tests(), []string{"./cmd/sentinel", "./internal/git", "./internal/review"}) {
		t.Fatalf("scope = packages %v, tests %v", scope.Packages(), scope.Tests())
	}
	explanation := strings.Join(scope.Explanation(), "\n")
	for _, part := range []string{"internal/git/diff.go", "internal/review", "cmd/sentinel", "imports"} {
		if !strings.Contains(explanation, part) {
			t.Fatalf("explanation missing %q:\n%s", part, explanation)
		}
	}
}

func TestNativeProviderCachesLoadPerTreeOID(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "a/a.go": "package a\n", "b/b.go": "package b\nimport _ \"example.test/s/a\"\n"})
	loads := 0
	analyze := func(oid, path, goos string) AnalysisResult {
		p := NewNativeProvider(dir, oid)
		if goos != "" {
			p.context.GOOS = goos
		}
		load := p.load
		p.load = func(config *packages.Config, patterns ...string) ([]*packages.Package, error) {
			loads++
			return load(config, patterns...)
		}
		result, _ := p.Analyze([]string{path})
		return result
	}

	oid := treeOID(t, dir)
	analyze(oid, "a/a.go", "")
	second := analyze(oid, "b/b.go", "")
	loadsSameTree := loads
	goos := "windows"
	if currentLoadContext().GOOS == goos {
		goos = "linux"
	}
	analyze(oid, "a/a.go", goos)
	loadsOtherContext := loads
	analyze(oid, "a/a.go", "")
	loadsOriginalContext := loads
	os.WriteFile(filepath.Join(dir, "b", "b.go"), []byte("package b\n"), 0644)
	git(t, dir, "commit", "-qam", "second tree")
	analyze(treeOID(t, dir), "b/b.go", "")
	if loadsSameTree != 1 || loadsOtherContext != 2 || loadsOriginalContext != 2 || loads != 3 || !reflect.DeepEqual(second.Scope().Packages(), []string{"example.test/s/b"}) {
		t.Fatalf("loads same/other/original/tree=%d/%d/%d/%d; scope=%v", loadsSameTree, loadsOtherContext, loadsOriginalContext, loads, second.Scope().Packages())
	}
}

func TestNativeProviderLoadsHermeticVendor(t *testing.T) {
	dir := createModule(t, map[string]string{
		"go.mod":                         "module example.test/s\n\ngo 1.26\n\nrequire example.test/dep v1.0.0\n",
		"main.go":                        "package s\nimport _ \"example.test/dep\"\n",
		"vendor/modules.txt":             "# example.test/dep v1.0.0\n## explicit; go 1.26\nexample.test/dep\n",
		"vendor/example.test/dep/dep.go": "package dep\n",
	})
	p := providerForModule(t, dir)
	p.context.GOPATH, p.context.GOMODCACHE, p.context.GOCACHE = t.TempDir(), t.TempDir(), t.TempDir()
	result, _ := p.Analyze([]string{"main.go"})
	if !result.Complete() || !reflect.DeepEqual(p.context.BuildFlags, []string{"-mod=vendor", "-tags="}) {
		t.Fatalf("complete=%v flags=%v reason=%q", result.Complete(), p.context.BuildFlags, result.ReasonIncomplete())
	}
}

func TestNativeProviderIncompleteVendorUsesReadonly(t *testing.T) {
	for _, name := range []string{"missing", "symlink"} {
		t.Run(name, func(t *testing.T) {
			dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
			if err := os.Mkdir(filepath.Join(dir, "vendor"), 0755); err != nil {
				t.Fatal(err)
			}
			if name == "symlink" {
				if err := os.Symlink(filepath.Join(dir, "go.mod"), filepath.Join(dir, "vendor", "modules.txt")); err != nil {
					t.Skipf("symlinks not available: %v", err)
				}
			}
			p := providerForModule(t, dir)
			if !reflect.DeepEqual(p.context.BuildFlags, []string{"-mod=readonly", "-tags="}) {
				t.Fatalf("flags=%v", p.context.BuildFlags)
			}
		})
	}
}

func TestNativeProviderCacheContentionDoesNotAffectCompleteness(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	root, err := cacheRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	unlock, err := lockCache(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	start := time.Now()
	p := providerForModule(t, dir)
	result, err := p.Analyze([]string{"main.go"})
	if err != nil || !result.Complete() || result.ReasonIncomplete() != "" {
		t.Fatalf("complete=%v error=%v reason=%q", result.Complete(), err, result.ReasonIncomplete())
	}
	if _, ok := p.readValidCache(); ok {
		t.Fatal("contention did not skip persistence")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cache contention did not stay bounded")
	}
}

func TestReleasedCacheLockAllowsWrite(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	root, err := cacheRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	unlock, err := lockCache(root)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	p := NewNativeProvider(dir, oid)
	p.imports, p.files, p.tests = map[string][]string{}, map[string]string{"main.go": "example.test/s"}, map[string]string{}
	if err := p.saveCache([]string{filepath.Join(dir, "main.go")}); err != nil {
		t.Fatalf("the released lock did not allow writing: %v", err)
	}
	info, err := root.Lstat(".write.lock")
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("unsafe lock file: info=%v error=%v", info, err)
	}
	if cache, ok := p.readValidCache(); !ok || cache.Entries[p.context.fingerprint()].TreeOID != oid {
		t.Fatalf("subsequent write invalid: %#v", cache)
	}
}

func TestNativeProviderCacheEvictsNinthContext(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	fingerprints := make([]string, 0, maxCacheEntries+1)
	for i := range maxCacheEntries + 1 {
		p := NewNativeProvider(dir, oid)
		p.context.GoVersion = fmt.Sprintf("test-version-%d", i)
		fingerprints = append(fingerprints, p.context.fingerprint())
		result, _ := p.Analyze([]string{"main.go"})
		if !result.Complete() {
			t.Fatalf("context %d incomplete: %s", i, result.ReasonIncomplete())
		}
	}
	cache, ok := NewNativeProvider(dir, oid).readValidCache()
	if !ok || len(cache.Entries) != maxCacheEntries {
		t.Fatalf("cache valid=%v entries=%d", ok, len(cache.Entries))
	}
	sort.Strings(fingerprints[:maxCacheEntries])
	if _, exists := cache.Entries[fingerprints[0]]; exists || cache.Entries[fingerprints[maxCacheEntries]].TreeOID != oid {
		t.Fatalf("non-deterministic eviction: %#v", cache.Entries)
	}
}

func TestCacheCombinesIndependentWriters(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	p1, p2 := NewNativeProvider(dir, oid), NewNativeProvider(dir, oid)
	p1.context.Temp, p2.context.Temp = "writer-1", "writer-2"
	for _, p := range []*nativeProvider{p1, p2} {
		p.imports, p.files, p.tests = map[string][]string{}, map[string]string{"main.go": "example.test/s"}, map[string]string{}
	}
	var wg sync.WaitGroup
	for _, p := range []*nativeProvider{p1, p2} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.saveCacheWithoutMutex([]string{filepath.Join(dir, "main.go")}) }()
	}
	wg.Wait()
	cache, ok := p1.readValidCache()
	if !ok || cache.Entries[p1.context.fingerprint()].TreeOID != oid || cache.Entries[p2.context.fingerprint()].TreeOID != oid {
		t.Fatalf("a writer was lost: %#v", cache.Entries)
	}
}

func TestNativeProviderRecoversFromUntrustedCache(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	if result, _ := NewNativeProvider(dir, oid).Analyze([]string{"main.go"}); !result.Complete() {
		t.Fatal(result.ReasonIncomplete())
	}
	path := filepath.Join(dir, ".git", "vcsentinel", "graph", oid+".json")
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		prepare func()
	}{
		{"malformed", func() { os.WriteFile(path, []byte("{"), 0600) }},
		{"different version", func() {
			os.WriteFile(path, []byte(strings.Replace(string(valid), `"version": 3`, `"version": 1`, 1)), 0600)
		}},
		{"different tree", func() {
			os.WriteFile(path, []byte(strings.Replace(string(valid), oid, strings.Repeat("0", len(oid)), 1)), 0600)
		}},
		{"different fingerprint", func() {
			fingerprint := NewNativeProvider(dir, oid).context.fingerprint()
			os.WriteFile(path, []byte(strings.Replace(string(valid), `"`+fingerprint+`":`, `"`+strings.Repeat("0", 64)+`":`, 1)), 0600)
		}},
		{"oversized", func() { os.WriteFile(path, make([]byte, maxGraphCacheSize+1), 0600) }},
		{"symlink", func() {
			os.Remove(path)
			if err := os.Symlink(filepath.Join(t.TempDir(), "target"), path); err != nil {
				t.Skipf("symlinks not available: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.prepare()
			p := NewNativeProvider(dir, oid)
			loads := 0
			load := p.load
			p.load = func(c *packages.Config, patterns ...string) ([]*packages.Package, error) {
				loads++
				return load(c, patterns...)
			}
			result, _ := p.Analyze([]string{"main.go"})
			if !result.Complete() || loads != 1 {
				t.Fatalf("complete=%v loads=%d reason=%q", result.Complete(), loads, result.ReasonIncomplete())
			}
			if tc.name == "symlink" {
				return
			}
			valid, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeProviderPersistsOnlyAfterVerification(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	p := NewNativeProvider(dir, oid)
	p.verify = func(r verifyRequest) error {
		if r.phase == verifyConsumed {
			return errors.New("final failure")
		}
		return verifyGitSnapshot(r)
	}
	result, _ := p.Analyze([]string{"main.go"})
	path := filepath.Join(dir, ".git", "vcsentinel", "graph", oid+".json")
	if result.Complete() || !errors.Is(func() error { _, err := os.Stat(path); return err }(), os.ErrNotExist) {
		t.Fatalf("result=%q persisted cache=%v", result.ReasonIncomplete(), path)
	}
}

func TestNativeProviderConcurrentCacheIsSafe(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	var loads atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan string, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := NewNativeProvider(dir, oid)
			load := p.load
			p.load = func(c *packages.Config, patterns ...string) ([]*packages.Package, error) {
				loads.Add(1)
				return load(c, patterns...)
			}
			result, _ := p.Analyze([]string{"main.go"})
			if !result.Complete() {
				errs <- result.ReasonIncomplete()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if loads.Load() == 0 {
		t.Fatal("no reader built the cache")
	}
	p := NewNativeProvider(dir, oid)
	p.load = func(*packages.Config, ...string) ([]*packages.Package, error) {
		return nil, errors.New("loader must not run")
	}
	result, _ := p.Analyze([]string{"main.go"})
	if !result.Complete() {
		t.Fatal(result.ReasonIncomplete())
	}
	path := filepath.Join(dir, ".git", "vcsentinel", "graph", oid+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("cache entry not regular: %v %v", info, err)
	}
	var cache graphCache
	data, _ := os.ReadFile(path)
	if json.Unmarshal(data, &cache) != nil || !cache.valid(oid) || cache.Entries[p.context.fingerprint()].TreeOID != oid {
		t.Fatalf("final cache invalid: %s", data)
	}
}

func TestNativeProviderFailsClosed(t *testing.T) {
	cases := []struct {
		name, path, content, reason string
	}{
		{"load error", "bad.go", "package broken\nfunc {", "load"},
		{"reflect", "main.go", "package sample\nimport \"reflect\"\nvar _ = reflect.TypeOf(1)\n", "reflect"},
		{"plugin", "main.go", "package sample\nimport \"plugin\"\nvar _ = plugin.Open\n", "plugin"},
		{"go linkname", "main.go", "package sample\nimport _ \"unsafe\"\n//go:linkname f x.f\nfunc f()\n", "go:linkname"},
		{"uncovered file", "z.txt", "text\n", "any loaded package"},
		{"go mod", "go.mod", "module example.test/changed\n", "global configuration"},
		{"makefile", "Makefile", "all:\n\t@true\n", "global configuration"},
		{"build script", "build.sh", "go build ./...\n", "global configuration"},
		{"ci", filepath.Join(".github", "workflows", "ci.yml"), "name: ci\n", "global configuration"},
		{"missing identity", "main.go", "package sample\n", "snapshot identity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{
				"go.mod":  "module example.test/snapshot\n\ngo 1.26\n",
				"base.go": "package sample\n",
			}
			files[tc.path] = tc.content
			dir := createModule(t, files)
			identity := "tree-case"
			if tc.name == "missing identity" {
				identity = ""
			}
			if identity != "" {
				identity = treeOID(t, dir)
			}
			result, err := NewNativeProvider(dir, identity).Analyze([]string{tc.path})
			if err != nil || result.Complete() || !strings.Contains(result.ReasonIncomplete(), tc.reason) {
				t.Fatalf("complete=%v, error=%v, reason=%q", result.Complete(), err, result.ReasonIncomplete())
			}
			if !reflect.DeepEqual(result.Uncovered(), []string{filepath.ToSlash(tc.path)}) {
				t.Fatalf("uncovered = %v", result.Uncovered())
			}
		})
	}
}

func TestNativeProviderVerifiesSnapshotBeforeTrusting(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	cases := []struct {
		name, oid string
		mutate    func()
	}{
		{"different tree", strings.Repeat("0", 40), func() {}},
		{"mutated tracked file", treeOID(t, dir), func() { os.WriteFile(filepath.Join(dir, "main.go"), []byte("package s\n// dirty\n"), 0644) }},
		{"untracked file", treeOID(t, dir), func() { os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("dirty"), 0644) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.mutate()
			result, _ := NewNativeProvider(dir, tc.oid).Analyze([]string{"main.go"})
			if result.Complete() || !strings.Contains(result.ReasonIncomplete(), "snapshot") {
				t.Fatalf("snapshot not verified: complete=%v reason=%q", result.Complete(), result.ReasonIncomplete())
			}
			git(t, dir, "reset", "--hard", "-q")
			os.Remove(filepath.Join(dir, "extra.txt"))
		})
	}
}

func TestNativeProviderAttestsConsumedFiles(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"ignored go", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.go\n"), 0644)
			git(t, dir, "add", ".gitignore")
			git(t, dir, "commit", "-qm", "ignore")
			os.WriteFile(filepath.Join(dir, "ignored.go"), []byte("package s\n"), 0644)
		}},
		{"tracked assume unchanged", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, "main.go"), []byte("package s\n// altered\n"), 0644)
			git(t, dir, "update-index", "--assume-unchanged", "main.go")
			if state := strings.TrimSpace(git(t, dir, "status", "--porcelain")); state != "" {
				t.Fatalf("the fixture must hide the change from status: %q", state)
			}
		}},
		{"module metadata", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hostile.test/s\n\ngo 1.26\n"), 0644)
			git(t, dir, "update-index", "--assume-unchanged", "go.mod")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
			tc.mutate(t, dir)
			result, _ := providerForModule(t, dir).Analyze([]string{"main.go"})
			if result.Complete() || !strings.Contains(result.ReasonIncomplete(), "blob") {
				t.Fatalf("bytes not attested: complete=%v reason=%q", result.Complete(), result.ReasonIncomplete())
			}
		})
	}
}

func TestNativeProviderIgnoresGoWorkWhenDisabled(t *testing.T) {
	dir := createModule(t, map[string]string{
		"go.mod":  "module example.test/s\n\ngo 1.26\n",
		"go.work": "go 1.26\n\nuse .\n",
		"main.go": "package s\n",
	})
	oid := treeOID(t, dir)
	os.WriteFile(filepath.Join(dir, "go.work"), []byte("go 1.26\n\nuse ./missing\n"), 0644)
	git(t, dir, "update-index", "--assume-unchanged", "go.work")
	result, _ := NewNativeProvider(dir, oid).Analyze([]string{"main.go"})
	if !result.Complete() {
		t.Fatalf("go.work consumed despite GOWORK=off: %q", result.ReasonIncomplete())
	}
}

func TestNativeProviderIgnoresReplacementObjects(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	original := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	expected := treeOID(t, dir)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package hostile\n"), 0644)
	git(t, dir, "commit", "-qam", "replacement")
	replacement := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	git(t, dir, "reset", "--hard", "-q", original)
	git(t, dir, "replace", original, replacement)
	if actual := treeOID(t, dir); actual == expected {
		t.Fatal("refs/replace did not alter HEAD^{tree}; the fixture does not exercise the defense")
	}
	result, _ := NewNativeProvider(dir, expected).Analyze([]string{"main.go"})
	if !result.Complete() {
		t.Fatalf("refs/replace altered the attestation: %q", result.ReasonIncomplete())
	}
}

func TestNativeProviderSanitizesGitEnvironment(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	hostile := createModule(t, map[string]string{"go.mod": "module hostile.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	t.Setenv("GIT_DIR", filepath.Join(hostile, ".git"))
	t.Setenv("GIT_WORK_TREE", hostile)
	hostileConfig := filepath.Join(hostile, "hostile-config")
	os.WriteFile(hostileConfig, []byte("invalid configuration\n"), 0644)
	t.Setenv("GIT_CONFIG_GLOBAL", hostileConfig)
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	cmd.Env = os.Environ()
	if err := cmd.Run(); err == nil {
		t.Fatal("the hostile global configuration does not affect Git; the fixture does not exercise the sanitization")
	}
	result, _ := NewNativeProvider(dir, oid).Analyze([]string{"main.go"})
	if !result.Complete() {
		t.Fatalf("the Git environment redirected the attestation: %q", result.ReasonIncomplete())
	}
}

func TestNativeProviderRevalidatesAfterLoad(t *testing.T) {
	dir := createModule(t, map[string]string{
		"go.mod":   "module example.test/s\n\ngo 1.26\n",
		"main.go":  "package s\n",
		"other.go": "package s\n",
	})
	p := providerForModule(t, dir)
	load := p.load
	p.load = func(config *packages.Config, patterns ...string) ([]*packages.Package, error) {
		pkgs, err := load(config, patterns...)
		os.WriteFile(filepath.Join(dir, "main.go"), []byte("package s\n// mutated during load\n"), 0644)
		return pkgs, err
	}
	result, _ := p.Analyze([]string{"main.go", "other.go"})
	if result.Complete() || !reflect.DeepEqual(result.Uncovered(), []string{"main.go", "other.go"}) {
		t.Fatalf("temporary mutation authorized: complete=%v uncovered=%v reason=%q", result.Complete(), result.Uncovered(), result.ReasonIncomplete())
	}
}

func TestNativeProviderRejectsSymlinkEscape(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	outside := filepath.Join(t.TempDir(), "outside.go")
	os.WriteFile(outside, []byte("package outside\n"), 0644)
	link := filepath.Join(dir, "escape.go")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "symlink")
	result, _ := providerForModule(t, dir).Analyze([]string{"escape.go"})
	if result.Complete() || !strings.Contains(result.ReasonIncomplete(), "outside the snapshot") {
		t.Fatalf("escape authorized: %q", result.ReasonIncomplete())
	}
	os.Remove(outside)
	result, _ = providerForModule(t, dir).Analyze([]string{"escape.go"})
	if result.Complete() || !strings.Contains(result.ReasonIncomplete(), "unresolvable") {
		t.Fatalf("broken symlink authorized: %q", result.ReasonIncomplete())
	}
}

func TestBuildGoIsOrdinarySource(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "build.go": "package s\n"})
	link := filepath.Join(t.TempDir(), "snapshot")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	result, _ := NewNativeProvider(link, treeOID(t, dir)).Analyze([]string{"build.go"})
	if !result.Complete() {
		t.Fatalf("build.go flagged as global: %s", result.ReasonIncomplete())
	}
}

func TestReverseClosureWithCycleIsDeterministic(t *testing.T) {
	p := &nativeProvider{imports: map[string][]string{"a": {"b"}, "b": {"a"}}, tests: map[string]string{"a": "./a", "b": "./b"}}
	var first, second affectedScope
	p.expandScope(map[string][]string{"a": {"a.go", "a.go"}}, &first)
	p.expandScope(map[string][]string{"a": {"a.go", "a.go"}}, &second)
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(first.packages, []string{"a", "b"}) {
		t.Fatalf("non-deterministic closure: %+v / %+v", first, second)
	}
}
func TestLoadDoesNotAuthorizeFilesOutsideSnapshot(t *testing.T) {
	dir := createModule(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	outside := filepath.Join(t.TempDir(), "outside.go")
	os.WriteFile(outside, []byte("package outside\n"), 0644)
	p := providerForModule(t, dir)
	p.load = func(*packages.Config, ...string) ([]*packages.Package, error) {
		return []*packages.Package{{PkgPath: "example.test/outside", GoFiles: []string{outside}}}, nil
	}
	result, _ := p.Analyze([]string{"main.go"})
	if result.Complete() || !strings.Contains(result.ReasonIncomplete(), "package file outside") {
		t.Fatalf("external file authorized: %q", result.ReasonIncomplete())
	}
	if strings.Contains(result.ReasonIncomplete(), outside) {
		t.Fatalf("reason exposes absolute consumed path: %q", result.ReasonIncomplete())
	}
}

func createModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for path, content := range files {
		path = filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "fixture@example.test")
	git(t, dir, "config", "user.name", "Fixture")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "fixture")
	return dir
}

func providerForModule(t *testing.T, dir string) *nativeProvider {
	t.Helper()
	return NewNativeProvider(dir, treeOID(t, dir))
}

func treeOID(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(git(t, dir, "rev-parse", "HEAD^{tree}"))
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
