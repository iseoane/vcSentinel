package graph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

func TestDetectCodeGraphRequiresIndexAndCLI(t *testing.T) {
	root := t.TempDir()
	if detectCodeGraphProvider(root, func(string) (string, error) { return "codegraph", nil }, nil) != nil {
		t.Fatal("provider present without .codegraph")
	}
	_ = os.Mkdir(filepath.Join(root, ".codegraph"), 0o755)
	if detectCodeGraphProvider(root, func(string) (string, error) { return "", errors.New("no CLI") }, nil) != nil {
		t.Fatal("provider present without CLI")
	}
	if detectCodeGraphProvider(root, func(name string) (string, error) { return filepath.Join(root, name), nil }, nil) == nil {
		t.Fatal("provider missing with index and binaries")
	}
}

func TestCodeGraphProviderDegradesOnUnboundState(t *testing.T) {
	if _, authorizes := any(&CodeGraphProvider{}).(GraphProvider); authorizes {
		t.Fatal("CodeGraphProvider satisfies GraphProvider")
	}
	cases := []string{
		`{"initialized":false,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`,
		`{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":1,"modified":0,"removed":0},"worktreeMismatch":null}`,
		`{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":{}}`,
		`{"initialized":true,"projectPath":"/other/project","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`,
	}
	for _, state := range cases {
		p, _ := providerWithAnswers(t, state)
		refs, err := p.Context("head", []string{"a.go"})
		if err == nil || len(refs) != 0 {
			t.Fatalf("unsafe state produced context: (%v, %v)", refs, err)
		}
	}
}

func TestCodeGraphProviderSkipsStaleSHA(t *testing.T) {
	p, fake := providerWithAnswers(t, `{}`)
	refs, err := p.Context("older", []string{"a.go"})
	if err == nil || len(refs) != 0 || len(fake.calls) != 1 {
		t.Fatalf("stale SHA not skipped: refs=%v err=%v calls=%d", refs, err, len(fake.calls))
	}
}

func TestCodeGraphProviderExposesSkipReasons(t *testing.T) {
	const cleanState = `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`
	cases := []struct {
		name   string
		want   string
		mutate func(*fakeCG)
	}{
		{
			name: "audited HEAD does not match current HEAD",
			want: "head_mismatch",
			mutate: func(fake *fakeCG) {
				fake.answers[0] = []byte("other\n")
			},
		},
		{
			name: "worktree is dirty",
			want: "dirty_worktree",
			mutate: func(fake *fakeCG) {
				fake.answers[1] = []byte(" M internal/file.go\n")
			},
		},
		{
			name: "index is uninitialized",
			want: "uninitialized_index",
			mutate: func(fake *fakeCG) {
				fake.answers[2] = []byte(`{"initialized":false,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`)
			},
		},
		{
			name: "CodeGraph project path does not match",
			want: "project_path_mismatch",
			mutate: func(fake *fakeCG) {
				fake.answers[2] = []byte(`{"initialized":true,"projectPath":"/other/project","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`)
			},
		},
		{
			name: "CodeGraph has pending changes",
			want: "pending_changes",
			mutate: func(fake *fakeCG) {
				fake.answers[2] = []byte(replaceRoot(`{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":1,"modified":0,"removed":0},"worktreeMismatch":null}`, fake.root))
			},
		},
		{
			name: "CodeGraph worktree mismatches",
			want: "worktree_mismatch",
			mutate: func(fake *fakeCG) {
				fake.answers[2] = []byte(replaceRoot(`{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":{}}`, fake.root))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, fake := providerWithAnswers(t, cleanState)
			tc.mutate(fake)
			refs, err := provider.Context("head", []string{"a.go"})
			if len(refs) != 0 {
				t.Fatalf("refs = %#v, want no context on a skipped provider", refs)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want observable reason %q", err, tc.want)
			}
		})
	}
}

func TestCodeGraphProviderAffectedStructuredAndBounded(t *testing.T) {
	p, fake := providerWithAnswers(t, `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`)
	for _, path := range []string{"a_test.go", "z_test.go"} {
		if err := os.WriteFile(filepath.Join(p.root, path), []byte("package x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fake.answers = append(fake.answers, []byte(`{"changedFiles":["a.go"],"affectedTests":["z_test.go","../escape_test.go","..\\escape_test.go","C:\\escape_test.go","a_test.go","a_test.go","-x_test.go"],"totalDependentsTraversed":2}`))
	refs, err := p.Context("head", []string{"a.go"})
	want := []review.Reference{{Path: "a_test.go", Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph}, {Path: "z_test.go", Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph}}
	if err != nil || !reflect.DeepEqual(refs, want) {
		t.Fatalf("references = (%v, %v), want %v", refs, err, want)
	}
	if !reflect.DeepEqual(fake.calls[3].args, []string{"affected", "-p", p.root, "--stdin", "--json"}) || fake.calls[3].stdin != "a.go\n" || fake.calls[3].dir != p.root || len(fake.calls[3].env) == 0 {
		t.Fatalf("invalid affected: %+v", fake.calls[3])
	}
	wantArgs := [][]string{{"rev-parse", "--verify", "HEAD^{commit}"}, {"status", "--porcelain"}, {"status", "--json", p.root}}
	for i, args := range wantArgs {
		if !reflect.DeepEqual(fake.calls[i].args, args) || fake.calls[i].dir != p.root || fake.calls[i].stdin != "" || len(fake.calls[i].env) == 0 {
			t.Fatalf("invalid call %d: %+v", i, fake.calls[i])
		}
	}
}

func TestCodeGraphProviderContextWidenedRelations(t *testing.T) {
	p, fake := providerWithAnswers(t, `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`)
	// Materialize every path the widened relations may name, so validation
	// keeps them; "../escape.go" is dropped by sanitization and "ghost.go"
	// by resolution (it does not exist inside the root).
	for _, path := range []string{"a_test.go", "service.go", "help.go", "impact.go"} {
		if err := os.WriteFile(filepath.Join(p.root, path), []byte("package x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fake.answers = append(fake.answers,
		[]byte(`{"changedFiles":["service.go"],"affectedTests":["a_test.go"],"totalDependentsTraversed":1}`),
		// Audited diff restricted to the input paths: one added function and
		// one added type share the name "Service", collapsing to one symbol.
		[]byte("diff --git a/service.go b/service.go\nindex 0000000..1111111 100644\n--- a/service.go\n+++ b/service.go\n@@ -0,0 +1,2 @@\n+func Service() {}\n+type Service struct {}\n"),
		[]byte(`{"symbol":"Service","callers":[{"name":"Main","kind":"function","filePath":"service.go","startLine":10},{"name":"Escape","kind":"function","filePath":"../escape.go","startLine":1},{"name":"Absent","kind":"function","filePath":"ghost.go","startLine":2}]}`),
		[]byte(`{"symbol":"Service","callees":[{"name":"help","kind":"function","filePath":"help.go","startLine":3}]}`),
		[]byte(`{"symbol":"Service","depth":2,"nodeCount":3,"edgeCount":2,"affected":[{"name":"Main","kind":"function","filePath":"impact.go","startLine":42}]}`),
	)
	refs, err := p.Context("head", []string{"service.go"})
	want := []review.Reference{
		{Path: "a_test.go", Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph},
		{Path: "service.go", Relation: review.RelationCaller, Reason: review.ReasonCodeGraph},
		{Path: "help.go", Relation: review.RelationCallee, Reason: review.ReasonCodeGraph},
		{Path: "impact.go", Relation: review.RelationImpact, Reason: review.ReasonCodeGraph},
	}
	if err != nil || !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = (%v, %v), want %v", refs, err, want)
	}
	// Pin the additive subprocess shapes: the audited diff restricted to the
	// validated input paths, then one query per relation per derived symbol.
	wantArgs := [][]string{
		{"show", "head", "--format=", "--", "service.go"},
		{"callers", "-p", p.root, "-l", "16", "--json", "Service"},
		{"callees", "-p", p.root, "-l", "16", "--json", "Service"},
		{"impact", "-p", p.root, "-d", "2", "--json", "Service"},
	}
	for i, args := range wantArgs {
		if !reflect.DeepEqual(fake.calls[i+4].args, args) {
			t.Fatalf("call %d args = %v, want %v", i+4, fake.calls[i+4].args, args)
		}
	}
}

// TestCodeGraphProviderAffectedTestsFullBudget pins the affectedTests
// budget: every deterministically-validated candidate is returned. The
// fixture (33 candidates) and the expectation (exactly 32 refs) are literal,
// so a regression of maxCodeGraphRefs fails here instead of silently
// reshaping both sides of the assertion.
func TestCodeGraphProviderAffectedTestsFullBudget(t *testing.T) {
	p, fake := providerWithAnswers(t, `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`)
	candidates := make([]string, 0, 33)
	for i := range 33 {
		candidates = append(candidates, fmt.Sprintf("t%02d_test.go", i))
	}
	for _, file := range candidates {
		if err := os.WriteFile(filepath.Join(p.root, file), []byte("package x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var b strings.Builder
	b.WriteString(`{"affectedTests":[`)
	for i, file := range candidates {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%q", file)
	}
	b.WriteString(`]}`)
	fake.answers = append(fake.answers, []byte(b.String()))
	refs, err := p.Context("head", []string{"a.go"})
	want := make([]review.Reference, 0, 32)
	for i := range 32 {
		want = append(want, review.Reference{Path: fmt.Sprintf("t%02d_test.go", i), Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph})
	}
	if err != nil || !reflect.DeepEqual(refs, want) {
		t.Fatalf("affected refs = %d items (err %v), want the full %d-item budget", len(refs), err, len(want))
	}
}

// TestCodeGraphProviderDegradesWhenQueriesFail guards the additive
// contract: a failing git show, or one relation query erroring, answering
// empty, or answering malformed JSON, never errors and never regresses the
// affectedTests result.
func TestCodeGraphProviderDegradesWhenQueriesFail(t *testing.T) {
	state := `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`
	affected := []byte(`{"changedFiles":["a.go"],"affectedTests":["a_test.go"],"totalDependentsTraversed":1}`)
	want := []review.Reference{{Path: "a_test.go", Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph}}

	t.Run("git show fails", func(t *testing.T) {
		p, fake := providerWithAnswers(t, state)
		if err := os.WriteFile(filepath.Join(p.root, "a_test.go"), []byte("package x"), 0o600); err != nil {
			t.Fatal(err)
		}
		fake.answers = append(fake.answers, affected)
		// Call 4 is the `git show` feeding symbol derivation.
		fake.failures = map[int]error{4: errors.New("git show failed")}
		refs, err := p.Context("head", []string{"a.go"})
		if err != nil || !reflect.DeepEqual(refs, want) {
			t.Fatalf("refs = (%v, %v), want %v", refs, err, want)
		}
	})

	t.Run("relation responses degrade", func(t *testing.T) {
		p, fake := providerWithAnswers(t, state)
		if err := os.WriteFile(filepath.Join(p.root, "a_test.go"), []byte("package x"), 0o600); err != nil {
			t.Fatal(err)
		}
		diff := []byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -0,0 +1 @@\n+func Service() {}\n")
		fake.answers = append(fake.answers, affected, diff, []byte{}, []byte("{malformed"))
		// Calls 5-7 are the callers/callees/impact queries: callers fails,
		// callees answers empty, impact answers malformed JSON.
		fake.failures = map[int]error{5: errors.New("callers failed")}
		refs, err := p.Context("head", []string{"a.go"})
		if err != nil || !reflect.DeepEqual(refs, want) {
			t.Fatalf("refs = (%v, %v), want %v", refs, err, want)
		}
		if len(fake.calls) != 8 {
			t.Fatalf("calls = %d, want 8: four gate calls, git show, three relation queries", len(fake.calls))
		}
		for i, wantArgs := range [][]string{
			{"callers", "-p", p.root, "-l", "16", "--json", "Service"},
			{"callees", "-p", p.root, "-l", "16", "--json", "Service"},
			{"impact", "-p", p.root, "-d", "2", "--json", "Service"},
		} {
			if !reflect.DeepEqual(fake.calls[i+5].args, wantArgs) {
				t.Fatalf("call %d args = %v, want %v", i+5, fake.calls[i+5].args, wantArgs)
			}
		}
	})
}

// TestCodeGraphProviderAdditiveRelationTruncated pins the per-relation
// budget: one relation's candidates are validated first — invalid graph
// paths cannot starve the budget — then capped at 8 sorted, deduplicated
// paths. The cap in the expectation is the literal 8, so a regression of
// perRelationBudget fails here instead of reshaping both sides.
func TestCodeGraphProviderAdditiveRelationTruncated(t *testing.T) {
	p, fake := providerWithAnswers(t, `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`)
	if err := os.WriteFile(filepath.Join(p.root, "a_test.go"), []byte("package x"), 0o600); err != nil {
		t.Fatal(err)
	}
	validPaths := make([]string, 0, 10)
	for i := 1; i <= 10; i++ {
		validPaths = append(validPaths, fmt.Sprintf("c%02d.go", i))
	}
	for _, file := range validPaths {
		if err := os.WriteFile(filepath.Join(p.root, file), []byte("package x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The graph answers 13 caller entries: one path escaping the root, one
	// nonexistent, one duplicate, ten valid files — more than the
	// per-relation budget of 8.
	entries := append([]string{"../escape.go", "a_ghost.go", "c01.go", "c01.go"}, validPaths[1:]...)
	var b strings.Builder
	b.WriteString(`{"symbol":"Service","callers":[`)
	for i, file := range entries {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":"N%02d","kind":"function","filePath":%q,"startLine":%d}`, i, file, i)
	}
	b.WriteString(`]}`)
	fake.answers = append(fake.answers,
		[]byte(`{"changedFiles":["a.go"],"affectedTests":["a_test.go"],"totalDependentsTraversed":1}`),
		[]byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -0,0 +1 @@\n+func Service() {}\n"),
		[]byte(b.String()),
	)
	refs, err := p.Context("head", []string{"a.go"})
	want := []review.Reference{{Path: "a_test.go", Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph}}
	for i := 1; i <= 8; i++ {
		want = append(want, review.Reference{Path: fmt.Sprintf("c%02d.go", i), Relation: review.RelationCaller, Reason: review.ReasonCodeGraph})
	}
	if err != nil || !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %d items (err %v), want %d: %v", len(refs), err, len(want), want)
	}
}

// TestCodeGraphProviderTotalBoundOverfill fills every budget at once: a
// full affectedTests budget of 32 plus more valid candidates than the
// per-relation cap on all three additive relations. The result must land
// exactly on maxTotalRefs — the total the two-tier budget promises — with
// each relation contributing its capped 8.
func TestCodeGraphProviderTotalBoundOverfill(t *testing.T) {
	p, fake := providerWithAnswers(t, `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`)
	tests := make([]string, 0, 32)
	for i := range 32 {
		tests = append(tests, fmt.Sprintf("t%02d_test.go", i))
	}
	valid := make([]string, 0, 10)
	for i := 1; i <= 10; i++ {
		valid = append(valid, fmt.Sprintf("v%02d.go", i))
	}
	for _, file := range append(append([]string(nil), tests...), valid...) {
		if err := os.WriteFile(filepath.Join(p.root, file), []byte("package x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var b strings.Builder
	b.WriteString(`{"affectedTests":[`)
	for i, file := range tests {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%q", file)
	}
	b.WriteString(`]}`)
	fake.answers = append(fake.answers, []byte(b.String()),
		[]byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -0,0 +1 @@\n+func Service() {}\n"),
	)
	// One overfull response per additive relation; the impact response
	// reports its entries under the "affected" key.
	for _, key := range []string{"callers", "callees", "affected"} {
		var rb strings.Builder
		fmt.Fprintf(&rb, `{"symbol":"Service","%s":[`, key)
		for i, file := range valid {
			if i > 0 {
				rb.WriteString(",")
			}
			fmt.Fprintf(&rb, `{"name":"N%02d","kind":"function","filePath":%q,"startLine":%d}`, i, file, i)
		}
		rb.WriteString(`]}`)
		fake.answers = append(fake.answers, []byte(rb.String()))
	}
	refs, err := p.Context("head", []string{"a.go"})
	want := make([]review.Reference, 0, maxTotalRefs)
	for i := range 32 {
		want = append(want, review.Reference{Path: fmt.Sprintf("t%02d_test.go", i), Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph})
	}
	for _, relation := range additiveRelations {
		for i := 1; i <= 8; i++ {
			want = append(want, review.Reference{Path: fmt.Sprintf("v%02d.go", i), Relation: relation, Reason: review.ReasonCodeGraph})
		}
	}
	if err != nil || !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = (%v, %v), want exactly %d: 32 affectedTests plus the capped 8 of each additive relation", refs, err, maxTotalRefs)
	}
}

// TestRelationWireValuesPinned pins the wire values of the additive
// relations: they reach the emitted reference payload consumed by
// downstream reviewers, so renaming the Go constants must not silently
// change what lands there.
func TestRelationWireValuesPinned(t *testing.T) {
	if review.RelationCaller != "caller" || review.RelationCallee != "callee" || review.RelationImpact != "impact" {
		t.Fatalf("additive relation wire values changed: caller=%q callee=%q impact=%q, want \"caller\", \"callee\", \"impact\"", review.RelationCaller, review.RelationCallee, review.RelationImpact)
	}
}

type callCG struct {
	binary     string
	args       []string
	dir, stdin string
	env        []string
}
type fakeCG struct {
	root    string
	answers [][]byte
	// failures keys injected subprocess failures by absolute call index
	// (0 = rev-parse); a failed call consumes no scripted response.
	failures map[int]error
	calls    []callCG
}

func providerWithAnswers(t *testing.T, state string) (*CodeGraphProvider, *fakeCG) {
	t.Helper()
	root := t.TempDir()
	state = string([]byte(state))
	state = replaceRoot(state, root)
	p := &CodeGraphProvider{root: root, binary: "codegraph", git: "git", limit: 4096}
	fake := &fakeCG{root: root, answers: [][]byte{[]byte("head\n"), nil, []byte(state)}}
	p.run = func(_ context.Context, binary string, args []string, dir string, env []string, stdin string, _ int) ([]byte, error) {
		fake.calls = append(fake.calls, callCG{binary, append([]string(nil), args...), dir, stdin, append([]string(nil), env...)})
		if err, ok := fake.failures[len(fake.calls)-1]; ok {
			return nil, err
		}
		if len(fake.answers) == 0 {
			return nil, errors.New("unexpected command")
		}
		output := fake.answers[0]
		fake.answers = fake.answers[1:]
		return output, nil
	}
	return p, fake
}

func replaceRoot(s, root string) string {
	b := []byte(s)
	return string(bytes.ReplaceAll(b, []byte("ROOT"), []byte(filepath.ToSlash(root))))
}

func TestInterpreterDirReadsOnlyThePrefix(t *testing.T) {
	dir := t.TempDir()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	shebang := "#!" + sh + "\n"
	large := append([]byte(shebang), bytes.Repeat([]byte{0}, 4<<20)...)
	write := func(name string, content []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, content, 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	cases := map[string]struct {
		path string
		want string
	}{
		"direct shebang":       {write("a", []byte(shebang)), filepath.Dir(sh)},
		"shebang after binary": {write("b", large), filepath.Dir(sh)},
		"no shebang":           {write("c", []byte("\x7fELF....")), ""},
		"env with flags":       {write("d", []byte("#!/usr/bin/env -S node --flag\n")), filepath.Dir(node)},
		"missing":              {filepath.Join(dir, "missing"), ""},
		"directory":            {dir, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := interpreterDir(tc.path); got != tc.want {
				t.Errorf("interpreterDir = %q, want %q", got, tc.want)
			}
		})
	}
}
