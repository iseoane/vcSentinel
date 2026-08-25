package review

import (
	"slices"
	"strings"
	"testing"
)

// historyStub answers CRITICAL for prompts carrying commit 2's "+defect" diff line, ok otherwise.
type historyStub struct {
	auditorStub
	prompts []string
}

func (s *historyStub) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	if strings.Contains(prompt, "+defect\n") && !strings.Contains(prompt, "Pull request intention:") {
		return auditOutputCritical, nil
	}
	return s.auditSalida, nil
}
func addDefectCommits(t *testing.T, fix bool) (shaDefect string) {
	gitEjecutar(t, "commit", "--allow-empty", "-qm", "feat: step 1")
	shaDefect = commitEnRama(t, "x.go", "defect\nkept\n")
	gitEjecutar(t, "commit", "--allow-empty", "-qm", "feat: step 3")
	gitEjecutar(t, "commit", "--allow-empty", "-qm", "feat: step 4")
	if fix {
		commitEnRama(t, "x.go", "kept\nfixed\n") // commit 5 removes the defect
	} else {
		gitEjecutar(t, "commit", "--allow-empty", "-qm", "feat: unrelated step 5")
	}
	return shaDefect
}
func TestNetReviewIndependentOfHistoricalFindings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fix       bool
		promptHas []string
	}{
		{"archived-obsolete", true, []string{"+fixed", "ARCHIVED", "injected defect", "Add x module", "lint-ok", "NET EVIDENCE"}},
		{"active-context", false, []string{"ACTIVE", "+defect"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gitDir := prepararRepoRama(t)
			shaDefect := addDefectCommits(t, tc.fix)
			stub := &historyStub{auditorStub: auditorStub{auditSalida: salidaAuditOK}}
			res, err := AnalizarRama(NuevoLedger(gitDir), OpcionesRama{Fabrica: fabricaStub(stub), Parallel: 1, NetReview: &NetReviewOptions{Intention: "Add x module", Validation: "lint-ok"}})
			if err != nil || res.Net == nil || res.Net.From != strings.TrimSpace(gitSalida(t, "merge-base", "main", "HEAD")) {
				t.Fatalf("net result/range wrong: %v %+v", err, res.Net)
			}
			prompt := stub.prompts[len(stub.prompts)-1]
			for _, want := range append(tc.promptHas, "- Intention:", "- Integration:", "- Interaction between commits:", "- Net regression:", "- Contracts:", "- Coverage:", "Permitted paths:", "- x.go") {
				if !strings.Contains(prompt, want) {
					t.Errorf("prompt missing %q:\n%s", want, prompt)
				}
			}
			idx := slices.IndexFunc(res.Net.Context, func(f HistoricalFinding) bool { return f.SHA == shaDefect })
			if idx < 0 || res.Net.Context[idx].Severity != SevCritical || (res.Net.Context[idx].ArchiveReason != "") != tc.fix {
				t.Fatalf("commit-2 context = %+v, want CRITICAL archived=%v", res.Net.Context, tc.fix)
			}
			if res.Net.Audit.Veredicto != VerdictOK || len(res.Net.Audit.Findings) != 0 {
				t.Fatalf("net verdict/findings = %s/%d, want ok/0", res.Net.Audit.Veredicto, len(res.Net.Audit.Findings))
			}
			if !tc.fix {
				return
			}
			record, err := NuevoLedger(gitDir).LeerFicha(shaDefect)
			if err != nil || record == nil || len(record.Revisions) != 1 || len(record.Revisions[0].HallazgosEfectivos()) == 0 {
				t.Fatalf("historical ledger record was mutated: %+v (%v)", record, err)
			}
		})
	}
}
func TestStackedNetReviewUsesOwnRange(t *testing.T) {
	stack := prepareStackRepo(t)
	swapParentResolver(t, fixedResolver("feature-a"))
	opts := OpcionesRama{Fabrica: fabricaStub(&auditorStub{auditSalida: salidaAuditOK}), Parallel: 1, OwnDiff: &OwnDiffOptions{ResolveParent: true}, NetReview: &NetReviewOptions{}}
	res, err := AnalizarRama(NuevoLedger(stack.gitDir), opts)
	if err != nil || res.Net == nil || res.Net.From != stack.shaA {
		t.Errorf("net start = %v/%v, want own-diff start %s (not base %s)", err, res.Net, stack.shaA, stack.baseSHA)
	}
}
