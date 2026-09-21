package pr

import (
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/review"
)

func TestSecretFindingsFactoryReportsCredentialDiff(t *testing.T) {
	diff := "diff --git a/docs/runbook.md b/docs/runbook.md\n" +
		"new file mode 100644\n" +
		"--- /dev/null\n" +
		"+++ b/docs/runbook.md\n" +
		"@@ -0,0 +1,2 @@\n" +
		"+deploy:\n" +
		"+  token = \"github_pat_EXAMPLE1234567890abcdef\"\n"
	findings := SecretFindingsFactory()("abc123", []string{"docs/runbook.md"}, diff)
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	got := findings[0]
	if got.Source != review.SourceValidation || got.Dimension != "" ||
		got.Severity != review.SevWarning || got.Status != review.StatusPending ||
		got.Evidence != "" {
		t.Errorf("finding = %+v, want deterministic WARNING without dimension or evidence", got)
	}
	if !strings.Contains(got.Description, "docs/runbook.md") {
		t.Errorf("description = %q, want the path named", got.Description)
	}
}

func TestSecretFindingsFactoryCleanDiffYieldsNothing(t *testing.T) {
	diff := "diff --git a/app.txt b/app.txt\n" +
		"--- a/app.txt\n" +
		"+++ b/app.txt\n" +
		"@@ -1 +1,2 @@\n" +
		" context\n" +
		"+added prose without credentials\n"
	if findings := SecretFindingsFactory()("abc123", []string{"app.txt"}, diff); len(findings) != 0 {
		t.Fatalf("findings = %v, want none: absence is data, never a verdict", findings)
	}
}
