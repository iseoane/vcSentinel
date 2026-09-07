package graph

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

type packagesLoader func(*packages.Config, ...string) ([]*packages.Package, error)
type verifyPhase uint8

type loadContext struct {
	GOOS, GOARCH, GoVersion, GOROOT, CGO, GOCACHE, GOPATH, GOMODCACHE string
	SystemRoot, Temp                                                  string
	Mode                                                              packages.LoadMode
	Tests                                                             bool
	Patterns, BuildFlags                                              []string
}

func currentLoadContext() loadContext {
	home, _ := os.UserHomeDir()
	cache, _ := os.UserCacheDir()
	gopath := filepath.Join(home, "go")
	return loadContext{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), GOROOT: runtime.GOROOT(), CGO: "0",
		GOCACHE: filepath.Join(cache, "go-build"), GOPATH: gopath, GOMODCACHE: filepath.Join(gopath, "pkg", "mod"),
		SystemRoot: os.Getenv("SystemRoot"), Temp: os.TempDir(),
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedEmbedFiles | packages.NeedForTest | packages.NeedModule, Tests: true,
		Patterns: []string{"./..."}, BuildFlags: []string{"-mod=readonly", "-tags="},
	}
}

func (c loadContext) env() []string {
	env := []string{
		"GOOS=" + c.GOOS, "GOARCH=" + c.GOARCH, "CGO_ENABLED=" + c.CGO,
		"GOROOT=" + c.GOROOT, "PATH=" + filepath.Join(c.GOROOT, "bin"),
		"GOCACHE=" + c.GOCACHE, "GOPATH=" + c.GOPATH, "GOMODCACHE=" + c.GOMODCACHE,
		"GOWORK=off", "GOFLAGS=", "GOEXPERIMENT=none", "GOTOOLCHAIN=local", "GOENV=off",
		"GOPROXY=off", "GOSUMDB=off",
	}
	return append(env, "SystemRoot="+c.SystemRoot, "TMPDIR="+c.Temp, "TEMP="+c.Temp, "TMP="+c.Temp)
}

func (c loadContext) fingerprint() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%q\n%d\n%t\n%q\n%q", append([]string{c.GoVersion}, c.env()...), c.Mode, c.Tests, c.Patterns, c.BuildFlags)))
	return fmt.Sprintf("%x", sum)
}

func (c loadContext) config(dir string) *packages.Config {
	return &packages.Config{Mode: c.Mode, Dir: dir, Env: c.env(), Tests: c.Tests, BuildFlags: clone(c.BuildFlags)}
}

func (c *loadContext) prepare(dir string) {
	c.BuildFlags = []string{"-mod=readonly", "-tags="}
	vendor, err := os.Lstat(filepath.Join(dir, "vendor"))
	modules, modErr := os.Lstat(filepath.Join(dir, "vendor", "modules.txt"))
	if err == nil && modErr == nil && vendor.IsDir() && vendor.Mode()&os.ModeSymlink == 0 && modules.Mode().IsRegular() && modules.Mode()&os.ModeSymlink == 0 {
		c.BuildFlags[0] = "-mod=vendor"
	}
}

const (
	verifyIdentity verifyPhase = iota
	verifyConsumed
)

type verifyRequest struct {
	dir, identity string
	phase         verifyPhase
	consumed      []string
}

type snapshotVerifier func(verifyRequest) error

type nativeProvider struct {
	dir      string
	identity string
	load     packagesLoader
	verify   snapshotVerifier
	context  loadContext
	imports  map[string][]string
	files    map[string]string
	tests    map[string]string
}

// NewNativeProvider anchors each load to a snapshot directory and its tree OID.
func NewNativeProvider(snapshotDir, treeOID string) *nativeProvider {
	dir := ""
	if snapshotDir != "" {
		dir, _ = filepath.Abs(snapshotDir)
	}
	return &nativeProvider{dir: dir, identity: treeOID, load: packages.Load, verify: verifyGitSnapshot, context: currentLoadContext()}
}

func (*nativeProvider) Name() string { return "native" }

func (p *nativeProvider) Analyze(paths []string) (AnalysisResult, error) {
	paths = normalize(paths)
	reasons := []string{}
	if p.identity == "" {
		reasons = append(reasons, "empty snapshot identity")
	}
	if p.dir == "" {
		reasons = append(reasons, "empty snapshot directory")
	} else if dir, err := filepath.EvalSymlinks(p.dir); err != nil {
		reasons = append(reasons, "snapshot unresolvable: "+err.Error())
	} else {
		p.dir = dir
		request := verifyRequest{dir: p.dir, identity: p.identity, phase: verifyIdentity}
		if err := p.verify(request); err != nil {
			reasons = append(reasons, "snapshot unverifiable: "+err.Error())
		}
	}
	p.context.prepare(p.dir)
	config := p.context.config(p.dir)
	var pkgs []*packages.Package
	var err error
	consumed, cacheHit, _ := p.loadCache()
	if p.dir != "" && !cacheHit {
		pkgs, err = p.load(config, p.context.Patterns...)
	}
	if err != nil {
		reasons = append(reasons, "load error: "+err.Error())
	}
	if !cacheHit {
		p.buildGraph(pkgs, &reasons)
		consumed = consumedFiles(pkgs)
	}
	cachePending := !cacheHit && len(reasons) == 0 && len(pkgs) > 0
	if !cacheHit && len(pkgs) == 0 {
		reasons = append(reasons, "load error: no packages loaded")
	}

	scope := affectedScope{}
	uncovered := []string{}
	changed := map[string][]string{}
	for _, path := range paths {
		pkg, covered := p.files[path]
		pathReasons := []string{}
		resolved, err := resolveInSnapshot(p.dir, path)
		if err != nil {
			pathReasons = append(pathReasons, err.Error())
			covered = false
		} else {
			pathReasons = detectRisks(resolved, path)
		}
		if isGlobalConfig(path) {
			pathReasons = append(pathReasons, fmt.Sprintf("global configuration changed: %s", path))
		}
		if !covered {
			pathReasons = append(pathReasons, fmt.Sprintf("file does not belong to any loaded package: %s", path))
		} else {
			changed[pkg] = append(changed[pkg], path)
		}
		if len(pathReasons) > 0 {
			reasons = append(reasons, pathReasons...)
			uncovered = append(uncovered, path)
		}
	}
	p.expandScope(changed, &scope)
	if p.dir != "" {
		request := verifyRequest{
			dir: p.dir, identity: p.identity,
			phase: verifyConsumed, consumed: consumed,
		}
		if err := p.verify(request); err != nil {
			reasons = append(reasons, "snapshot unverifiable at completion: "+err.Error())
		} else if cachePending {
			_ = p.saveCache(consumed)
		}
	}
	if len(reasons) > 0 && len(uncovered) == 0 {
		uncovered = append(uncovered, paths...)
	}
	sortUnique(&reasons)
	sortUnique(&uncovered)
	sortUnique(&scope.packages)
	sortUnique(&scope.tests)
	sortUnique(&scope.explanation)
	evidence := completenessEvidence{complete: len(reasons) == 0, uncovered: uncovered}
	if len(reasons) > 0 {
		evidence.reason = strings.Join(reasons, "; ")
	}
	return newAnalysisResult(p.identity, paths, scope, evidence), nil
}

func (p *nativeProvider) buildGraph(pkgs []*packages.Package, reasons *[]string) {
	p.imports, p.files, p.tests = map[string][]string{}, map[string]string{}, map[string]string{}
	for _, pkg := range pkgs {
		if strings.HasSuffix(pkg.PkgPath, ".test") {
			continue
		}
		for _, failure := range pkg.Errors {
			*reasons = append(*reasons, fmt.Sprintf("load error for %s: %s", pkg.PkgPath, failure.Msg))
		}
		if pkg.ForTest == "" {
			for imported := range pkg.Imports {
				p.imports[pkg.PkgPath] = append(p.imports[pkg.PkgPath], imported)
			}
		}
		for _, file := range append(append(pkg.GoFiles, pkg.OtherFiles...), pkg.EmbedFiles...) {
			resolved, err := filepath.EvalSymlinks(file)
			if err != nil || !withinDir(p.dir, resolved) {
				*reasons = append(*reasons, "package file outside the snapshot")
				continue
			}
			relative, _ := filepath.Rel(p.dir, resolved)
			path := filepath.ToSlash(relative)
			p.files[path] = pkg.PkgPath
			if strings.HasSuffix(path, "_test.go") {
				p.tests[pkg.PkgPath] = "./" + filepath.ToSlash(filepath.Dir(path))
			}
		}
	}
	for pkg := range p.imports {
		imported := p.imports[pkg]
		sortUnique(&imported)
		p.imports[pkg] = imported
	}
}

func (p *nativeProvider) expandScope(changed map[string][]string, scope *affectedScope) {
	reverse := map[string][]string{}
	for importer, imports := range p.imports {
		for _, imported := range imports {
			reverse[imported] = append(reverse[imported], importer)
		}
	}
	queue := make([]string, 0, len(changed))
	chains := map[string]string{}
	for pkg, paths := range changed {
		sortUnique(&paths)
		queue = append(queue, pkg)
		chains[pkg] = pkg
		scope.explanation = append(scope.explanation, fmt.Sprintf("%s: direct change in %s", pkg, strings.Join(paths, ", ")))
	}
	sort.Strings(queue)
	for i := 0; i < len(queue); i++ {
		pkg := queue[i]
		scope.packages = append(scope.packages, pkg)
		if test := p.tests[pkg]; test != "" {
			scope.tests = append(scope.tests, test)
		}
		importers := reverse[pkg]
		sortUnique(&importers)
		for _, importer := range importers {
			if _, seen := chains[importer]; seen {
				continue
			}
			chains[importer] = importer + " -> " + chains[pkg]
			scope.explanation = append(scope.explanation, fmt.Sprintf("%s: imports %s (via %s)", importer, pkg, chains[importer]))
			queue = append(queue, importer)
		}
	}
}

func detectRisks(resolvedPath, path string) []string {
	if filepath.Ext(path) != ".go" {
		return nil
	}
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return []string{fmt.Sprintf("file unreadable: %s", path)}
	}
	risks := []string{}
	if strings.Contains(string(content), "//go:linkname") {
		risks = append(risks, "go:linkname directive in "+path)
	}
	file, _ := parser.ParseFile(token.NewFileSet(), path, content, parser.ImportsOnly)
	if file != nil {
		for _, imported := range file.Imports {
			name, _ := strconv.Unquote(imported.Path.Value)
			if name == "reflect" || name == "plugin" {
				risks = append(risks, "use of "+name+" in "+path)
			}
		}
	}
	return risks
}

func isGlobalConfig(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(filepath.FromSlash(path))
	return path == "go.mod" || path == "go.sum" || base == "makefile" || base == "dockerfile" ||
		strings.HasPrefix(path, ".github/workflows/") || base == ".gitlab-ci.yml" || base == "azure-pipelines.yml" ||
		base == "build.sh" || base == "build.bat" || base == "build.cmd" || base == "build.ps1" ||
		base == "build.yml" || base == "build.yaml" || base == "build.xml"
}

func consumedFiles(pkgs []*packages.Package) []string {
	var files []string
	for _, pkg := range pkgs {
		if strings.HasSuffix(pkg.PkgPath, ".test") {
			continue
		}
		files = append(files, pkg.GoFiles...)
		files = append(files, pkg.CompiledGoFiles...)
		files = append(files, pkg.OtherFiles...)
		files = append(files, pkg.EmbedFiles...)
		if pkg.Module != nil {
			files = append(files, pkg.Module.GoMod)
			if pkg.Module.Replace != nil {
				files = append(files, pkg.Module.Replace.GoMod)
			}
		}
	}
	sortUnique(&files)
	return files
}

func verifyGitSnapshot(request verifyRequest) error {
	dir, expected := request.dir, request.identity
	gitDir, err := gitSnapshot(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("Git repository unverifiable")
	}
	gitDir = strings.TrimSpace(gitDir)
	tree, err := gitInRepo(dir, gitDir, nil, "rev-parse", "HEAD^{tree}")
	if err != nil || strings.TrimSpace(tree) != expected {
		return fmt.Errorf("tree OID differs from expected")
	}
	untracked, err := gitInRepo(dir, gitDir, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil || untracked != "" {
		return fmt.Errorf("dirty directory or untracked files")
	}
	output, err := gitInRepo(dir, gitDir, nil, "ls-tree", "-rz", "--full-tree", expected)
	if err != nil {
		return fmt.Errorf("expected tree unreadable")
	}
	blobs := map[string]string{}
	for _, entry := range strings.Split(output, "\x00") {
		header, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(header)
		if ok && len(fields) == 3 && fields[1] == "blob" {
			blobs[path] = fields[2]
		}
	}
	consumed := request.consumed
	if request.phase == verifyConsumed {
		for _, path := range []string{"go.mod", "go.sum", filepath.Join("vendor", "modules.txt")} {
			if _, ok := blobs[filepath.ToSlash(path)]; ok {
				consumed = append(consumed, filepath.Join(dir, path))
			} else if _, err := os.Stat(filepath.Join(dir, path)); err == nil {
				consumed = append(consumed, filepath.Join(dir, path))
			}
		}
	}
	sortUnique(&consumed)
	for _, file := range consumed {
		if file == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(file)
		if err != nil || !withinDir(dir, resolved) {
			return fmt.Errorf("consumed blob outside the snapshot")
		}
		relative, _ := filepath.Rel(dir, resolved)
		path := filepath.ToSlash(relative)
		expectedBlob, ok := blobs[path]
		if !ok {
			return fmt.Errorf("consumed blob untracked: %s", path)
		}
		content, err := os.ReadFile(resolved)
		if err != nil {
			return fmt.Errorf("consumed blob unreadable: %s", path)
		}
		actual, err := gitInRepo(dir, gitDir, content, "hash-object", "--stdin")
		if err != nil || strings.TrimSpace(actual) != expectedBlob {
			return fmt.Errorf("consumed blob differs from tree: %s", path)
		}
	}
	return nil
}

func gitSnapshot(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "core.attributesFile=" + os.DevNull, "-c", "diff.external=", "-C", dir}, args...)...)
	cmd.Env = sanitizedGitEnv()
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func gitInRepo(dir, gitDir string, input []byte, args ...string) (string, error) {
	base := []string{"-c", "core.attributesFile=" + os.DevNull, "-c", "diff.external=", "--git-dir", gitDir, "--work-tree", dir}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Env = sanitizedGitEnv()
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func sanitizedGitEnv() []string {
	env := []string{"LC_ALL=C", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0"}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "GIT_") && name != "LANG" && name != "LC_ALL" && !strings.HasPrefix(name, "LC_") && name != "GOWORK" {
			env = append(env, entry)
		}
	}
	return env
}

func resolveInSnapshot(dir, path string) (string, error) {
	native := filepath.FromSlash(path)
	if path == ".." || strings.HasPrefix(path, "../") || filepath.IsAbs(native) {
		return "", fmt.Errorf("path outside the snapshot: %s", path)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(dir, native))
	if err != nil {
		return "", fmt.Errorf("path unresolvable in snapshot: %s", path)
	}
	if !withinDir(dir, resolved) {
		return "", fmt.Errorf("path outside the snapshot: %s", path)
	}
	return resolved, nil
}

func withinDir(dir, path string) bool {
	relative, err := filepath.Rel(dir, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func normalize(paths []string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		result = append(result, filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))))
	}
	sortUnique(&result)
	return result
}

func sortUnique(values *[]string) {
	sort.Strings(*values)
	dest := (*values)[:0]
	for _, value := range *values {
		if len(dest) == 0 || dest[len(dest)-1] != value {
			dest = append(dest, value)
		}
	}
	*values = dest
}
