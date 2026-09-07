package modelprobe

import (
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

type fakeModelAgent struct {
	model string
	err   error
}

func (a fakeModelAgent) RunPrompt(string) (string, error) {
	return a.model, a.err
}

type fakeConfiguredModelAgent struct {
	fakeModelAgent
	expected string
}

func (a fakeConfiguredModelAgent) ConfiguredModel() (string, bool) {
	return a.expected, true
}

type unavailableConfiguredModelAgent struct {
	fakeModelAgent
}

func (unavailableConfiguredModelAgent) ConfiguredModel() (string, bool) {
	return "openai/gpt-5.6-terra", false
}

func TestVerifierPersistsNormalizedOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		profile  string
		expected string
		agent    Agent
		stored   *store.Profile
	}{
		{
			name:     "mismatch persists normalized values",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    fakeModelAgent{model: "  openai/gpt-5.6-sol  "},
			stored: &store.Profile{
				Name:          "normal",
				Status:        store.ProfileUnverified,
				Event:         "model_mismatch",
				ExpectedModel: "openai/gpt-5.6-terra",
				ActualModel:   "openai/gpt-5.6-sol",
			},
		},
		{
			name:     "matching surrounding whitespace is ignored",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    fakeModelAgent{model: "  openai/gpt-5.6-terra\t"},
			stored: &store.Profile{
				Name:          "normal",
				Status:        store.ProfileVerified,
				Event:         "model_match",
				ExpectedModel: "openai/gpt-5.6-terra",
				ActualModel:   "openai/gpt-5.6-terra",
			},
		},
		{
			name:     "mismatch with trailing LF is recorded",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    fakeModelAgent{model: "openai/gpt-5.6-sol\n"},
			stored: &store.Profile{
				Name:          "normal",
				Status:        store.ProfileUnverified,
				Event:         "model_mismatch",
				ExpectedModel: "openai/gpt-5.6-terra",
				ActualModel:   "openai/gpt-5.6-sol",
			},
		},
		{
			name:     "mismatch with trailing CRLF is recorded",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    fakeModelAgent{model: "openai/gpt-5.6-sol\r\n"},
			stored: &store.Profile{
				Name:          "normal",
				Status:        store.ProfileUnverified,
				Event:         "model_mismatch",
				ExpectedModel: "openai/gpt-5.6-terra",
				ActualModel:   "openai/gpt-5.6-sol",
			},
		},
		{
			name:     "agent error is ignored",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    fakeModelAgent{err: errors.New("unavailable")},
		},
		{
			name:    "unavailable configured model is ignored",
			profile: "normal",
			agent: unavailableConfiguredModelAgent{
				fakeModelAgent: fakeModelAgent{model: "openai/gpt-5.6-sol"},
			},
		},
		{
			name:    "available configured model is used",
			profile: "normal",
			agent: fakeConfiguredModelAgent{
				fakeModelAgent: fakeModelAgent{model: "openai/gpt-5.6-sol"},
				expected:       "openai/gpt-5.6-terra",
			},
			stored: &store.Profile{
				Name:          "normal",
				Status:        store.ProfileUnverified,
				Event:         "model_mismatch",
				ExpectedModel: "openai/gpt-5.6-terra",
				ActualModel:   "openai/gpt-5.6-sol",
			},
		},
		{
			name:     "invalid response is ignored",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    fakeModelAgent{model: "openai/gpt-5.6-sol\nuntrusted"},
		},
		{
			name:     "literal 129 character response is ignored",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    fakeModelAgent{model: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := store.NewStore(t.TempDir())
			v := NewVerifier(s)

			v.Verify(tt.profile, tt.expected, tt.agent)

			profile, err := s.ReadProfile(tt.profile)
			if err != nil {
				t.Fatalf("ReadProfile: %v", err)
			}
			if tt.stored == nil {
				if profile != nil {
					t.Errorf("profile = %+v, want nil", profile)
				}
				return
			}
			if profile == nil {
				t.Fatal("the mismatched profile was not recorded")
			}
			if *profile != *tt.stored {
				t.Errorf("profile = %+v, want %+v", profile, tt.stored)
			}
		})
	}
}

func TestReportedModelAccepts128CharLiteral(t *testing.T) {
	model, ok := validReportedModel("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if !ok {
		t.Fatal("validReportedModel rejected a 128 character identifier")
	}
	if len(model) != 128 {
		t.Errorf("len(model) = %d, want 128", len(model))
	}
}

func TestVerifierProbesOncePerProfile(t *testing.T) {
	s := store.NewStore(t.TempDir())
	v := NewVerifier(s)
	agent := &countingAgent{model: "openai/gpt-5.6-terra"}

	first := v.Verify("normal", "openai/gpt-5.6-terra", agent)
	second := v.Verify("normal", "openai/gpt-5.6-sol", agent)

	if agent.calls != 1 {
		t.Fatalf("probe calls = %d, want exactly 1 per profile per session", agent.calls)
	}
	if first != OutcomeMatched || second != OutcomeMatched {
		t.Errorf("outcomes = %q, %q: want the stored first outcome twice", first, second)
	}
	profile, err := s.ReadProfile("normal")
	if err != nil {
		t.Fatalf("ReadProfile: %v", err)
	}
	if profile == nil || profile.Status != store.ProfileVerified {
		t.Errorf("profile = %+v, want the verified match record", profile)
	}
}
