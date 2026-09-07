package change

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestComputeChangeProfileKind(t *testing.T) {
	cases := []struct {
		name, path, content, message, want string
		extra                              []syntheticFile
	}{
		{"generated dominates infra", "api/output.pb.go", "package api\n", "feat: generate API", "generated", []syntheticFile{{"infra/main.tf", "resource \"x\" \"y\" {}\n"}}},
		{"dependency", "go.mod", "module example.test/change\n", "chore: update dependency", "dependency", nil},
		{"infra", "infra/main.tf", "resource \"x\" \"y\" {}\n", "chore: infrastructure", "infra", nil},
		{"ci cd", ".github/workflows/ci.yml", "name: CI\n", "chore: pipeline", "ci_cd", nil},
		{"configuration", "config/app.toml", "name = \"sentinel\"\n", "chore: configuration", "configuration", nil},
		{"documentation", "README.md", "# Change\n", "docs: explain change", "documentation", nil},
		{"test only", "internal/demo/demo_test.go", "package demo\n", "test: covers demo", "test_only", nil},
		{"refactor", "internal/demo/demo.go", "package demo\nfunc Renamed() { println(\"same\") }\n", "chore: reorganize", "refactor", nil},
		{"bugfix", "internal/demo/demo.go", "package demo\nfunc State() int { return 1 }\n", "fix: correct state", "bugfix", nil},
		{"feature", "internal/new/new.go", "package new\nfunc Create() {}\n", "feat: create capability", "feature", nil},
	}

	initialDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(initialDirectory) })

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := t.TempDir()
			prepareRepository(t, repo)
			writeFile(t, repo, c.path, c.content)
			for _, extra := range c.extra {
				writeFile(t, repo, extra.path, extra.content)
			}
			runGit(t, repo, "add", ".")
			runGit(t, repo, "commit", "-m", c.message)
			if err := os.Chdir(repo); err != nil {
				t.Fatal(err)
			}

			profile, err := ComputeChangeProfile("HEAD~1", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if profile.Kind != c.want {
				t.Errorf("Kind = %q, want %q", profile.Kind, c.want)
			}
			if profile.Size.Files != 1+len(c.extra) || profile.Size.Added == 0 || profile.Size.Hunks == 0 {
				t.Errorf("Size = %#v, want files, lines and hunks of the diff", profile.Size)
			}
			if c.name == "generated dominates infra" && (profile.FileClasses[ClassGenerated] != 1 || profile.FileClasses[ClassInfra] != 1) {
				t.Errorf("FileClasses = %#v, want both classes counted", profile.FileClasses)
			}
		})
	}
}

func TestComputeCommitProfileUsesEmptyTreeForRootCommit(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	writeFile(t, repo, "internal/demo/demo.go", "package demo\nfunc Create() {}\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "feat: initial capability")
	t.Chdir(repo)

	profile, err := ComputeCommitProfile("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	emptyTree, err := exec.Command("git", "-C", repo, "hash-object", "-t", "tree", "--stdin").Output()
	if err != nil {
		t.Fatal(err)
	}
	if profile.Base != strings.TrimSpace(string(emptyTree)) || profile.Head != "HEAD" {
		t.Fatalf("profile base/head = %q/%q, expected empty tree/HEAD", profile.Base, profile.Head)
	}
	if profile.Size.Files != 1 || profile.Size.Added == 0 || profile.Kind != "feature" {
		t.Fatalf("incomplete root profile: %#v", profile)
	}
}

func TestComputeChangeProfileDerivesExactSymbols(t *testing.T) {
	cases := []struct {
		name, before, after string
		want                ChangeSymbols
	}{
		{"only body and parameter name", "func Public(a int) int { return a }", "func Public(b int) int { return b + 1 }", ChangeSymbols{Modified: 1, Complete: true}},
		{"generic parameter name", "func Public[T any](value T) T { return value }", "func Public[T any](other T) T { return other }", ChangeSymbols{Modified: 1, Complete: true}},
		{"generic constraint", "func Public[T any](value T) T { return value }", "func Public[T comparable](value T) T { return value }", ChangeSymbols{Modified: 1, ExportedTouched: 1, Complete: true}},
		{"generic result", "func Public[T any](value T) T { return value }", "func Public[T any](value T) []T { return nil }", ChangeSymbols{Modified: 1, ExportedTouched: 1, Complete: true}},
		{"signature", "func Public(a int) int { return a }", "func Public(a string) int { return len(a) }", ChangeSymbols{Modified: 1, ExportedTouched: 1, Complete: true}},
		{"parameter name in interface", "type Public interface { Method(a int) }", "type Public interface { Method(b int) }", ChangeSymbols{Modified: 1, Complete: true}},
		{"type and method", "", "type Public struct { Field int }; func (Public) Method() {}", ChangeSymbols{Added: 2, ExportedTouched: 2, Complete: true}},
		{"interface deleted", "type Public interface { Method() error }", "", ChangeSymbols{Deleted: 1, ExportedTouched: 1, Complete: true}},
		{"invalid parse", "", "func Broken(", ChangeSymbols{}},
		{"transitively reachable private type", "type hidden struct { inner }; type inner struct { Field int }; func Public() hidden { return hidden{} }", "type hidden struct { inner }; type inner struct { Field string }; func Public() hidden { return hidden{} }", ChangeSymbols{Modified: 1, ExportedTouched: 1, Complete: true}},
		{"unresolved private type", "type hidden struct { field int }", "type hidden struct { field string }", ChangeSymbols{Modified: 1}},
		{"var and const", "var Public = 1; const Constant = 1", "var Public = 2; const Constant = 2", ChangeSymbols{Modified: 2, ExportedTouched: 2, Complete: true}},
		{"pointer and value methods", "type Public struct{}; func (Public) Value() {}", "type Public struct{}; func (*Public) Pointer() {}", ChangeSymbols{Added: 1, Deleted: 1, ExportedTouched: 2, Complete: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := t.TempDir()
			prepareRepository(t, repo)
			writeFile(t, repo, "internal/demo/api.go", "package demo\n"+c.before+"\n")
			runGit(t, repo, "add", ".")
			runGit(t, repo, "commit", "-m", "chore: base API")
			writeFile(t, repo, "internal/demo/api.go", "package demo\n"+c.after+"\n")
			runGit(t, repo, "add", ".")
			runGit(t, repo, "commit", "-m", "feat: API")
			t.Chdir(repo)
			profile, err := ComputeChangeProfile("HEAD~1", "HEAD")
			if err != nil || profile.Symbols != c.want {
				t.Fatalf("Symbols=%#v err=%v, want %#v", profile.Symbols, err, c.want)
			}
		})
	}
}

func TestSymbolsFailClosedOutsideTheAnalyzer(t *testing.T) {
	content := map[string]string{
		"base:api.ts": "export const API = 1", "head:api.ts": "export const API = 2",
		"base:api.pb.go": "irrelevant", "head:api.pb.go": "also irrelevant",
	}
	git := func(args ...string) (string, error) { return content[args[1]], nil }
	classify := func(path string) string { return ClassifyByPath(path, DefaultRules()) }
	if got := changedSymbols(git, []pathChange{{"api.ts", "api.ts"}}, "base", "head", classify); got.Complete {
		t.Fatalf("TypeScript source declared complete: %#v", got)
	}
	if got := changedSymbols(git, []pathChange{{"api.pb.go", "api.pb.go"}}, "base", "head", classify); !got.Complete {
		t.Fatalf("generated must not require the analyzer: %#v", got)
	}
}

func TestSymbolsIncludePackageAndSignaturePurity(t *testing.T) {
	git := func(args ...string) (string, error) {
		if args[1] == "base:api.go" {
			return "package before\nfunc Public(a int) int { return a }", nil
		}
		return "package after\nfunc Public(a int) int { return a }", nil
	}
	got := changedSymbols(git, []pathChange{{"api.go", "api.go"}}, "base", "head", func(string) string { return ClassSource })
	if got.Modified != 1 || got.ExportedTouched != 1 || !got.Complete {
		t.Fatalf("package change not detected: %#v", got)
	}

	fset := token.NewFileSet()
	file, _ := parser.ParseFile(fset, "api.go", "package p\nfunc Public[T any](name T) T", 0)
	typeDecl := file.Decls[0].(*ast.FuncDecl).Type
	before := printAST(fset, typeDecl)
	first := printAPI(fset, typeDecl)
	if after := printAST(fset, typeDecl); after != before {
		t.Fatalf("printAPI mutated the AST: before=%q after=%q", before, after)
	}
	if second := printAPI(fset, typeDecl); second != first {
		t.Fatalf("signature depends on order: first=%q second=%q", first, second)
	}
}

func TestReachablePrivateTypesLongChain(t *testing.T) {
	const length = 2000
	var source strings.Builder
	source.WriteString("package p\nfunc Public() type0 { panic(0) }\n")
	for i := 0; i < length-1; i++ {
		source.WriteString("type type" + strconv.Itoa(i) + " type" + strconv.Itoa(i+1) + "\n")
	}
	source.WriteString("type type" + strconv.Itoa(length-1) + " int\n")
	file, err := parser.ParseFile(token.NewFileSet(), "api.go", source.String(), 0)
	if err != nil {
		t.Fatal(err)
	}
	reachable := reachablePrivateTypes(file)
	if len(reachable) != length {
		t.Fatalf("reachable types = %d, want %d", len(reachable), length)
	}
}

func TestRevisionTreeProtectsOptions(t *testing.T) {
	var got []string
	_, _ = revisionTree(func(args ...string) (string, error) { got = args; return "tree\n", nil }, "-malicious")
	want := []string{"rev-parse", "--verify", "--end-of-options", "-malicious^{tree}"}
	if !slices.Equal(got, want) {
		t.Fatalf("git args = %q, want %q", got, want)
	}
}

// TestComputeChangeProfileWithFakeGitReader proves the classification logic
// is injectable: a test double is enough, without invoking real git (T3.2
// review: before, exec.Command("git", ...) was coupled directly in the
// domain, with no intermediate port allowing this).
func TestComputeChangeProfileWithFakeGitReader(t *testing.T) {
	calls := 0
	fake := func(args ...string) (string, error) {
		calls++
		switch {
		case args[0] == "rev-parse":
			return args[1][:len(args[1])-7] + "-tree\n", nil
		case args[0] == "diff" && contains(args, "--name-only"):
			return "internal/new/new.go\x00", nil
		case args[0] == "diff" && contains(args, "--name-status"):
			return "M\x00internal/new/new.go\x00", nil
		case args[0] == "diff" && contains(args, "--numstat"):
			return "3\t0\tinternal/new/new.go\n", nil
		case args[0] == "diff":
			return "@@ -0,0 +1,3 @@\n", nil
		case args[0] == "log":
			return "feat: create capability\n", nil
		}
		return "", nil
	}

	profile, err := computeChangeProfileWith("HEAD~1", "HEAD", fake)
	if err != nil {
		t.Fatalf("computeChangeProfileWith returned an error: %v", err)
	}
	if profile.Kind != "feature" {
		t.Errorf("Kind = %q, want %q", profile.Kind, "feature")
	}
	if calls == 0 {
		t.Fatal("the test double was never invoked: the test proves nothing")
	}
}

func contains(args []string, wanted string) bool {
	for _, a := range args {
		if a == wanted {
			return true
		}
	}
	return false
}

type syntheticFile struct{ path, content string }

func prepareRepository(t *testing.T, repo string) {
	t.Helper()
	runGit(t, repo, "init")
	writeFile(t, repo, "internal/demo/demo.go", "package demo\nfunc Old() { println(\"same\") }\nfunc State() int { return 0 }\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "chore: base")
}

func writeFile(t *testing.T, repo, path, content string) {
	t.Helper()
	abs := filepath.Join(repo, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	args = append([]string{"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid"}, args...)
	if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
