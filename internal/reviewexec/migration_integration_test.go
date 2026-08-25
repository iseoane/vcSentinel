package reviewexec

// Integration tests for ticket 05 slice 4: migration parity between the
// legacy scheduler and the controller-backed transport, parallelism parity,
// and effective-agent attribution across the durable path. They live on the
// wiring-target side because internal/store already imports internal/review,
// so review-side tests could not open a store without an import cycle.

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func migrationBundles() []review.ReviewBundle {
	return []review.ReviewBundle{
		{Name: "quality", Dimensions: []string{"logic", "style"}, Priority: review.PriorityRequired, Cost: 1},
		{Name: "security", Dimensions: []string{"security"}, Priority: review.PriorityOptional, Cost: 5},
	}
}

// durableTestTransport mirrors the production cmd/sentinel wiring shape: real
// store, policy, SHA binding, restricted-capability assertion.
func durableTestTransport(t *testing.T, sha string) review.ReviewTransport {
	t.Helper()
	backing := store.NuevoStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:migration"}, sha, []string{"a.go"})
	return func(bundleName, dimension, prompt string, agent review.AuditorAgente) (string, string, error) {
		restricted, ok := agent.(RestrictedReviewer)
		if !ok {
			return "", "", review.ErrRestrictedRequired
		}
		output, evidence, err := transport.Run(restricted, bundleName+"/"+dimension, prompt)
		if err != nil {
			return "", "", err
		}
		return output, evidence.InvocationID, nil
	}
}

// lenientTestTransport is the same wiring with review.evidence_admission=false:
// construction-time rollback to the pre-R6 observe-but-admit behavior.
func lenientTestTransport(t *testing.T, sha string) review.ReviewTransport {
	t.Helper()
	backing := store.NuevoStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:migration-lenient"}, sha, []string{"a.go"},
		WithEvidenceAdmission(false))
	return func(bundleName, dimension, prompt string, agent review.AuditorAgente) (string, string, error) {
		restricted, ok := agent.(RestrictedReviewer)
		if !ok {
			return "", "", review.ErrRestrictedRequired
		}
		output, evidence, err := transport.Run(restricted, bundleName+"/"+dimension, prompt)
		if err != nil {
			return "", "", err
		}
		return output, evidence.InvocationID, nil
	}
}

// cannedAgent returns one deterministic verdict per call.
type cannedAgent struct {
	responses []string
	calls     int32
}

func (a *cannedAgent) EjecutarPrompt(prompt string) (string, error) {
	index := int(atomic.AddInt32(&a.calls, 1)) - 1
	if index < len(a.responses) {
		return a.responses[index], nil
	}
	return fmt.Sprintf(`{"dim":%q,"verdict":"ok"}`, migrationDimensionFromPrompt(prompt)), nil
}

func migrationDimensionFromPrompt(prompt string) string {
	const prefix = `against the "`
	start := strings.Index(prompt, prefix)
	if start < 0 {
		return "logic"
	}
	rest := prompt[start+len(prefix):]
	end := strings.Index(rest, `" dimension`)
	if end < 0 {
		return "logic"
	}
	return rest[:end]
}

func (a *cannedAgent) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	return a.EjecutarPrompt(prompt)
}

func (a *cannedAgent) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.EjecutarRevision(prompt, sha, paths)
}

// failingAgent fails every restricted call with concrete provider evidence.
type failingAgent struct{}

func (failingAgent) EjecutarPrompt(string) (string, error) { return "", nil }

func (failingAgent) EjecutarRevision(string, string, []string) (string, error) {
	return "", errors.New("provider exploded during audit")
}

func (failingAgent) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return failingAgent{}.EjecutarRevision(prompt, sha, paths)
}

// barrierAgent releases its wave only when waveSize reviewers are inside the
// call at the same time, deterministically proving the configured parallelism
// is actually used; a serialized engine hits the timeout and fails loudly.
type barrierAgent struct {
	release  chan struct{}
	once     sync.Once
	active   int32
	peak     int32
	waveSize int32
}

func newBarrierAgent(waveSize int32) *barrierAgent {
	return &barrierAgent{release: make(chan struct{}), waveSize: waveSize}
}

func (b *barrierAgent) EjecutarPrompt(string) (string, error) { return "", nil }

func (b *barrierAgent) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	current := atomic.AddInt32(&b.active, 1)
	defer atomic.AddInt32(&b.active, -1)
	for {
		peak := atomic.LoadInt32(&b.peak)
		if current <= peak || atomic.CompareAndSwapInt32(&b.peak, peak, current) {
			break
		}
	}
	if current == b.waveSize {
		b.once.Do(func() { close(b.release) })
	}
	select {
	case <-b.release:
	case <-time.After(2 * time.Second):
		return "", errors.New("barrier timeout: dimensions were executed serially")
	}
	return fmt.Sprintf(`{"dim":%q,"verdict":"ok"}`, migrationDimensionFromPrompt(prompt)), nil
}

func (b *barrierAgent) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return b.EjecutarRevision(prompt, sha, paths)
}

func (b *barrierAgent) observedPeak() int32 { return atomic.LoadInt32(&b.peak) }

// observerAgent mirrors cmd/sentinel's effective-agent wrapper (H4): it
// records authorship only after the wrapped agent answers without error.
type observerAgent struct {
	inner     RestrictedReviewer
	mu        sync.Mutex
	answerer  string
	successes int
}

// EjecutarPrompt satisfies AuditorAgente; durable audits only exercise the
// restricted path, so this delegation-free stub is enough here.
func (o *observerAgent) EjecutarPrompt(string) (string, error) { return "", nil }

func (o *observerAgent) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	output, err := o.inner.EjecutarRevision(prompt, sha, paths)
	if err == nil {
		o.mu.Lock()
		o.successes++
		o.answerer = "wrapped-dimension-agent"
		o.mu.Unlock()
	}
	return output, err
}

func (o *observerAgent) ReviewWithPolicy(prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	reviewer, ok := o.inner.(interface {
		ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error)
	})
	if !ok {
		return "", review.ErrRestrictedRequired
	}
	output, err := reviewer.ReviewWithPolicy(prompt, sha, paths, policy)
	if err == nil {
		o.mu.Lock()
		o.successes++
		o.answerer = "wrapped-dimension-agent"
		o.mu.Unlock()
	}
	return output, err
}

func TestMigrationLegacyAndControllerBackedOutcomesMatch(t *testing.T) {
	factory := func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return &cannedAgent{responses: []string{`{"dim":"logic","verdict":"ok"}`}}, "normal", nil
	}
	buildOpts := func(transport review.ReviewTransport) review.OpcionesAuditoria {
		return review.OpcionesAuditoria{
			SHA:             "migration-success",
			Mensaje:         "message",
			Diff:            "diff",
			Bundles:         migrationBundles(),
			Budget:          review.ReviewBudget{MaxCost: 2},
			ReviewTransport: transport,
		}
	}

	legacy := review.AuditarCommit(factory, 2, buildOpts(nil))
	durable := review.AuditarCommit(factory, 2, buildOpts(durableTestTransport(t, "migration-success")))

	if legacy.Veredicto != durable.Veredicto {
		t.Fatalf("verdicts differ: legacy=%q durable=%q", legacy.Veredicto, durable.Veredicto)
	}
	if len(legacy.Dims) != len(durable.Dims) || len(legacy.Dims) != 2 {
		t.Fatalf("dimension counts differ: legacy=%d durable=%d, want 2 each", len(legacy.Dims), len(durable.Dims))
	}
	// Completion order across dimension goroutines is nondeterministic, so
	// parity is compared per identity key rather than by slice index.
	dimKey := func(d review.ResultadoDimension) string { return d.Bundle + "/" + d.Dim }
	legacyByKey := make(map[string]review.ResultadoDimension, len(legacy.Dims))
	for _, d := range legacy.Dims {
		legacyByKey[dimKey(d)] = d
	}
	for _, d := range durable.Dims {
		l, ok := legacyByKey[dimKey(d)]
		if !ok {
			t.Fatalf("durable dimension %q absent from legacy run", dimKey(d))
		}
		if l.Perfil != d.Perfil {
			t.Fatalf("dim %s profile differs: %q vs %q", dimKey(d), l.Perfil, d.Perfil)
		}
		if l.Resultado == nil || d.Resultado == nil {
			t.Fatalf("dim %s has nil result: legacy=%v durable=%v", dimKey(d), l.Resultado, d.Resultado)
		}
		if l.Resultado.Verdict != d.Resultado.Verdict || l.Resultado.Reason != d.Resultado.Reason {
			t.Fatalf("dim %s outcomes differ: %q/%q vs %q/%q",
				dimKey(d), l.Resultado.Verdict, l.Resultado.Reason, d.Resultado.Verdict, d.Resultado.Reason)
		}
	}
	if len(legacy.Findings) != len(durable.Findings) {
		t.Fatalf("finding counts differ: legacy=%d durable=%d", len(legacy.Findings), len(durable.Findings))
	}
	if len(legacy.Skipped) != 1 || len(durable.Skipped) != 1 ||
		legacy.Skipped[0].Name != durable.Skipped[0].Name ||
		legacy.Skipped[0].Reason != durable.Skipped[0].Reason {
		t.Fatalf("budget skips differ: legacy=%+v durable=%+v, want identical security/budget_exhausted", legacy.Skipped, durable.Skipped)
	}
}

func TestMigrationFailureReasonsMatchEndToEnd(t *testing.T) {
	factory := func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return failingAgent{}, "normal", nil
	}
	bundles := []review.ReviewBundle{{Name: "quality", Dimensions: []string{"logic"}, Priority: review.PriorityRequired}}
	buildOpts := func(transport review.ReviewTransport) review.OpcionesAuditoria {
		return review.OpcionesAuditoria{SHA: "migration-failure", Mensaje: "m", Diff: "d", Bundles: bundles, ReviewTransport: transport}
	}

	legacy := review.AuditarCommit(factory, 1, buildOpts(nil))
	durable := review.AuditarCommit(factory, 1, buildOpts(durableTestTransport(t, "migration-failure")))

	for _, result := range []review.ResultadoAuditoria{legacy, durable} {
		if len(result.Dims) != 1 || result.Dims[0].Resultado == nil {
			t.Fatalf("dims = %+v, want one unavailable dimension", result.Dims)
		}
	}
	legacyResult, durableResult := legacy.Dims[0].Resultado, durable.Dims[0].Resultado
	if legacyResult.Verdict != review.VerdictUnavailable || durableResult.Verdict != review.VerdictUnavailable {
		t.Fatalf("verdicts = %q / %q, want unavailable in both modes", legacyResult.Verdict, durableResult.Verdict)
	}
	wantText := "provider exploded during audit"
	if !strings.Contains(legacyResult.Reason, wantText) || !strings.Contains(durableResult.Reason, wantText) {
		t.Fatalf("reasons = %q / %q, want concrete provider text %q preserved in both", legacyResult.Reason, durableResult.Reason, wantText)
	}
}

func TestParallelismParityBoundedAndConcurrentUnderBothModes(t *testing.T) {
	runAudit := func(sha string, transport review.ReviewTransport) (review.ResultadoAuditoria, *barrierAgent) {
		barrier := newBarrierAgent(2)
		bundles := []review.ReviewBundle{{Name: "quality", Dimensions: []string{"logic", "style"}, Priority: review.PriorityRequired}}
		result := review.AuditarCommit(func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
			return barrier, "normal", nil
		}, 2, review.OpcionesAuditoria{
			SHA:             sha,
			Mensaje:         "m",
			Diff:            "d",
			Bundles:         bundles,
			ReviewTransport: transport,
		})
		return result, barrier
	}

	legacyResult, legacyBarrier := runAudit("parity-legacy", nil)
	durableResult, durableBarrier := runAudit("parity-durable", durableTestTransport(t, "parity-durable"))

	if got := legacyBarrier.observedPeak(); got != 2 {
		t.Fatalf("legacy peak concurrency = %d, want exactly configured parallel=2", got)
	}
	if got := durableBarrier.observedPeak(); got != 2 {
		t.Fatalf("durable peak concurrency = %d, want exactly configured parallel=2", got)
	}
	if legacyResult.Veredicto != durableResult.Veredicto || legacyResult.Veredicto == review.VerdictUnavailable {
		t.Fatalf("verdicts = %q / %q, want equal non-unavailable parity", legacyResult.Veredicto, durableResult.Veredicto)
	}
}

func TestDurablePathAttributionRecordsAnsweringAgent(t *testing.T) {
	inner := &cannedAgent{responses: []string{`{"dim":"logic","verdict":"ok"}`}}
	observer := &observerAgent{inner: inner}
	bundles := []review.ReviewBundle{{Name: "quality", Dimensions: []string{"logic"}, Priority: review.PriorityRequired}}

	result := review.AuditarCommit(func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return observer, "normal", nil
	}, 1, review.OpcionesAuditoria{
		SHA:             "attribution-sha",
		Mensaje:         "m",
		Diff:            "d",
		Bundles:         bundles,
		ReviewTransport: durableTestTransport(t, "attribution-sha"),
	})

	if len(result.Dims) != 1 || result.Dims[0].Resultado == nil || result.Dims[0].Resultado.Verdict != "ok" {
		t.Fatalf("result = %+v, want successful routed dimension", result.Dims)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.successes != 1 || observer.answerer != "wrapped-dimension-agent" {
		t.Fatalf("observer = {successes:%d answerer:%q}, want post-success attribution through the durable path", observer.successes, observer.answerer)
	}
}

// findingBearingResponse emits one v2 finding (marker fields make esV2 true)
// so content parity — not just counts — is proven between execution modes.
const findingBearingResponse = `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","severity":"critical","description":"unchecked nil dereference in handler","id":"logic-nil-deref-1","title":"nil dereference","evidence":"handler dereferences cfg before the nil guard","confidence":"high","location":{"file":"a.go","line_start":3,"line_end":3},"status":"open"}]}`

func TestMigrationFindingsParityCarriesFullContent(t *testing.T) {
	factory := func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return &cannedAgent{responses: []string{findingBearingResponse}}, "normal", nil
	}
	bundles := []review.ReviewBundle{{Name: "quality", Dimensions: []string{"logic"}, Priority: review.PriorityRequired}}
	buildOpts := func(transport review.ReviewTransport) review.OpcionesAuditoria {
		return review.OpcionesAuditoria{SHA: "migration-findings", Mensaje: "m", Diff: "d", Bundles: bundles, ReviewTransport: transport}
	}

	legacy := review.AuditarCommit(factory, 1, buildOpts(nil))
	durable := review.AuditarCommit(factory, 1, buildOpts(durableTestTransport(t, "migration-findings")))

	if len(legacy.Findings) != 1 || len(durable.Findings) != 1 {
		t.Fatalf("findings = %d / %d, want one finding each", len(legacy.Findings), len(durable.Findings))
	}
	lf, df := legacy.Findings[0], durable.Findings[0]
	if lf.Description != df.Description || lf.Severity != df.Severity ||
		lf.Dimension != df.Dimension || lf.Location != df.Location ||
		lf.Title != df.Title || lf.Fingerprint != df.Fingerprint {
		t.Fatalf("finding content differs:\nlegacy  %+v\ndurable %+v", lf, df)
	}
	if legacy.Veredicto != durable.Veredicto || legacy.Veredicto == review.VerdictUnavailable {
		t.Fatalf("verdicts = %q / %q, want equal non-unavailable parity", legacy.Veredicto, durable.Veredicto)
	}
}

func TestMigrationBudgetAdmitsAffordableOptionalBundleInBothModes(t *testing.T) {
	factory := func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return &cannedAgent{}, "normal", nil
	}
	bundles := migrationBundles()
	buildOpts := func(transport review.ReviewTransport) review.OpcionesAuditoria {
		return review.OpcionesAuditoria{
			SHA:             "migration-admit",
			Mensaje:         "m",
			Diff:            "d",
			Bundles:         bundles,
			Budget:          review.ReviewBudget{MaxCost: 10},
			ReviewTransport: transport,
		}
	}

	legacy := review.AuditarCommit(factory, 2, buildOpts(nil))
	durable := review.AuditarCommit(factory, 2, buildOpts(durableTestTransport(t, "migration-admit")))

	for _, result := range []review.ResultadoAuditoria{legacy, durable} {
		if len(result.Skipped) != 0 {
			t.Fatalf("skipped = %+v, want affordable optional bundle admitted", result.Skipped)
		}
		if !hasDimension(result, "security", "security") {
			t.Fatalf("dims = %+v, want admitted security dimension", result.Dims)
		}
	}
}

func hasDimension(result review.ResultadoAuditoria, bundleName, dimension string) bool {
	for _, d := range result.Dims {
		if d.Bundle == bundleName && d.Dim == dimension {
			return true
		}
	}
	return false
}

// TestMigrationInvocationIdentityStrictVersusLenient extends migration parity
// to the ticket 07 cutover: under strict admission a successful durable
// dimension produces the SAME verdict and findings as legacy PLUS a non-empty
// producing invocation identity; under lenient mode (evidence_admission=false)
// the result is identical to legacy with EMPTY invocation identities,
// proving lenient equals pre-R6 byte behavior.
func TestMigrationInvocationIdentityStrictVersusLenient(t *testing.T) {
	factory := func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return &cannedAgent{responses: []string{findingBearingResponse}}, "normal", nil
	}
	bundles := []review.ReviewBundle{{Name: "quality", Dimensions: []string{"logic"}, Priority: review.PriorityRequired}}
	buildOpts := func(transport review.ReviewTransport) review.OpcionesAuditoria {
		return review.OpcionesAuditoria{
			SHA:             "migration-invocation",
			Mensaje:         "m",
			Diff:            "d",
			Bundles:         bundles,
			ReviewTransport: transport,
		}
	}

	legacy := review.AuditarCommit(factory, 1, buildOpts(nil))
	strict := review.AuditarCommit(factory, 1, buildOpts(durableTestTransport(t, "migration-invocation")))
	lenient := review.AuditarCommit(factory, 1, buildOpts(lenientTestTransport(t, "migration-invocation")))

	if strict.Veredicto != legacy.Veredicto || lenient.Veredicto != legacy.Veredicto {
		t.Fatalf("verdicts differ: legacy=%q strict=%q lenient=%q, want identical outcomes in every mode",
			legacy.Veredicto, strict.Veredicto, lenient.Veredicto)
	}
	if legacy.Veredicto == review.VerdictUnavailable {
		t.Fatalf("verdict = %q, want a successful (non-unavailable) audit", legacy.Veredicto)
	}
	if legacy.Findings[0].Fingerprint != strict.Findings[0].Fingerprint ||
		legacy.Findings[0].Description != strict.Findings[0].Description ||
		lenient.Findings[0].Fingerprint != legacy.Findings[0].Fingerprint ||
		lenient.Findings[0].Description != legacy.Findings[0].Description {
		t.Fatalf("finding content differs across modes:\nlegacy %+v\nstrict %+v\nlenient %+v",
			legacy.Findings[0], strict.Findings[0], lenient.Findings[0])
	}

	if len(strict.Dims) != 1 || strict.Dims[0].Resultado == nil {
		t.Fatalf("strict dims = %+v, want one routed dimension", strict.Dims)
	}
	if strict.Dims[0].Resultado.InvocationID == "" {
		t.Fatal("strict InvocationID = empty, want the producing durable invocation bound to the dimension")
	}
	if strict.Findings[0].InvocationID != strict.Dims[0].Resultado.InvocationID {
		t.Fatalf("finding invocation %q differs from its producing dimension invocation %q",
			strict.Findings[0].InvocationID, strict.Dims[0].Resultado.InvocationID)
	}

	// Lenient mode must reproduce legacy exactly: same verdict and finding
	// content, and NO provenance metadata anywhere.
	if len(lenient.Dims) != 1 || lenient.Dims[0].Resultado == nil {
		t.Fatalf("lenient dims = %+v, want one routed dimension", lenient.Dims)
	}
	if got := lenient.Dims[0].Resultado.InvocationID; got != "" {
		t.Fatalf("lenient InvocationID = %q, want empty (pre-R6 byte behavior)", got)
	}
	if got := lenient.Findings[0].InvocationID; got != "" {
		t.Fatalf("lenient finding InvocationID = %q, want empty like the legacy record", got)
	}
	if lenient.Dims[0].Resultado.Verdict != legacy.Dims[0].Resultado.Verdict ||
		lenient.Dims[0].Resultado.Reason != legacy.Dims[0].Resultado.Reason {
		t.Fatalf("lenient dimension outcome differs from legacy: %+v vs %+v",
			lenient.Dims[0].Resultado, legacy.Dims[0].Resultado)
	}
}

func TestAttributionDoesNotFireOnProviderFailure(t *testing.T) {
	observer := &observerAgent{inner: failingAgent{}}
	bundles := []review.ReviewBundle{{Name: "quality", Dimensions: []string{"logic"}, Priority: review.PriorityRequired}}

	result := review.AuditarCommit(func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return observer, "normal", nil
	}, 1, review.OpcionesAuditoria{
		SHA:             "attribution-failure",
		Mensaje:         "m",
		Diff:            "d",
		Bundles:         bundles,
		ReviewTransport: durableTestTransport(t, "attribution-failure"),
	})

	if len(result.Dims) != 1 || result.Dims[0].Resultado == nil || result.Dims[0].Resultado.Verdict != review.VerdictUnavailable {
		t.Fatalf("result = %+v, want unavailable failure routed through durable path", result.Dims)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.successes != 0 || observer.answerer != "" {
		t.Fatalf("observer fired on failure {successes:%d answerer:%q}, want no authorship without a successful answer", observer.successes, observer.answerer)
	}
}
