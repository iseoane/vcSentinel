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

func preflightHealthyExecutor(head, porcelain, state string) ejecutorCodeGraph {
	respuestas := [][]byte{[]byte(head), []byte(porcelain), []byte(state)}
	return func(_ context.Context, _ string, _ []string, _ string, _ []string, _ string, _ int) ([]byte, error) {
		salida := respuestas[0]
		respuestas = respuestas[1:]
		return salida, nil
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
	llamadas := 0
	ejecutar := func(_ context.Context, _ string, _ []string, _ string, _ []string, _ string, _ int) ([]byte, error) {
		llamadas++
		return nil, errors.New("must not run")
	}
	conds := Preflight(root, func(name string) (string, error) {
		if name == "codegraph" {
			return "", errors.New("not found")
		}
		return name, nil
	}, ejecutar)
	if got := conditionByName(t, conds, "binary"); got.OK {
		t.Errorf("binary OK without a codegraph binary")
	}
	if llamadas != 0 {
		t.Errorf("executor ran %d times without a binary, want 0", llamadas)
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
	respuestas := [][]byte{nil, []byte(""), []byte(state)}
	llamadas := 0
	ejecutar := func(_ context.Context, _ string, args []string, _ string, _ []string, _ string, _ int) ([]byte, error) {
		llamadas++
		salida := respuestas[0]
		respuestas = respuestas[1:]
		if salida == nil {
			return nil, errors.New("rev-parse failed")
		}
		_ = args
		return salida, nil
	}
	conds := Preflight(root, lookupOK(root), ejecutar)
	if got := conditionByName(t, conds, "head"); got.OK {
		t.Errorf("head OK on rev-parse failure")
	}
	if got := conditionByName(t, conds, "worktree_clean"); !got.OK {
		t.Errorf("worktree_clean not OK when only rev-parse failed: %s", got.Detail)
	}
	if llamadas != 3 {
		t.Errorf("executor ran %d times, want 3 (rev-parse, porcelain, status)", llamadas)
	}
}
