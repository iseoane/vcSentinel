package graph

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lookupOK(root string) func(string) (string, error) {
	return func(name string) (string, error) { return filepath.Join(root, name), nil }
}

func preflightHealthyExecutor(head, porcelain, state string) codeGraphRunner {
	responses := [][]byte{[]byte(head), []byte(porcelain), []byte(state)}
	return func(_ context.Context, _ string, _ []string, _ string, _ []string, _ string, _ int) ([]byte, error) {
		output := responses[0]
		responses = responses[1:]
		return output, nil
	}
}

func conditionByName(t *testing.T, conds []Condition, name string) Condition {
	t.Helper()
	for _, c := range conds {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("condition %q missing in %+v", name, conds)
	return Condition{}
}

func TestPreflightHealthyReportsEightOK(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".codegraph"), 0o755)
	state := strings.ReplaceAll(`{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`, "ROOT", filepath.ToSlash(root))
	conds := Preflight(root, lookupOK(root), preflightHealthyExecutor("abc123\n", "", state))
	want := []string{"binary", "index_dir", "head", "worktree_clean", "index_initialized", "project_path", "pending_changes", "worktree_match"}
	if len(conds) != len(want) {
		t.Fatalf("len(conditions) = %d, want %d (%+v)", len(conds), len(want), conds)
	}
	for i, name := range want {
		if conds[i].Name != name {
			t.Fatalf("conditions[%d].Name = %q, want %q", i, conds[i].Name, name)
		}
		if !conds[i].OK {
			t.Errorf("condition %q not OK: %s", name, conds[i].Detail)
		}
		if conds[i].Detail == "" {
			t.Errorf("condition %q has empty detail", name)
		}
	}
	if got := conditionByName(t, conds, "head").Detail; !strings.Contains(got, "abc123") {
		t.Errorf("head detail = %q, want the resolved SHA", got)
	}
}

func TestPreflightMissingBinarySkipsProbes(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".codegraph"), 0o755)
	calls := 0
	run := func(_ context.Context, _ string, _ []string, _ string, _ []string, _ string, _ int) ([]byte, error) {
		calls++
		return nil, errors.New("must not run")
	}
	conds := Preflight(root, func(name string) (string, error) {
		if name == "codegraph" {
			return "", errors.New("not found")
		}
		return name, nil
	}, run)
	if got := conditionByName(t, conds, "binary"); got.OK {
		t.Errorf("binary OK without a codegraph binary")
	}
	if calls != 0 {
		t.Errorf("executor ran %d times without a binary, want 0", calls)
	}
	for _, name := range []string{"head", "worktree_clean", "index_initialized", "project_path", "pending_changes", "worktree_match"} {
		if got := conditionByName(t, conds, name); got.OK {
			t.Errorf("condition %q OK without a codegraph binary", name)
		}
	}
}

func TestPreflightMissingIndexDirSkipsProbes(t *testing.T) {
	root := t.TempDir()
	conds := Preflight(root, lookupOK(root), preflightHealthyExecutor("abc123\n", "", "{}"))
	if got := conditionByName(t, conds, "index_dir"); got.OK {
		t.Errorf("index_dir OK without a .codegraph directory")
	}
	if got := conditionByName(t, conds, "pending_changes"); got.OK {
		t.Errorf("pending_changes OK without a .codegraph directory")
	}
}

func TestPreflightDirtyWorktreeCountsEntries(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".codegraph"), 0o755)
	state := strings.ReplaceAll(`{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`, "ROOT", filepath.ToSlash(root))
	conds := Preflight(root, lookupOK(root), preflightHealthyExecutor("abc123\n", " M a.go\n?? b.go\n", state))
	if got := conditionByName(t, conds, "worktree_clean"); got.OK {
		t.Errorf("worktree_clean OK with dirty entries")
	} else if !strings.Contains(got.Detail, "2") {
		t.Errorf("worktree_clean detail = %q, want the dirty-entry count", got.Detail)
	}
	if got := conditionByName(t, conds, "head"); !got.OK {
		t.Errorf("head not OK on a resolvable HEAD: %s", got.Detail)
	}
}

func TestPreflightIndexConditionsMirrorContextSkips(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".codegraph"), 0o755)
	slash := filepath.ToSlash(root)
	cases := []struct {
		name      string
		state     string
		failing   string
		detailHas string
	}{
		{"uninitialized", `{"initialized":false,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`, "index_initialized", "initialized"},
		{"project mismatch", `{"initialized":true,"projectPath":"/other/project","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`, "project_path", "/other/project"},
		{"pending changes", `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":1,"modified":0,"removed":0},"worktreeMismatch":null}`, "pending_changes", "1"},
		{"worktree mismatch", `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":{}}`, "worktree_match", "mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := strings.ReplaceAll(tc.state, "ROOT", slash)
			conds := Preflight(root, lookupOK(root), preflightHealthyExecutor("abc123\n", "", state))
			if got := conditionByName(t, conds, tc.failing); got.OK {
				t.Errorf("condition %q OK on %s", tc.failing, tc.name)
			} else if !strings.Contains(strings.ToLower(got.Detail), strings.ToLower(tc.detailHas)) {
				t.Errorf("condition %q detail = %q, want %q", tc.failing, got.Detail, tc.detailHas)
			}
		})
	}
}

func TestPreflightHeadFailureStillProbesWorktree(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".codegraph"), 0o755)
	slash := filepath.ToSlash(root)
	state := strings.ReplaceAll(`{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`, "ROOT", slash)
	responses := [][]byte{nil, []byte(""), []byte(state)}
	calls := 0
	run := func(_ context.Context, _ string, args []string, _ string, _ []string, _ string, _ int) ([]byte, error) {
		calls++
		output := responses[0]
		responses = responses[1:]
		if output == nil {
			return nil, errors.New("rev-parse failed")
		}
		_ = args
		return output, nil
	}
	conds := Preflight(root, lookupOK(root), run)
	if got := conditionByName(t, conds, "head"); got.OK {
		t.Errorf("head OK on rev-parse failure")
	}
	if got := conditionByName(t, conds, "worktree_clean"); !got.OK {
		t.Errorf("worktree_clean not OK when only rev-parse failed: %s", got.Detail)
	}
	if calls != 3 {
		t.Errorf("executor ran %d times, want 3 (rev-parse, porcelain, status)", calls)
	}
}
func TestPreflightBinaryStaysOKWhenIndexMissing(t *testing.T) {
	root := t.TempDir()
	conds := Preflight(root, lookupOK(root), preflightHealthyExecutor("abc123\n", "", "{}"))
	if got := conditionByName(t, conds, "binary"); !got.OK {
		t.Errorf("binary not OK while the binary resolves: %s", got.Detail)
	}
	if got := conditionByName(t, conds, "index_dir"); got.OK {
		t.Errorf("index_dir OK without a .codegraph directory")
	}
}

func TestPreflightHeadAndWorktreeStayOKWhenStatusFails(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".codegraph"), 0o755)
	responses := [][]byte{[]byte("abc123\n"), []byte(""), nil}
	run := func(_ context.Context, _ string, _ []string, _ string, _ []string, _ string, _ int) ([]byte, error) {
		output := responses[0]
		responses = responses[1:]
		if output == nil {
			return nil, errors.New("codegraph status failed")
		}
		return output, nil
	}
	conds := Preflight(root, lookupOK(root), run)
	for _, name := range []string{"binary", "index_dir", "head", "worktree_clean"} {
		if got := conditionByName(t, conds, name); !got.OK {
			t.Errorf("condition %q not OK when only codegraph status failed: %s", name, got.Detail)
		}
	}
	for _, name := range []string{"index_initialized", "project_path", "pending_changes", "worktree_match"} {
		if got := conditionByName(t, conds, name); got.OK {
			t.Errorf("condition %q OK without a codegraph status answer", name)
		}
	}
}
func TestPreflightStatusUnavailableMarksFourUnknown(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".codegraph"), 0o755)
	responses := [][]byte{[]byte("abc123\n"), []byte(""), nil}
	run := func(_ context.Context, _ string, _ []string, _ string, _ []string, _ string, _ int) ([]byte, error) {
		output := responses[0]
		responses = responses[1:]
		if output == nil {
			return nil, errors.New("exit status 127")
		}
		return output, nil
	}
	conds := Preflight(root, lookupOK(root), run)
	for _, name := range []string{"index_initialized", "project_path", "pending_changes", "worktree_match"} {
		got := conditionByName(t, conds, name)
		if got.OK {
			t.Errorf("condition %q OK without a status answer", name)
		}
		if !got.Unknown {
			t.Errorf("condition %q is failed, want unknown: the prober never ran", name)
		}
		if !strings.Contains(got.Detail, "unavailable") {
			t.Errorf("condition %q detail = %q, want the unavailable cause", name, got.Detail)
		}
	}
}

func TestPreflightStatusUnparsableMarksFourUnknownDistinct(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".codegraph"), 0o755)
	conds := Preflight(root, lookupOK(root), preflightHealthyExecutor("abc123\n", "", "not json"))
	for _, name := range []string{"index_initialized", "project_path", "pending_changes", "worktree_match"} {
		got := conditionByName(t, conds, name)
		if got.OK || !got.Unknown {
			t.Errorf("condition %q = %+v, want unknown", name, got)
		}
		if !strings.Contains(got.Detail, "unparsable") {
			t.Errorf("condition %q detail = %q, want it distinct from unavailable", name, got.Detail)
		}
	}
}

func TestPreflightSkippedRowsAreUnknown(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".codegraph"), 0o755)
	conds := Preflight(root, func(name string) (string, error) {
		if name == "codegraph" {
			return "", errors.New("not found")
		}
		return name, nil
	}, preflightHealthyExecutor("abc123\n", "", "{}"))
	for _, name := range []string{"head", "worktree_clean", "index_initialized", "project_path", "pending_changes", "worktree_match"} {
		if got := conditionByName(t, conds, name); !got.Unknown {
			t.Errorf("condition %q is failed, want unknown: skipped by a missing prerequisite", name)
		}
	}
}
func TestPreflightCountsPorcelainVariants(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".codegraph"), 0o755)
	slash := filepath.ToSlash(root)
	healthy := func(porcelain string) string {
		state := strings.ReplaceAll(`{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`, "ROOT", slash)
		conds := Preflight(root, lookupOK(root), preflightHealthyExecutor("abc123\n", porcelain, state))
		return conditionByName(t, conds, "worktree_clean").Detail
	}
	cases := []struct {
		name      string
		porcelain string
		want      string
	}{
		{"quoted names and dirs count once each", " M tracked.txt\n?? \"sp ace.txt\"\n?? sub/\n", "3 dirty entries"},
		{"rename counts once", "R  old.go -> new.go\n", "1 dirty entries"},
		{"missing trailing newline still counts", " M a.go", "1 dirty entries"},
		{"empty stays clean", "", "worktree clean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthy(tc.porcelain); !strings.Contains(got, tc.want) {
				t.Errorf("detail = %q, want %q", got, tc.want)
			}
		})
	}
}
