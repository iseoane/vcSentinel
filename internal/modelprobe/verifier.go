// Package modelprobe verifies the model that an agent reports for a session.
package modelprobe

import (
	"strings"
	"sync"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

const promptModel = "What model are you actually using? Reply with only the exact model identifier."

const maxModelIdentifierLength = 128

// Agent is the minimal agent capability needed for a model probe.
type Agent interface {
	RunPrompt(prompt string) (string, error)
}

// ReportsConfiguredModel is implemented when an adapter can identify the
// model selected after any fallback resolution.
type ReportsConfiguredModel interface {
	ConfiguredModel() (string, bool)
}

// Outcome is the typed result of one model probe. Only OutcomeMatched lets
// a consumer claim the model as verified; every other outcome keeps the
// honest default of unverified.
type Outcome string

const (
	// OutcomeMatched: probed and the reported identifier equals the expected one.
	OutcomeMatched Outcome = "matched"
	// OutcomeMismatch: probed with a parseable reply that differs.
	OutcomeMismatch Outcome = "mismatch"
	// OutcomeProbeError: the probe prompt itself failed.
	OutcomeProbeError Outcome = "probe_error"
	// OutcomeUnparseable: the reply fails validReportedModel.
	OutcomeUnparseable Outcome = "unparseable"
	// OutcomeSkipped: nothing to probe (empty profile, nil agent or store)
	// or no expected model resolvable.
	OutcomeSkipped Outcome = "skipped"
)

// Verifier runs at most one probe per configured profile in a session,
// retaining each outcome for later queries. Concurrent callers for the same
// profile wait for the in-flight probe instead of reading a placeholder:
// dimensions audit in parallel and stamp right after probing, so returning
// early would stamp siblings with whatever happened to be stored mid-flight.
type Verifier struct {
	store  *store.Store
	probes sync.Map // profile name -> *probeCall
}

// probeCall is one session's single probe for a profile. done closes when
// outcome is final; readers after close see the outcome via the
// close-happens-before guarantee, so no further synchronization is needed.
type probeCall struct {
	done    chan struct{}
	outcome Outcome
}

func NewVerifier(s *store.Store) *Verifier {
	return &Verifier{store: s}
}

// Verify probes the agent for its model and records the outcome without
// affecting the caller's review request: a probe that errors never fails a
// review, a gate, or a PR flow. It probes at most once per profile per
// session; a repeated call returns the stored first outcome.
//
// A match writes a positive profile record (status verified), giving the
// mismatch signal a counterpart and making store.ReadProfile useful. A
// mismatch keeps writing the unverified record. Every other outcome
// records nothing.
func (v *Verifier) Verify(profile, expected string, agent Agent) Outcome {
	if profile == "" || agent == nil || v.store == nil {
		return OutcomeSkipped
	}
	call := &probeCall{done: make(chan struct{})}
	actual, loaded := v.probes.LoadOrStore(profile, call)
	if loaded {
		previous, ok := actual.(*probeCall)
		if !ok {
			return OutcomeSkipped
		}
		<-previous.done
		return previous.outcome
	}
	call.outcome = v.probe(profile, expected, agent)
	close(call.done)
	return call.outcome
}

// Verified reports whether the profile was probed and matched in this
// session. Anything else — mismatch, error, unparseable reply, in-flight
// probe, or never probed — is false.
func (v *Verifier) Verified(profile string) bool {
	raw, ok := v.probes.Load(profile)
	if !ok {
		return false
	}
	call, ok := raw.(*probeCall)
	if !ok {
		return false
	}
	select {
	case <-call.done:
		return call.outcome == OutcomeMatched
	default:
		return false
	}
}

func (v *Verifier) probe(profile, expected string, agent Agent) Outcome {
	actual, err := agent.RunPrompt(promptModel)
	if err != nil {
		return OutcomeProbeError
	}
	if expected == "" {
		if reporter, ok := agent.(ReportsConfiguredModel); ok {
			if model, ok := reporter.ConfiguredModel(); ok {
				expected = model
			}
		}
	}
	actual, ok := validReportedModel(actual)
	if expected == "" || !ok {
		if expected == "" {
			return OutcomeSkipped
		}
		return OutcomeUnparseable
	}
	if actual == expected {
		_ = v.store.SaveProfile(&store.Profile{
			Name:          profile,
			Status:        store.ProfileVerified,
			Event:         "model_match",
			ExpectedModel: expected,
			ActualModel:   actual,
		})
		return OutcomeMatched
	}
	_ = v.store.SaveProfile(&store.Profile{
		Name:          profile,
		Status:        store.ProfileUnverified,
		Event:         "model_mismatch",
		ExpectedModel: expected,
		ActualModel:   actual,
	})
	return OutcomeMismatch
}

func validReportedModel(model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" || len(model) > maxModelIdentifierLength {
		return "", false
	}
	for _, r := range model {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == '/') {
			return "", false
		}
	}
	return model, true
}
