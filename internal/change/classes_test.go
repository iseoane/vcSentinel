package change

import "testing"

// TestClassifyByPath covers the real false positives of
// internal/git.ClassifyLayer (T3.1): that function classifies "test" because
// the path contains the substring "test", without respecting directory or
// file-name boundaries.
func TestClassifyByPath(t *testing.T) {
	rules := DefaultRules()
	cases := []struct {
		name string
		path string
		want string
	}{
		{"'latest' directory is not test by substring", "latest/x.go", ClassSource},
		{"contest.go is not test by substring", "contest.go", ClassSource},
		{"legitimate _test.go is test", "internal/setup/install_test.go", ClassTest},
		{"protobuf generated", "foo.pb.go", ClassGenerated},
		{"go.sum generated", "go.sum", ClassGenerated},
		{"Dockerfile is infra", "Dockerfile", ClassInfra},
		{"github workflow is ci", ".github/workflows/build.yml", ClassCI},
		{"markdown in docs/ is docs", "docs/x.md", ClassDocs},
		{"loose README is docs", "README.md", ClassDocs},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyByPath(c.path, rules); got != c.want {
				t.Errorf("ClassifyByPath(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

// TestPrecedenceByDeclarationOrder verifies the explicit contract of
// ClassifyByPath: the first declared rule that matches wins, not the most
// specific one nor the last (deterministic and explicable only by order).
func TestPrecedenceByDeclarationOrder(t *testing.T) {
	specific := Rule{Class: ClassInfra, Patterns: []string{"vendor/**/*.md"}}
	generic := Rule{Class: ClassDocs, Patterns: []string{"**/*.md"}}
	path := "vendor/pkg/README.md"

	if got := ClassifyByPath(path, []Rule{specific, generic}); got != ClassInfra {
		t.Errorf("with the specific one declared first, want %q, got %q", ClassInfra, got)
	}
	if got := ClassifyByPath(path, []Rule{generic, specific}); got != ClassDocs {
		t.Errorf("with the generic one declared first, want %q, got %q", ClassDocs, got)
	}
}

// TestClassify covers the additional .gitattributes signal (section 8 of the
// architecture document, linguist-generated): a pattern without "/" must
// match at any depth (convention inherited from .gitignore), and the mark
// always wins over a glob rule that would classify differently.
func TestClassify(t *testing.T) {
	rules := DefaultRules()
	content := "*.pb.go linguist-generated=true\n*.md text\n"

	if got := Classify("api/foo.pb.go", rules, content); got != ClassGenerated {
		t.Errorf("foo.pb.go with linguist-generated, want %q, got %q", ClassGenerated, got)
	}
	if got := Classify("README.md", rules, content); got != ClassDocs {
		t.Errorf("README.md without linguist-generated, want %q (by glob), got %q", ClassDocs, got)
	}
}
