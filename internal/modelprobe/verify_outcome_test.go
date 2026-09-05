package modelprobe

import (
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

type countingAgent struct {
	modelo string
	err    error
	calls  int
}

func (a *countingAgent) EjecutarPrompt(string) (string, error) {
	a.calls++
	return a.modelo, a.err
}

func TestVerifyReturnsMatchedAndVerifiedOnMatch(t *testing.T) {
	s := store.NuevoStore(t.TempDir())
	v := NuevoVerificador(s)

	if got := v.Verify("normal", "openai/gpt-5.6-terra", agenteModeloFalso{modelo: "openai/gpt-5.6-terra"}); got != OutcomeMatched {
		t.Fatalf("outcome = %q, want %q", got, OutcomeMatched)
	}
	if !v.Verified("normal") {
		t.Error("Verified(normal) = false after a matched probe, want true")
	}
}

func TestVerifyReturnsMismatchAndKeepsVerifiedFalse(t *testing.T) {
	s := store.NuevoStore(t.TempDir())
	v := NuevoVerificador(s)

	if got := v.Verify("normal", "openai/gpt-5.6-terra", agenteModeloFalso{modelo: "openai/gpt-5.6-sol"}); got != OutcomeMismatch {
		t.Fatalf("outcome = %q, want %q", got, OutcomeMismatch)
	}
	if v.Verified("normal") {
		t.Error("Verified(normal) = true after a mismatch, want false")
	}
}

func TestVerifyProbeErrorIsNonFatalAndUnverified(t *testing.T) {
	s := store.NuevoStore(t.TempDir())
	v := NuevoVerificador(s)

	if got := v.Verify("normal", "openai/gpt-5.6-terra", agenteModeloFalso{err: errors.New("timeout")}); got != OutcomeProbeError {
		t.Fatalf("outcome = %q, want %q", got, OutcomeProbeError)
	}
	if v.Verified("normal") {
		t.Error("Verified(normal) = true after a probe error, want false")
	}
	if profile, _ := s.LeerPerfil("normal"); profile != nil {
		t.Errorf("profile = %+v, want nil: an errored probe records nothing", profile)
	}
}

func TestVerifyUnparseableReplyIsUnverified(t *testing.T) {
	s := store.NuevoStore(t.TempDir())
	v := NuevoVerificador(s)

	if got := v.Verify("normal", "openai/gpt-5.6-terra", agenteModeloFalso{modelo: "I am \"the best\" model!"}); got != OutcomeUnparseable {
		t.Fatalf("outcome = %q, want %q", got, OutcomeUnparseable)
	}
	if v.Verified("normal") {
		t.Error("Verified(normal) = true after an unparseable reply, want false")
	}
}

func TestVerifySkipsWithoutProbeEvidence(t *testing.T) {
	s := store.NuevoStore(t.TempDir())
	v := NuevoVerificador(s)

	cases := []struct {
		name    string
		profile string
		model   string
		agent   Agente
	}{
		{"empty profile", "", "openai/gpt-5.6-terra", agenteModeloFalso{modelo: "openai/gpt-5.6-terra"}},
		{"nil agent", "normal", "openai/gpt-5.6-terra", nil},
		{"no expected model", "normal", "", agenteModeloFalso{modelo: "openai/gpt-5.6-terra"}},
	}
	for _, tc := range cases {
		if got := v.Verify(tc.profile, tc.model, tc.agent); got != OutcomeSkipped {
			t.Errorf("%s: outcome = %q, want %q", tc.name, got, OutcomeSkipped)
		}
	}
	if v.Verified("normal") {
		t.Error("Verified(normal) = true without any probe, want false")
	}
	nilStore := NuevoVerificador(nil)
	if got := nilStore.Verify("normal", "m", agenteModeloFalso{modelo: "m"}); got != OutcomeSkipped {
		t.Errorf("nil store: outcome = %q, want %q", got, OutcomeSkipped)
	}
}

func TestVerifySecondCallReturnsStoredOutcomeWithoutReprobing(t *testing.T) {
	s := store.NuevoStore(t.TempDir())
	v := NuevoVerificador(s)
	agent := &countingAgent{modelo: "openai/gpt-5.6-terra"}

	first := v.Verify("normal", "openai/gpt-5.6-terra", agent)
	second := v.Verify("normal", "openai/gpt-5.6-sol", agent)

	if agent.calls != 1 {
		t.Fatalf("probe calls = %d, want exactly 1 per profile per session", agent.calls)
	}
	if first != OutcomeMatched || second != OutcomeMatched {
		t.Errorf("outcomes = %q, %q: the second call must return the stored first outcome", first, second)
	}
	if !v.Verified("normal") {
		t.Error("Verified(normal) = false, want the stored matched outcome")
	}
}
