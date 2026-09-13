package pr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

var (
	errStoredReviewInvalid = errors.New("stored pr review is semantically invalid")
)

var requiredPipelineSteps = []string{"slice", "review", "gate", "lint", "test", "build", "pr review", "ci"}

// CIOutcome is the deterministic result collected by pr create. It contains
// only bounded, reader-facing data; no semantic review text is authored here.
type CIOutcome struct {
	Status     string
	Icon       string
	Summary    string
	URL        string
	FailedJobs []string
}

// DefaultCIOutcome preserves Piece 4's placeholder when no workflow is
// explicitly configured. Detecting CI files is intentionally not enough to
// enable remote work.
func DefaultCIOutcome() CIOutcome {
	return CIOutcome{
		Status:  "not_observed",
		Icon:    "⚪",
		Summary: "not observed by Sentinel",
	}
}

// ValidatePRReviewEntry checks the stored Piece 4 artifact without deriving any
// new judgement. The body attestation and the raw persisted attestation must
// agree with each other and with the current branch snapshot.
func ValidatePRReviewEntry(entry *store.PRReviewEntry, branch, head string) (review.Attestation, error) {
	if entry == nil {
		return review.Attestation{}, fmt.Errorf("%w: missing entry", errStoredReviewInvalid)
	}
	if strings.TrimSpace(entry.Branch) == "" || entry.Branch != branch {
		return review.Attestation{}, fmt.Errorf("%w: branch does not match the current branch", errStoredReviewInvalid)
	}
	if !store.IsValidGitObjectID(entry.HeadSHA) || entry.HeadSHA != head {
		return review.Attestation{}, fmt.Errorf("%w: head does not match the current HEAD", errStoredReviewInvalid)
	}
	if strings.TrimSpace(entry.Title) == "" {
		return review.Attestation{}, fmt.Errorf("%w: title is empty", errStoredReviewInvalid)
	}
	if strings.TrimSpace(entry.Body) == "" || !utf8.ValidString(entry.Body) || len(entry.Body) > review.PRBodyLimit {
		return review.Attestation{}, fmt.Errorf("%w: body is empty, invalid UTF-8, or exceeds %d bytes", errStoredReviewInvalid, review.PRBodyLimit)
	}
	switch entry.Verdict {
	case review.VerdictOK, review.VerdictWarn, review.VerdictBlock:
	case review.VerdictQuestion, review.VerdictUnavailable:
		return review.Attestation{}, fmt.Errorf("%w: verdict %q is not terminal; re-run sentinel pr review", errStoredReviewInvalid, entry.Verdict)
	default:
		return review.Attestation{}, fmt.Errorf("%w: unknown verdict %q", errStoredReviewInvalid, entry.Verdict)
	}

	attestation, err := review.ParseAttestation(entry.Body)
	if err != nil {
		return review.Attestation{}, fmt.Errorf("%w: %v", errStoredReviewInvalid, err)
	}
	if attestation.Branch != branch || attestation.HeadSHA != head || attestation.Verdict != entry.Verdict {
		return review.Attestation{}, fmt.Errorf("%w: body attestation does not match the current entry", errStoredReviewInvalid)
	}
	if err := validatePipelineSteps(attestation.Steps); err != nil {
		return review.Attestation{}, err
	}

	var stored review.Attestation
	decoder := json.NewDecoder(bytes.NewReader(entry.Attestation))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return review.Attestation{}, fmt.Errorf("%w: persisted attestation: %v", errStoredReviewInvalid, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return review.Attestation{}, fmt.Errorf("%w: persisted attestation has trailing data", errStoredReviewInvalid)
	}
	if !attestationsEqual(stored, attestation) {
		return review.Attestation{}, fmt.Errorf("%w: persisted and body attestations differ", errStoredReviewInvalid)
	}
	return attestation, nil
}

func attestationsEqual(left, right review.Attestation) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func validatePipelineSteps(steps []review.AttestationStep) error {
	if len(steps) != len(requiredPipelineSteps) {
		return fmt.Errorf("%w: expected %d pipeline steps, got %d", errStoredReviewInvalid, len(requiredPipelineSteps), len(steps))
	}
	for i, step := range steps {
		if step.Step != requiredPipelineSteps[i] || strings.TrimSpace(step.Status) == "" {
			return fmt.Errorf("%w: invalid pipeline step at position %d", errStoredReviewInvalid, i+1)
		}
		if !validStepStatus(step.Step, step.Status) {
			return fmt.Errorf("%w: invalid status %q for pipeline step %q", errStoredReviewInvalid, step.Status, step.Step)
		}
	}
	return nil
}

func validStepStatus(step, status string) bool {
	allowed := map[string]map[string]bool{
		"slice":     {"passed": true, "not_observed": true},
		"review":    {"passed": true, "warning": true, "blocked": true, "question": true, "unavailable": true, "not_observed": true},
		"gate":      {"passed": true, "failed": true, "not_run": true},
		"lint":      {"passed": true, "failed": true, "not_configured": true},
		"test":      {"passed": true, "failed": true, "not_configured": true},
		"build":     {"passed": true, "failed": true, "not_configured": true},
		"pr review": {"authored": true},
		"ci":        {"passed": true, "failed": true, "warning": true, "pending": true, "not_observed": true},
	}
	return allowed[step][status]
}

// ComposePRBody replaces exactly the persisted CI details block and its
// attestation status. All non-CI bytes are retained verbatim. It does not call
// a renderer for any semantic section.
func ComposePRBody(entry store.PRReviewEntry, outcome CIOutcome) (string, error) {
	if _, err := ValidatePRReviewEntry(&entry, entry.Branch, entry.HeadSHA); err != nil {
		return "", err
	}
	if strings.TrimSpace(outcome.Status) == "" {
		return "", fmt.Errorf("compose pr body: missing CI status")
	}
	ciBlock, err := renderCIBlock(outcome)
	if err != nil {
		return "", err
	}
	if len(ciBlock) > review.CIStepReserveBytes {
		return "", fmt.Errorf("compose pr body: CI details exceed the %d-byte reserve", review.CIStepReserveBytes)
	}

	body, err := replaceCIDetails(entry.Body, ciBlock)
	if err != nil {
		return "", err
	}
	attestation, err := review.ParseAttestation(entry.Body)
	if err != nil {
		return "", err
	}
	updated := attestation
	for i := range updated.Steps {
		if updated.Steps[i].Step == "ci" {
			updated.Steps[i].Status = outcome.Status
		}
	}
	marker, err := review.RenderAttestation(updated)
	if err != nil {
		return "", err
	}
	body, err = replaceAttestationMarker(body, marker)
	if err != nil {
		return "", err
	}
	if len(body) > review.PRBodyLimit {
		return "", fmt.Errorf("compose pr body: %d bytes exceeds %d-byte limit", len(body), review.PRBodyLimit)
	}
	return body, nil
}

func renderCIBlock(outcome CIOutcome) (string, error) {
	summary := sanitizeAndBound(outcome.Summary, 280)
	if summary == "" {
		return "", errors.New("compose pr body: missing CI summary")
	}
	icon := sanitizeAndBound(outcome.Icon, 24)
	if icon == "" {
		return "", errors.New("compose pr body: missing CI icon")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<details><summary>%s <b>ci</b> — %s</summary>\n\n", icon, summary)
	if outcome.URL != "" {
		fmt.Fprintf(&b, "Run: %s\n", sanitizeAndBound(outcome.URL, 200))
	}
	jobs := boundedJobs(outcome.FailedJobs)
	if len(jobs) > 0 {
		b.WriteString("Failed jobs: ")
		b.WriteString(strings.Join(jobs, ", "))
		b.WriteByte('\n')
	}
	b.WriteString("</details>\n\n")
	return b.String(), nil
}

func boundedJobs(jobs []string) []string {
	const maxJobs = 5
	bounded := make([]string, 0, minInt(len(jobs), maxJobs))
	for _, job := range jobs {
		name := sanitizeAndBound(job, 70)
		if name != "" {
			bounded = append(bounded, name)
		}
		if len(bounded) == maxJobs {
			break
		}
	}
	if len(jobs) > maxJobs {
		bounded = append(bounded, fmt.Sprintf("… and %d more", len(jobs)-maxJobs))
	}
	return bounded
}

func boundedUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func sanitizeDisplay(value string) string {
	value = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(value)
	value = strings.ReplaceAll(value, "`", "'")
	return html.EscapeString(value)
}

func sanitizeAndBound(value string, maxBytes int) string {
	return boundedUTF8(sanitizeDisplay(value), maxBytes)
}

type textRange struct{ start, end int }

func replaceCIDetails(body, replacement string) (string, error) {
	pipelineStart := strings.Index(body, "## Pipeline\n")
	if pipelineStart < 0 || strings.Count(body, "## Pipeline\n") != 1 {
		return "", fmt.Errorf("%w: missing or duplicate Pipeline section", errStoredReviewInvalid)
	}
	section := body[pipelineStart:]
	ranges := make([]textRange, 0, 1)
	for offset := 0; ; {
		start := strings.Index(section[offset:], "<details>")
		if start < 0 {
			break
		}
		start += offset
		close := strings.Index(section[start:], "</details>")
		if close < 0 {
			return "", fmt.Errorf("%w: unterminated Pipeline details block", errStoredReviewInvalid)
		}
		end := start + close + len("</details>")
		if strings.HasPrefix(section[end:], "\n\n") {
			end += 2
		}
		block := section[start:end]
		if strings.Contains(block, "<b>ci</b>") {
			ranges = append(ranges, textRange{start: pipelineStart + start, end: pipelineStart + end})
		}
		offset = end
	}
	if len(ranges) != 1 {
		return "", fmt.Errorf("%w: expected exactly one CI details block, got %d", errStoredReviewInvalid, len(ranges))
	}
	r := ranges[0]
	return body[:r.start] + replacement + body[r.end:], nil
}

func replaceAttestationMarker(body, replacement string) (string, error) {
	const prefix = "<!-- vas-sentinel-attestation:"
	start := strings.Index(body, prefix)
	if start < 0 || strings.Count(body, prefix) != 1 {
		return "", fmt.Errorf("%w: missing or duplicate attestation marker", errStoredReviewInvalid)
	}
	end := strings.Index(body[start:], " -->")
	if end < 0 {
		return "", fmt.Errorf("%w: unterminated attestation marker", errStoredReviewInvalid)
	}
	end += start + len(" -->")
	return body[:start] + replacement + body[end:], nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
