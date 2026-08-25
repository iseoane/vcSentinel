// Package reviewcontract defines provider-neutral semantic review contracts.
package reviewcontract

import "fmt"

const (
	DimensionLogic    = "logic"
	DimensionStyle    = "style"
	DimensionDesign   = "design"
	DimensionTests    = "tests"
	DimensionSecurity = "security"
	DimensionSpec     = "spec"
)

// ToolPolicy limits a reviewer to capabilities that are independent of any
// provider's command-line syntax.
type ToolPolicy struct {
	AllowRead                bool
	AllowSearch              bool
	RequireImmutableSnapshot bool
	AllowMutation            bool
	AllowShell               bool
	AllowNetwork             bool
}

// EvidencePolicy defines the evidence required before a semantic finding can
// influence a review result.
type EvidencePolicy struct {
	RequireLiteralEvidence           bool
	RequireConfidence                bool
	VerifyAbsenceBeforeClaiming      bool
	ReportOnlyDiffIntroducedFindings bool
}

// OutputSchema describes the provider-independent review result envelope.
type OutputSchema struct {
	BeginDelimiter string
	EndDelimiter   string
	TopLevelFields []string
	FindingFields  []string
	QuestionFields []string
}

// DimensionContract is the authoritative definition of one semantic review
// dimension. Provider profiles select transport parameters only; they do not
// define this contract.
type DimensionContract struct {
	Name           string
	Instructions   string
	OutputSchema   OutputSchema
	EvidencePolicy EvidencePolicy
	ToolPolicy     ToolPolicy
	DefaultProfile string
}

var canonicalOutputSchema = OutputSchema{
	BeginDelimiter: "BEGIN_REVIEW",
	EndDelimiter:   "END_REVIEW",
	TopLevelFields: []string{"dim", "verdict", "findings", "questions", "reason"},
	FindingFields:  []string{"dimension", "file", "line", "severity", "description", "suggestion", "evidence", "confidence"},
	QuestionFields: []string{"id", "text", "file"},
}

var canonicalEvidencePolicy = EvidencePolicy{
	RequireLiteralEvidence:           true,
	RequireConfidence:                true,
	VerifyAbsenceBeforeClaiming:      true,
	ReportOnlyDiffIntroducedFindings: true,
}

var canonicalToolPolicy = ToolPolicy{
	AllowRead:                true,
	AllowSearch:              true,
	RequireImmutableSnapshot: true,
}

var contracts = map[string]DimensionContract{
	DimensionLogic: {
		Name:           DimensionLogic,
		Instructions:   "Correctness of behavior: conditions, error handling, edge cases and side effects.",
		OutputSchema:   canonicalOutputSchema,
		EvidencePolicy: canonicalEvidencePolicy,
		ToolPolicy:     canonicalToolPolicy,
		DefaultProfile: "normal",
	},
	DimensionStyle: {
		Name:           DimensionStyle,
		Instructions:   "Language and clarity: naming, idiomatic code and adherence to repository conventions.",
		OutputSchema:   canonicalOutputSchema,
		EvidencePolicy: canonicalEvidencePolicy,
		ToolPolicy:     canonicalToolPolicy,
		DefaultProfile: "cheap",
	},
	DimensionDesign: {
		Name:           DimensionDesign,
		Instructions:   "Structure and coupling: cohesion, open/closed principle, deep modules and dependencies pointing toward the domain.",
		OutputSchema:   canonicalOutputSchema,
		EvidencePolicy: canonicalEvidencePolicy,
		ToolPolicy:     canonicalToolPolicy,
		DefaultProfile: "deep",
	},
	DimensionTests: {
		Name:           DimensionTests,
		Instructions:   "Test value: meaningful coverage, determinism and a failing test implying a real behavior change.",
		OutputSchema:   canonicalOutputSchema,
		EvidencePolicy: canonicalEvidencePolicy,
		ToolPolicy:     canonicalToolPolicy,
		DefaultProfile: "normal",
	},
	DimensionSecurity: {
		Name:           DimensionSecurity,
		Instructions:   "Privilege boundaries, untrusted input handling and exposed data.",
		OutputSchema:   canonicalOutputSchema,
		EvidencePolicy: canonicalEvidencePolicy,
		ToolPolicy:     canonicalToolPolicy,
		DefaultProfile: "deep",
	},
	DimensionSpec: {
		Name:           DimensionSpec,
		Instructions:   "The diff does exactly what the commit message claims: no out-of-scope work and no unbacked claims.",
		OutputSchema:   canonicalOutputSchema,
		EvidencePolicy: canonicalEvidencePolicy,
		ToolPolicy:     canonicalToolPolicy,
		DefaultProfile: "cheap",
	},
}

// Lookup returns an isolated copy of the named canonical contract.
func Lookup(name string) (DimensionContract, error) {
	contract, ok := contracts[name]
	if !ok {
		return DimensionContract{}, fmt.Errorf("unknown review dimension %q", name)
	}
	return copyContract(contract), nil
}

// All returns isolated copies of every canonical contract in stable dimension order.
func All() []DimensionContract {
	names := []string{DimensionLogic, DimensionStyle, DimensionDesign, DimensionTests, DimensionSecurity, DimensionSpec}
	result := make([]DimensionContract, 0, len(names))
	for _, name := range names {
		contract, _ := Lookup(name)
		result = append(result, contract)
	}
	return result
}

// DefaultToolPolicy returns the canonical restricted policy used by legacy
// transport boundaries that do not carry a dimension name.
func DefaultToolPolicy() ToolPolicy {
	contract, err := Lookup(DimensionLogic)
	if err != nil {
		panic(fmt.Sprintf("canonical review contract unavailable: %v", err))
	}
	return contract.ToolPolicy
}

// DefaultProfile returns the provider-neutral execution profile selected by a
// canonical dimension contract.
func DefaultProfile(name string) string {
	contract, err := Lookup(name)
	if err != nil {
		panic(fmt.Sprintf("canonical review contract unavailable: %v", err))
	}
	return contract.DefaultProfile
}

func copyContract(contract DimensionContract) DimensionContract {
	contract.OutputSchema.TopLevelFields = append([]string(nil), contract.OutputSchema.TopLevelFields...)
	contract.OutputSchema.FindingFields = append([]string(nil), contract.OutputSchema.FindingFields...)
	contract.OutputSchema.QuestionFields = append([]string(nil), contract.OutputSchema.QuestionFields...)
	return contract
}
