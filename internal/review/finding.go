package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/reviewcontract"
)

// Canonical audit dimensions. They are the Go + prompt contract with the
// agent: the English vocabulary is fixed and validated strictly.
const (
	DimLogic    = reviewcontract.DimensionLogic
	DimStyle    = reviewcontract.DimensionStyle
	DimDesign   = reviewcontract.DimensionDesign
	DimTests    = reviewcontract.DimensionTests
	DimSecurity = reviewcontract.DimensionSecurity
	DimSpec     = reviewcontract.DimensionSpec
)

// Severities of a finding.
const (
	SevCritical = "CRITICAL"
	SevWarning  = "WARNING"
	SevAdvisory = "ADVISORY"
)

// Verdicts of a dimension.
const (
	VerdictOK          = "ok"
	VerdictWarn        = "warn"
	VerdictBlock       = "block"
	VerdictQuestion    = "question"
	VerdictUnavailable = "unavailable"
)

var validVerdicts = map[string]bool{
	VerdictOK:          true,
	VerdictWarn:        true,
	VerdictBlock:       true,
	VerdictQuestion:    true,
	VerdictUnavailable: true,
}

// Line is the line number of a finding. Agents sometimes emit "line" as a
// number and sometimes as a string ("126"); UnmarshalJSON accepts both so a
// numeric string does not discard the whole finding.
type Line int

// UnmarshalJSON accepts a JSON number or a numeric string. A non-numeric
// string returns an error: Line is the PERSISTED shape and stays strict
// (the ledger must not silence a corrupted line on reload). Agent-input
// tolerance — any non-numeric value leaves the line at 0 and preserves the
// raw text in LineRaw — lives in rawLine (FU-19), which rawFinding uses
// when parsing.
func (l *Line) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("non-numeric line %q", s)
		}
		*l = Line(n)
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*l = Line(n)
	return nil
}

// ReviewFinding is a concrete finding from the agent about one line of a
// file in the audited commit.
type ReviewFinding struct {
	Dimension string `json:"dimension"`
	File      string `json:"file"`
	Line      Line   `json:"line"`
	// LineRaw preserves the raw "line" text an agent emitted when it was not
	// a number (e.g. the range "17-19, 23-48"). FU-19 keeps such a finding
	// with Line=0 (unknown) instead of discarding it, and persists the raw
	// text here so the operator can read what the agent actually cited:
	// Warnings is memory-only (json:"-"), this field survives in the
	// persisted record. Deliberately never a Fingerprint input: it is untrusted
	// provider formatting, and the fingerprint must stay line-independent.
	// Empty unless the parse fell back to unknown, so existing persisted
	// records marshal byte-identically.
	LineRaw             string `json:"line_raw,omitempty"`
	Severity            string `json:"severity"`
	Description         string `json:"description"`
	Suggestion          string `json:"suggestion"`
	Source              string `json:"source,omitempty"`
	Status              string `json:"status,omitempty"`
	RefutationReason    string `json:"refutation_reason,omitempty"`
	RefutationEvidence  string `json:"refutation_evidence,omitempty"`
	RefutationLineStart int    `json:"refutation_line_start,omitempty"`
	RefutationLineEnd   int    `json:"refutation_line_end,omitempty"`
	RefutationRangeHash string `json:"refutation_range_hash,omitempty"`
	// RefutationActor records who issued the refutation: RefutationActorRefuter
	// for the automated refuter, RefutationActorHuman for a person answering
	// through the refute command. Empty on records written before FU-6, which
	// only the automated path could produce.
	RefutationActor string `json:"refutation_actor,omitempty"`
}

// Possible sources of a Finding (finding v2): what produced the finding.
const (
	SourceValidation = "validation" // deterministic command (internal/validation)
	SourceReview     = "review"     // semantic inference from an LLM agent
)

// Lifecycle states of a Finding (finding v2). pending is the initial
// state; confirmed/refuted are set by mechanical refutation or by the
// agent; accepted_by_user and reopened are set by a human; fixed is set by
// post-patch verification.
const (
	StatusPending        = "pending"
	StatusConfirmed      = "confirmed"
	StatusRefuted        = "refuted"
	StatusAcceptedByUser = "accepted_by_user"
	StatusFixed          = "fixed"
	StatusReopened       = "reopened"
)

// NormalizeStatus canonicalises a persisted lifecycle status so every site
// that interprets one agrees about the same record. The ledger is written by
// several producers across several schema generations, so a status can arrive
// padded or in a different case; comparing it raw at one site and normalised
// at another makes the two halves of a decision disagree about a single
// finding. It returns the empty string for an absent or whitespace-only
// status: an unknown disposition, never a present one.
//
// This is the single normalisation point for Status. Compare through it on
// both sides rather than repeating strings.ToLower(strings.TrimSpace(...)).
func NormalizeStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

// Confidence levels for automatically applying a Finding's suggested fix.
// safe: applicable without review; needs_review: applicable but a human
// must confirm; manual: no mechanical fix is possible.
const (
	FixableSafe        = "safe"
	FixableNeedsReview = "needs_review"
	FixableManual      = "manual"
)

// Producer identifies who or what generated a Finding: an LLM agent (with
// its model and effort) or a deterministic command (binary). ModelVerified
// distinguishes a model whose name was confirmed by the agent itself (e.g.
// via --version or API metadata) from one assumed by configuration.
type Producer struct {
	Agent         string `json:"agent"`
	Binary        string `json:"binary,omitempty"`
	Model         string `json:"model,omitempty"`
	Effort        string `json:"reasoning_effort,omitempty"`
	ModelVerified bool   `json:"model_verified"`
}

// Location places a Finding in the code. Blob is the hash of the file
// content at the moment of the finding: it lets one detect whether the file
// changed since then without depending on the line still meaning the same
// thing.
type Location struct {
	File      string `json:"file"`
	Blob      string `json:"blob,omitempty"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end,omitempty"`
	Simbolo   string `json:"symbol,omitempty"`
}

// Finding is finding v2: it coexists with ReviewFinding (v1) without
// replacing it. It adds provenance (Source/Producer), certainty
// (Confidence), lifecycle (Status) and literal evidence so a false positive
// can be refuted mechanically without re-reading the code by hand.
//
// internal/validation.Hallazgo (F1) has a similar shape (Source/Severity/
// Evidence) but was born in another package for another purpose: the result
// of a deterministic command (lint/test/build), not of an LLM agent. This
// struct deliberately does NOT import nor depend on internal/validation:
// they are analogous concepts, not the same type, and unifying them (if it
// is ever needed) is the job of a future phase (F6, aggregator, per the
// reengineering README). A Finding with Source=SourceValidation would be
// filled with Confidence: 1.0 (full certainty: it is a real exit code, not
// a semantic inference) and a Producer describing the executed command
// (Binary/Agent) instead of an LLM model (Model/Effort/ModelVerified
// would stay empty).
type Finding struct {
	ID                  string              `json:"id"`
	Source              string              `json:"source"`
	Producer            Producer            `json:"producer"`
	Dimension           string              `json:"dimension"`
	Severity            string              `json:"severity"`
	Confidence          float64             `json:"confidence"`
	Status              string              `json:"status"`
	Title               string              `json:"title"`
	Description         string              `json:"description"`
	Location            Location            `json:"location"`
	Evidence            string              `json:"evidence"`
	EvidenceSet         *FindingEvidenceSet `json:"evidence_set,omitempty"`
	Impact              string              `json:"impact,omitempty"`
	Recommendation      string              `json:"recommendation,omitempty"`
	Fixable             string              `json:"fixable"`
	IntroducedBy        string              `json:"introduced_by,omitempty"`
	Fingerprint         string              `json:"fingerprint"`
	RefutationReason    string              `json:"refutation_reason,omitempty"`
	RefutationEvidence  string              `json:"refutation_evidence,omitempty"`
	RefutationLineStart int                 `json:"refutation_line_start,omitempty"`
	RefutationLineEnd   int                 `json:"refutation_line_end,omitempty"`
	RefutationRangeHash string              `json:"refutation_range_hash,omitempty"`
	// RefutationActor records who issued the refutation: RefutationActorRefuter
	// for the automated refuter, RefutationActorHuman for a person answering
	// through the refute command. Empty on records written before FU-6, which
	// only the automated path could produce. Deliberately excluded from
	// Fingerprint like InvocationID: the answer does not change the defect.
	RefutationActor string `json:"refutation_actor,omitempty"`
	// InvocationID is the durable invocation provenance bound at finalization
	// when the producing transport reports one (ticket 07 slice 2b). Empty on
	// the legacy direct path. Deliberately excluded from Fingerprint: two
	// findings identical in content must keep byte-identical fingerprints no
	// matter which invocation produced them.
	InvocationID string `json:"invocation_id,omitempty"`
}

// FindingEvidence records the source evidence preserved during aggregation.
type FindingEvidence struct {
	Dimension  string   `json:"dimension"`
	Producer   Producer `json:"producer"`
	Evidence   string   `json:"evidence"`
	Confidence float64  `json:"confidence"`
}

// FindingEvidenceSet holds the evidence retained by an aggregated finding.
// A pointer keeps Finding comparable for existing consumers.
type FindingEvidenceSet struct {
	Values []FindingEvidence `json:"values"`
}

// Reasons for dismissing a Finding during evidence validation.
const (
	ReasonNoEvidence       = "no evidence: the Evidence field is empty"
	ReasonFileUnresolved   = "could not resolve the content of the cited file"
	ReasonEvidenceNotFound = "the evidence does not appear literally in the file content"
)

// Dismissal records why a Finding was dismissed during evidence validation.
// Dismissing a finding never invalidates the whole execution (T2.2): the
// reason travels with the finding so there is traceability of what was lost
// and why, without aborting the validation of the rest.
type Dismissal struct {
	Finding Finding
	Reason  string
}

// FindingsWithValidEvidence filters the Findings (finding v2) whose Evidence
// cannot be checked mechanically against the real content of the file they
// cite: it is the cheapest filter against LLM hallucinations, without
// needing another model to judge it.
//
// read is a deliberate injection seam: internal/review does NOT import
// internal/git so the audit domain model stays decoupled from the concrete
// git reading implementation. The caller passes a closure (e.g. over
// git.FileContentAtCommit) bound to the commit being audited.
//
// A finding is dismissed if Evidence is empty, if Location.File is empty or
// its content cannot be resolved, or if Evidence (normalized) does not
// appear literally in the (normalized) file content. Dismissing a finding
// never stops the validation of the others nor propagates the read error
// upward: of N findings, if one is invalid, the other N-1 survive intact.
func FindingsWithValidEvidence(findings []Finding, read func(file string) (string, error)) ([]Finding, []Dismissal) {
	valid := make([]Finding, 0, len(findings))
	var dismissals []Dismissal
	for _, h := range findings {
		if reason := discardEvidenceReason(h, read); reason != "" {
			dismissals = append(dismissals, Dismissal{Finding: h, Reason: reason})
			continue
		}
		valid = append(valid, h)
	}
	return valid, dismissals
}

// discardEvidenceReason returns the reason a Finding would be dismissed
// for, or "" if its evidence is valid.
func discardEvidenceReason(h Finding, read func(file string) (string, error)) string {
	if strings.TrimSpace(h.Evidence) == "" {
		return ReasonNoEvidence
	}
	if strings.TrimSpace(h.Location.File) == "" {
		return ReasonFileUnresolved
	}
	content, err := read(h.Location.File)
	if err != nil {
		return ReasonFileUnresolved
	}
	if !containsNormalizedEvidence(content, h.Evidence) {
		return ReasonEvidenceNotFound
	}
	return ""
}

// normalizeForComparison trims leading/trailing whitespace of every line and
// unifies line endings (\r\n -> \n), only for evidence comparison: it
// tolerates an LLM reindenting the code it cites without tolerating a vague
// similarity (it touches nothing else: not comments, not intermediate
// indentation).
func normalizeForComparison(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.Join(lines, "\n")
}

// containsNormalizedEvidence reports whether the recorded evidence still
// appears in the file content under the same normalization the fingerprint
// uses. The search spans the whole file, so a moved line still matches while
// removed evidence does not. Single home for the containment check shared by
// the ledger stale-evidence discard and the net carry-over revalidation.
func containsNormalizedEvidence(content, evidence string) bool {
	return strings.Contains(normalizeForComparison(content), normalizeForComparison(evidence))
}

// Fingerprint computes a stable fingerprint of a Finding (finding v2) so
// "the same defect" can be tracked between two revisions even when the file
// was reindented or renumbered by an edit elsewhere: it does not use
// Location.LineStart/LineEnd (line numbers are the least stable datum of a
// finding: they change with any earlier edit in the file) nor Location.Blob
// (it identifies an exact version of the file, not the defect itself, which
// can survive several commits without being touched).
//
// Components, in this order:
//  1. Dimension: the category of the finding (logic, security, ...).
//  2. Location.Simbolo when available (more precise: it survives the symbol
//     moving to another line or even another file); when the agent did not
//     resolve it, fall back to Location.File so findings from different
//     files do not collide under an empty key.
//  3. Evidence normalized with normalizeForComparison (T2.2): the same
//     normalization that already tolerates reindentation when validating
//     evidence against the file content; here it serves exactly the same
//     purpose, tolerating reindentation without duplicating the comparison
//     logic.
//  4. Title as the "rule": Description and Impact narrate concrete details
//     of the instance (which line, which value, which observed consequence),
//     which vary even when the TYPE of defect is the same; Title is the
//     label the agent itself uses to name the kind of problem (e.g.
//     "always-true condition") and repeats identically across instances of
//     the same defect, which is exactly what the "rule" property needs.
func Fingerprint(h Finding) string {
	symbolOrPath := h.Location.Simbolo
	if symbolOrPath == "" {
		symbolOrPath = h.Location.File
	}
	input := packWithLengthPrefixes(
		h.Dimension,
		symbolOrPath,
		normalizeForComparison(h.Evidence),
		h.Title,
	)
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// packWithLengthPrefixes concatenates components, prefixing each with its
// decimal length and ":". A simple separator like "|" would be ambiguous if
// any component contained it literally (e.g. evidence with a "|" inside a
// logic expression); prefixing with the length makes the concatenation
// unambiguous no matter which characters each component carries.
func packWithLengthPrefixes(components ...string) string {
	var b strings.Builder
	for _, c := range components {
		b.WriteString(strconv.Itoa(len(c)))
		b.WriteString(":")
		b.WriteString(c)
	}
	return b.String()
}

// AgentQuestion is a clarification the agent needs in order to audit.
type AgentQuestion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// File is the path the question refers to, when the agent can attribute
	// it to one file.
	File string `json:"file,omitempty"`
}

// DimensionResult is the agent's verdict for one concrete dimension.
// Warnings collects normalizations applied during parsing (e.g. an unknown
// severity downgraded to ADVISORY) and is not serialized into the record.
//
// Findings carries the agent's findings projected onto the shared v1 shape
// (ReviewFinding). The engine projects them once more into the durable v2
// Finding shape (lifecycle, provenance, aggregated evidence); that single
// projection belongs to the engine, never to a second parallel parse output.
type DimensionResult struct {
	Bundle   string          `json:"bundle,omitempty"`
	Dim      string          `json:"dim"`
	Verdict  string          `json:"verdict"`
	Findings []ReviewFinding `json:"findings,omitempty"`
	// V2Findings parallels Findings, not a substitute: each raw finding
	// carrying at least one v2-exclusive field is decoded AS WELL as a
	// full Finding and added here, while still appearing in Findings (v1).
	// A finding with v1 fields only leaves V2Findings empty: parsing v2
	// never depends on the prompt emitting it yet.
	V2Findings      []Finding       `json:"v2_findings,omitempty"`
	Questions       []AgentQuestion `json:"questions,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	RefutedCritical bool            `json:"refuted_critical,omitempty"`
	Warnings        []string        `json:"-"`
	// InvocationID identifies the durable invocation that produced this
	// result, when the audit routed through a transport that reports one
	// (ticket 07 slice 2b). Empty for legacy direct audits; additive
	// provenance metadata only, never a fingerprint input.
	InvocationID string `json:"invocation_id,omitempty"`
	// RawProviderOutput is retained only in memory for diagnosis and evidence;
	// persisted review results continue to omit untrusted raw provider output.
	RawProviderOutput string `json:"-"`
	// ExecutionFailure preserves a typed provider failure separately from a
	// deterministic semantic-output error.
	ExecutionFailure *ProviderExecutionFailure `json:"-"`
}

// rawLine decodes the "line" field of one raw finding tolerantly (FU-19).
// A JSON number and a numeric string keep parsing to Line exactly as before
// (same trimming Line.UnmarshalJSON applies); ANY other JSON value — range
// text ("17-19, 23-48"), a word, an array — is not an error: it leaves
// the line at 0, the unknown-line convention deterministic file-scoped
// findings already use, and keeps the raw text so aReviewFinding can persist
// it on ReviewFinding.LineRaw. JSON null never reaches UnmarshalJSON for an
// addressable struct field, so it behaves as an absent line (zero value) —
// already the unknown-line result, with nothing raw to preserve.
//
// Decision (FU-19): NO range semantics. The persisted shape holds a single
// Line int, so inventing first-line precision from "17-19, 23-48" would
// misdirect the evidence window while pretending the line is known; unknown
// (0) is already answerable through the file-scoped refutation gate (FU-6
// defect 2). Strictness stays on Line itself: the persisted shape still
// fails loudly on a corrupted line instead of silently zeroing it — only the
// agent-input path is tolerant.
type rawLine struct {
	value Line
	raw   string // raw JSON text, set only when the value was not numeric
}

func (l *rawLine) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			l.value = Line(n)
			return nil
		}
		l.value = 0
		l.raw = s
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		l.value = Line(n)
		return nil
	}
	l.value = 0
	l.raw = string(b)
	return nil
}

// rawFinding decodes one element of the "findings" array of a JSONL line
// (or of the multiline object) accepting at once the v1 fields
// (ReviewFinding, always present today) and the fields exclusive to v2
// (Finding, F5). The v1 fields share the JSON key with their Finding
// counterpart where one exists (severity/description): there are not two
// fields for the same thing, only two destination structs fed from the same
// datum, decoded exactly once.
//
// The fields exclusive to v2 are pointers on purpose: nil distinguishes
// "the agent did not send this field" from "it sent it with its zero value"
// (e.g. confidence: 0), which is exactly the signal that lets v2-only
// fields coexist with the v1 shape without requiring the agent to send all
// the v2 fields at once.
type rawFinding struct {
	Dimension   string  `json:"dimension"`
	File        string  `json:"file"`
	Line        rawLine `json:"line"`
	Severity    string  `json:"severity"`
	Description string  `json:"description"`
	Suggestion  string  `json:"suggestion"`

	ID             *string          `json:"id"`
	Source         *string          `json:"source"`
	Producer       *Producer        `json:"producer"`
	Confidence     *confidenceScore `json:"confidence"`
	Status         *string          `json:"status"`
	Title          *string          `json:"title"`
	Evidence       *string          `json:"evidence"`
	Location       *Location        `json:"location"`
	Impact         *string          `json:"impact"`
	Recommendation *string          `json:"recommendation"`
	Fixable        *string          `json:"fixable"`
	IntroducedBy   *string          `json:"introduced_by"`
}

// confidenceScore parses a finding's "confidence" field. The review prompt
// (T5.6) asks the model for a category ("high", "medium", "low") rather than
// a raw float, since an LLM has no reliable basis for a precise numeric
// estimate; this maps that category onto the float64 scale the rest of the
// v2 contract already uses (F2: Confidence 1.0 == full certainty). A bare
// number is still accepted as-is for callers that already emit one (e.g. the
// validation-sourced findings from F2, which use 1.0 directly).
type confidenceScore float64

const (
	confidenceHigh   confidenceScore = 0.9
	confidenceMedium confidenceScore = 0.6
	confidenceLow    confidenceScore = 0.3
)

func (c *confidenceScore) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "high":
			*c = confidenceHigh
		case "medium":
			*c = confidenceMedium
		case "low":
			*c = confidenceLow
		default:
			return fmt.Errorf("unknown confidence level %q", s)
		}
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*c = confidenceScore(n)
	return nil
}

// aReviewFinding projects the raw finding's v1 fields, ignoring the ones
// exclusive to v2: it is the same ReviewFinding built before T2.4.
func (f rawFinding) aReviewFinding() ReviewFinding {
	return ReviewFinding{
		Dimension:   f.Dimension,
		File:        f.File,
		Line:        f.Line.value,
		LineRaw:     f.Line.raw,
		Severity:    f.Severity,
		Description: f.Description,
		Suggestion:  f.Suggestion,
	}
}

// isV2 reports whether the raw finding carries at least one v2-exclusive
// field: only then is it decoded as well as a full Finding. A finding with
// v1 fields only leaves the v2 parallel list empty.
func (f rawFinding) isV2() bool {
	return f.ID != nil || f.Source != nil || f.Producer != nil || f.Confidence != nil ||
		f.Status != nil || f.Title != nil || f.Evidence != nil || f.Location != nil ||
		f.Impact != nil || f.Recommendation != nil || f.Fixable != nil || f.IntroducedBy != nil
}

// aFinding projects the raw finding's full v2 shape, falling back to the
// v1 file/line when no explicit location object (or an empty one) came in:
// an empty explicit location would otherwise collide fingerprints across
// files, which the v1 fallback exists precisely to avoid.
func (f rawFinding) aFinding(lineDim string) Finding {
	location := Location{File: f.File, LineStart: int(f.Line.value)}
	// Guard on emptiness, not just nil: an explicit "location": {} in the
	// JSON yields a non-nil pointer with neither File nor Simbolo.
	if f.Location != nil && (f.Location.File != "" || f.Location.Simbolo != "") {
		location = *f.Location
	}
	h := Finding{
		Dimension:   lineDim,
		Severity:    f.Severity,
		Description: f.Description,
		Location:    location,
	}
	if f.Dimension != "" {
		h.Dimension = f.Dimension
	}
	if f.ID != nil {
		h.ID = *f.ID
	}
	if f.Source != nil {
		h.Source = *f.Source
	}
	if f.Producer != nil {
		h.Producer = *f.Producer
	}
	if f.Confidence != nil {
		h.Confidence = float64(*f.Confidence)
	}
	if f.Status != nil {
		h.Status = *f.Status
	}
	if f.Title != nil {
		h.Title = *f.Title
	}
	if f.Evidence != nil {
		h.Evidence = *f.Evidence
	}
	if f.Impact != nil {
		h.Impact = *f.Impact
	}
	if f.Recommendation != nil {
		h.Recommendation = *f.Recommendation
	}
	if f.Fixable != nil {
		h.Fixable = *f.Fixable
	}
	if f.IntroducedBy != nil {
		h.IntroducedBy = *f.IntroducedBy
	}
	h.Fingerprint = Fingerprint(h)
	return h
}

// processFindings converts the raw findings of one line/object into their
// v1 shape (ReviewFinding, compatibility) and v2 shape (Finding, only for
// the ones carrying some exclusive field). It normalizes severity ONCE per
// finding with the same criterion v1 always applied (T2.4: no second
// normalization criterion), and the normalized severity is what both the
// ReviewFinding and the Finding see.
func processFindings(raw []rawFinding, lineDim string, normalizations *[]string) ([]ReviewFinding, []Finding) {
	findingsV1 := make([]ReviewFinding, 0, len(raw))
	var findingsV2 []Finding
	for _, f := range raw {
		if f.Line.raw != "" {
			*normalizations = append(*normalizations,
				fmt.Sprintf("line %q in %s normalized to unknown line", f.Line.raw, f.File))
		}
		if f.Severity != SevCritical && f.Severity != SevWarning && f.Severity != SevAdvisory {
			*normalizations = append(*normalizations,
				fmt.Sprintf("severity %q in %s:%d normalized to ADVISORY", f.Severity, f.File, int(f.Line.value)))
			f.Severity = SevAdvisory
		}
		// The finding inherits the line's dimension unless it declares its
		// own: the dimension comes from the containing result, never from
		// the finding alone (pre-T2.4 convention, kept by findingWithDisposition).
		if f.Dimension == "" {
			f.Dimension = lineDim
		}
		findingsV1 = append(findingsV1, f.aReviewFinding())
		if f.isV2() {
			findingsV2 = append(findingsV2, f.aFinding(lineDim))
		}
	}
	return findingsV1, findingsV2
}

// Typed parse errors, so the caller decides the degradation
// (unavailable/block) without guessing.
var (
	ErrEmptyOutput       = errors.New("the agent returned an empty output")
	ErrInvalidJSONL      = errors.New("no JSONL line with a valid dimension")
	ErrInvalidDimension  = errors.New("unknown dimension")
	ErrDimensionMismatch = errors.New("review result dimension does not match the requested contract")
	ErrInvalidVerdict    = errors.New("unknown verdict")
)

// SemanticOutputClass identifies deterministic failures in a provider's
// returned payload. Provider execution failures do not use this type.
type SemanticOutputClass string

const (
	SemanticOutputMissingPayload SemanticOutputClass = "missing_semantic_payload"
	SemanticOutputToolDenied     SemanticOutputClass = "tool_denied"
	SemanticOutputMalformedJSON  SemanticOutputClass = "malformed_json"
	SemanticOutputSchemaInvalid  SemanticOutputClass = "schema_invalid"
)

const maxSemanticOutputEvidenceRunes = 240

var semanticOutputSecret = regexp.MustCompile(`(?i)\b(api[_ -]?key|authorization|token|password|secret)\b\s*[:=]\s*(?:bearer\s+)?[^\s,;]+`)

// semanticOutputQuotedSecret covers JSON-shaped secrets the bare pattern
// misses, where a quote separates the key from its value
// ({"token":"SECRET"}). Live review evidence showed exactly this form.
var semanticOutputQuotedSecret = regexp.MustCompile(`(?i)"(api[_ -]?key|authorization|token|password|secret)"\s*:\s*"[^"]*"`)

// SemanticOutputError preserves a bounded, redacted excerpt for deterministic
// provider output failures while retaining the legacy sentinel through Unwrap.
type SemanticOutputError struct {
	Class    SemanticOutputClass
	Evidence string
	legacy   error
}

func (e *SemanticOutputError) Error() string {
	if e.Evidence == "" {
		return fmt.Sprintf("semantic review output %s", e.Class)
	}
	return fmt.Sprintf("semantic review output %s: %s", e.Class, e.Evidence)
}

func (e *SemanticOutputError) Unwrap() error { return e.legacy }

func newSemanticOutputError(class SemanticOutputClass, legacy error, output string) error {
	return &SemanticOutputError{Class: class, Evidence: semanticOutputEvidence(output), legacy: legacy}
}

func semanticOutputEvidence(output string) string {
	evidence := strings.Join(strings.Fields(output), " ")
	evidence = semanticOutputSecret.ReplaceAllString(evidence, "$1=[REDACTED]")
	evidence = semanticOutputQuotedSecret.ReplaceAllString(evidence, `"$1":"[REDACTED]"`)
	runes := []rune(evidence)
	if len(runes) <= maxSemanticOutputEvidenceRunes {
		return evidence
	}
	return string(runes[:maxSemanticOutputEvidenceRunes])
}

func classifyUnparseableSemanticOutput(output string) error {
	trimmed := strings.TrimSpace(output)
	legacy := ErrInvalidJSONL
	switch {
	case semanticOutputLooksToolDenied(trimmed):
		return newSemanticOutputError(SemanticOutputToolDenied, legacy, output)
	case json.Valid([]byte(trimmed)):
		return newSemanticOutputError(SemanticOutputSchemaInvalid, legacy, output)
	case strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "["):
		return newSemanticOutputError(SemanticOutputMalformedJSON, legacy, output)
	default:
		return newSemanticOutputError(SemanticOutputMissingPayload, legacy, output)
	}
}

func semanticOutputLooksToolDenied(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "access denied") ||
		strings.Contains(lower, "not allowed") ||
		strings.Contains(lower, "not permitted") ||
		(strings.Contains(lower, "tool") && strings.Contains(lower, "denied"))
}

// ParseDimensionResult extracts one dimension result from raw provider output.
// It accepts surrounding text and markdown fences, locating a BEGIN_REVIEW /
// END_REVIEW block when present and otherwise using the complete output. Lines
// that are not valid JSONL are discarded; the first known "dim" and "verdict"
// wins. An unknown dimension always returns an explicit error.
func ParseDimensionResult(output string) (*DimensionResult, error) {
	contract, err := reviewcontract.Lookup(DimLogic)
	if err != nil {
		panic(fmt.Sprintf("canonical review contract unavailable: %v", err))
	}
	return parseDimensionResult(output, "", contract.OutputSchema, reviewcontract.EvidencePolicy{})
}

// ParseDimensionResultForContract parses a provider answer against the
// schema selected for one requested contract. A valid result for any other
// canonical dimension is a deterministic schema failure.
func ParseDimensionResultForContract(output string, contract reviewcontract.DimensionContract) (*DimensionResult, error) {
	return parseDimensionResult(output, contract.Name, contract.OutputSchema, contract.EvidencePolicy)
}

func parseDimensionResult(output, expectedDimension string, schema reviewcontract.OutputSchema, evidencePolicy reviewcontract.EvidencePolicy) (*DimensionResult, error) {
	block := extractJSONLBlockWithSchema(output, schema)
	lines := strings.Split(block, "\n")

	var discarded int
	var normalizations []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var parsed struct {
			Dim       string          `json:"dim"`
			Verdict   string          `json:"verdict"`
			Findings  []rawFinding    `json:"findings"`
			Questions []AgentQuestion `json:"questions"`
			Reason    string          `json:"reason"`
		}
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			discarded++
			continue
		}
		if parsed.Dim == "" {
			discarded++
			continue
		}
		if _, err := reviewcontract.Lookup(parsed.Dim); err != nil {
			return nil, newSemanticOutputError(SemanticOutputSchemaInvalid, fmt.Errorf("%w: %q", ErrInvalidDimension, parsed.Dim), block)
		}
		if !validVerdicts[parsed.Verdict] {
			if len(parsed.Findings) == 0 {
				return nil, newSemanticOutputError(SemanticOutputSchemaInvalid, fmt.Errorf("%w: %q (dimension %q)", ErrInvalidVerdict, parsed.Verdict, parsed.Dim), block)
			}
			// De facto verdicts ("issues", "error", ...) with findings are
			// derived from the severities instead of aborting the audit.
			normalizations = append(normalizations,
				fmt.Sprintf("verdict %q normalized from the finding severities", parsed.Verdict))
			parsed.Verdict = ""
		}
		if err := validateFindingsAgainstContract(parsed.Findings, evidencePolicy); err != nil {
			return nil, newSemanticOutputError(SemanticOutputSchemaInvalid, err, block)
		}

		findings, findingsV2 := processFindings(parsed.Findings, parsed.Dim, &normalizations)

		if discarded > 0 {
			normalizations = append(normalizations, fmt.Sprintf("%d non-JSONL lines discarded", discarded))
		}
		result := &DimensionResult{
			Dim:        parsed.Dim,
			Verdict:    finalVerdict(parsed.Verdict, findings, &normalizations),
			Findings:   findings,
			V2Findings: findingsV2,
			Questions:  parsed.Questions,
			Reason:     parsed.Reason,
			Warnings:   normalizations,
		}
		return validateContractDimension(result, expectedDimension, block)
	}

	if strings.TrimSpace(block) == "" {
		return nil, newSemanticOutputError(SemanticOutputMissingPayload, ErrEmptyOutput, block)
	}
	// Fallback: some models (e.g. the cheap profile with reasoning low) emit
	// the JSON object pretty-printed across several lines inside the block.
	// No single line is valid JSONL, but the whole block is one JSON object;
	// parsing it whole keeps a valid audit from degrading to unavailable.
	if res, err, ok := parseMultilineObject(block, evidencePolicy); ok {
		if err != nil {
			return nil, err
		}
		return validateContractDimension(res, expectedDimension, block)
	}
	return nil, classifyUnparseableSemanticOutput(block)
}
func validateFindingsAgainstContract(findings []rawFinding, policy reviewcontract.EvidencePolicy) error {
	if !policy.RequireLiteralEvidence && !policy.RequireConfidence {
		return nil
	}
	for i, finding := range findings {
		if policy.RequireLiteralEvidence && (finding.Evidence == nil || strings.TrimSpace(*finding.Evidence) == "") {
			return fmt.Errorf("finding %d: literal evidence is required by the semantic review contract", i+1)
		}
		if policy.RequireConfidence {
			if finding.Confidence == nil {
				return fmt.Errorf("finding %d: categorical or numeric confidence is required by the semantic review contract", i+1)
			}
			if *finding.Confidence < 0 || *finding.Confidence > 1 {
				return fmt.Errorf("finding %d: numeric confidence must be between 0 and 1", i+1)
			}
		}
	}
	return nil
}

func validateContractDimension(result *DimensionResult, expectedDimension, output string) (*DimensionResult, error) {
	if expectedDimension == "" || result.Dim == expectedDimension {
		return result, nil
	}
	return nil, newSemanticOutputError(SemanticOutputSchemaInvalid, fmt.Errorf("%w: requested %q, received %q", ErrDimensionMismatch, expectedDimension, result.Dim), output)
}

// parseMultilineObject attempts to decode the block as one JSON object,
// accepting pretty-printed multiline output. It reports ok only when complete
// parsing succeeds and the dimension is canonical.
func parseMultilineObject(block string, evidencePolicy reviewcontract.EvidencePolicy) (*DimensionResult, error, bool) {
	var parsed struct {
		Dim       string          `json:"dim"`
		Verdict   string          `json:"verdict"`
		Findings  []rawFinding    `json:"findings"`
		Questions []AgentQuestion `json:"questions"`
		Reason    string          `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(block)), &parsed); err != nil {
		return nil, nil, false
	}
	if parsed.Dim == "" {
		return nil, nil, false
	}
	if _, err := reviewcontract.Lookup(parsed.Dim); err != nil {
		return nil, nil, false
	}

	var normalizations []string
	verdict := parsed.Verdict
	if !validVerdicts[verdict] {
		if len(parsed.Findings) == 0 {
			return nil, nil, false
		}
		normalizations = append(normalizations,
			fmt.Sprintf("verdict %q normalized from the finding severities", verdict))
		verdict = ""
	}
	if err := validateFindingsAgainstContract(parsed.Findings, evidencePolicy); err != nil {
		return nil, newSemanticOutputError(SemanticOutputSchemaInvalid, err, block), true
	}
	findings, findingsV2 := processFindings(parsed.Findings, parsed.Dim, &normalizations)

	return &DimensionResult{
		Dim:        parsed.Dim,
		Verdict:    finalVerdict(verdict, findings, &normalizations),
		Findings:   findings,
		V2Findings: findingsV2,
		Questions:  parsed.Questions,
		Reason:     parsed.Reason,
		Warnings:   normalizations,
	}, nil, true
}

// finalVerdict decides a dimension's verdict after parsing: the findings
// override the declared verdict. A declared ok with CRITICAL goes up to
// block; a de facto verdict (derived from severities) is resolved here.
// question and unavailable are respected as-is.
func finalVerdict(declared string, findings []ReviewFinding, normalizations *[]string) string {
	if declared == VerdictQuestion || declared == VerdictUnavailable {
		return declared
	}

	derived := verdictFromSeverities(findings)
	if declared == "" {
		return derived
	}
	if derived != declared && derived != VerdictOK {
		*normalizations = append(*normalizations,
			fmt.Sprintf("verdict %q raised to %q by finding severities", declared, derived))
		return derived
	}
	return declared
}

// verdictFromSeverities maps findings to a verdict: CRITICAL -> block,
// WARNING/ADVISORY -> warn, no findings -> ok.
func verdictFromSeverities(findings []ReviewFinding) string {
	worst := VerdictOK
	for _, h := range findings {
		switch h.Severity {
		case SevCritical:
			return VerdictBlock
		case SevWarning, SevAdvisory:
			worst = VerdictWarn
		}
	}
	return worst
}

// extractJSONLBlock trims output to the segment between BEGIN_REVIEW and
// END_REVIEW when present; otherwise it returns the complete output.
func extractJSONLBlock(output string) string {
	contract, err := reviewcontract.Lookup(DimLogic)
	if err != nil {
		panic(fmt.Sprintf("canonical review contract unavailable: %v", err))
	}
	return extractJSONLBlockWithSchema(output, contract.OutputSchema)
}

func extractJSONLBlockWithSchema(output string, schema reviewcontract.OutputSchema) string {
	begin := strings.Index(output, schema.BeginDelimiter)
	if begin < 0 {
		return output
	}
	begin += len(schema.BeginDelimiter)
	end := strings.Index(output[begin:], schema.EndDelimiter)
	if end < 0 {
		return output[begin:]
	}
	return output[begin : begin+end]
}
