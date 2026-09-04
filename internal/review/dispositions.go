package review

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Human refutation provenance. Automated refutations are issued by the
// SHA-bound restricted refuter inside the engine; human refutations are
// recorded through the refute command against the same evidence gate. Audits
// and metrics separate the two through RefutationActor, never through
// InvocationID alone: a human decision cites no durable invocation.
const (
	// RefutationActorHuman marks a refutation recorded by a person through
	// the refute command.
	RefutationActorHuman = "human"
	// RefutationActorRefuter marks a refutation issued by the automated
	// refuter during an audit.
	RefutationActorRefuter = "refuter"
	// DispositionSourceHuman is the source recorded on every append-only
	// human disposition.
	DispositionSourceHuman = "human"
)

// FindingDisposition is one append-only human answer to a finding,
// addressed by the reviewed SHA plus the finding's stable fingerprint. It
// lives outside the immutable review revisions: re-auditing a SHA appends a
// revision and never rewrites one, while dispositions accumulate in their own
// log and are overlaid onto effective findings by ApplyDispositions.
//
// TargetDimension/TargetLine/TargetDescription record the finding's v1
// identity at record time as audit metadata only. They never match: a
// disposition applies by unique SHA + fingerprint, and anything else fails
// closed.
type FindingDisposition struct {
	SHA               string    `json:"sha"`
	Fingerprint       string    `json:"fingerprint"`
	Status            string    `json:"status"`
	Reason            string    `json:"reason"`
	Path              string    `json:"path"`
	LineStart         int       `json:"line_start"`
	LineEnd           int       `json:"line_end"`
	Evidence          string    `json:"evidence"`
	RangeHash         string    `json:"range_hash"`
	Actor             string    `json:"actor"`
	Source            string    `json:"source"`
	At                time.Time `json:"at"`
	TargetDimension   string    `json:"target_dimension,omitempty"`
	TargetLine        int       `json:"target_line,omitempty"`
	TargetDescription string    `json:"target_description,omitempty"`
}

// IsBlocking is the single blocking rule shared by the review engine, the
// gate, and BloqueantesDeRama: every consumer must agree about the same
// record. Only a refuted or fixed CRITICAL finding stops blocking.
// AcceptedByUser records human judgement without clearing the block, and a
// reopened finding blocks again. An empty or unknown status blocks: legacy
// findings predate lifecycle tracking, and failing closed keeps them
// visible instead of silently dropping a defect nobody answered.
func IsBlocking(severity, status string) bool {
	if severity != SevCritical {
		return false
	}
	switch NormalizeStatus(status) {
	case StatusRefuted, StatusFixed:
		return false
	default:
		return true
	}
}

// EffectiveFingerprint returns the fingerprint a finding is addressed by:
// the stored one when the finding carries it, or the computed stable
// fingerprint otherwise.
func EffectiveFingerprint(h Hallazgo) string {
	if fp := strings.TrimSpace(h.Fingerprint); fp != "" {
		return fp
	}
	return Fingerprint(h)
}

// FilterDispositionsForSHA returns the dispositions recorded against one
// reviewed SHA, in log order.
func FilterDispositionsForSHA(dispositions []FindingDisposition, sha string) []FindingDisposition {
	var out []FindingDisposition
	for _, d := range dispositions {
		if d.SHA == sha {
			out = append(out, d)
		}
	}
	return out
}

// ApplyDispositions overlays append-only dispositions onto effective
// findings and returns the result, leaving the input untouched. It is the
// one domain projection that applies dispositions: the engine, the branch
// blockers, the gate evidence, and the metrics reader all observe human
// answers through it, so no consumer can disagree about the same record.
//
// Matching is by stable fingerprint first. A disposition whose fingerprint
// matches no finding, or matches several, is skipped: missing and ambiguous
// identities fail closed and the findings keep blocking. The last record
// wins per fingerprint; the log order is the authority.
//
// A human disposition cites no durable invocation, so applying one clears
// InvocationID: metrics must never attribute a human decision to an agent
// invocation. The finding's producer is preserved: who generated the
// finding is history, and the refutation actor records who answered it.
func ApplyDispositions(findings []Hallazgo, dispositions []FindingDisposition) []Hallazgo {
	out := make([]Hallazgo, len(findings))
	copy(out, findings)
	if len(dispositions) == 0 {
		return out
	}
	byFingerprint := make(map[string][]int, len(out))
	for i := range out {
		fp := EffectiveFingerprint(out[i])
		byFingerprint[fp] = append(byFingerprint[fp], i)
	}
	last := make(map[string]FindingDisposition, len(dispositions))
	for _, d := range dispositions {
		fp := strings.TrimSpace(d.Fingerprint)
		if fp == "" || NormalizeStatus(d.Status) == "" {
			continue
		}
		last[fp] = d
	}
	for fp, d := range last {
		targets := byFingerprint[fp]
		if len(targets) != 1 {
			continue
		}
		applyToHallazgo(&out[targets[0]], d)
	}
	return out
}

// applyToHallazgo stamps one disposition onto one finding.
func applyToHallazgo(h *Hallazgo, d FindingDisposition) {
	h.Status = NormalizeStatus(d.Status)
	h.RefutationReason = d.Reason
	h.RefutationEvidence = d.Evidence
	h.RefutationLineStart = d.LineStart
	h.RefutationLineEnd = d.LineEnd
	h.RefutationRangeHash = d.RangeHash
	actor := strings.TrimSpace(d.Actor)
	if actor == "" {
		actor = RefutationActorHuman
	}
	h.RefutationActor = actor
	h.InvocationID = ""
}

// applyToReviewFinding stamps one disposition onto one legacy v1 finding.
func applyToReviewFinding(f *ReviewFinding, d FindingDisposition) {
	f.Status = NormalizeStatus(d.Status)
	f.RefutationReason = d.Reason
	f.RefutationEvidence = d.Evidence
	f.RefutationLineStart = d.LineStart
	f.RefutationLineEnd = d.LineEnd
	f.RefutationRangeHash = d.RangeHash
	actor := strings.TrimSpace(d.Actor)
	if actor == "" {
		actor = RefutationActorHuman
	}
	f.RefutationActor = actor
}

// ApplyDispositionToResult overlays one disposition onto a fresh dimension
// result, across both finding shapes, and reports whether it newly cleared
// a blocking CRITICAL finding. Matching is by unique stable fingerprint
// only: a v2 finding whose effective fingerprint equals the recorded one is
// flipped, and its v1 counterpart (same dimension, file, line, and
// description, the key the automated refuter already uses) follows, so the
// engine verdict and the persisted revision agree. A fingerprint that
// matches nothing clears nothing: v1-only shapes carry no fingerprint input
// and are never disposed by heuristic, they fail closed.
func ApplyDispositionToResult(result *DimensionResult, disp FindingDisposition) bool {
	if result == nil {
		return false
	}
	fp := strings.TrimSpace(disp.Fingerprint)
	if fp == "" || NormalizeStatus(disp.Status) == "" {
		return false
	}
	cleared := false
	flipV1 := func(f *ReviewFinding) {
		was := IsBlocking(f.Severity, f.Status)
		applyToReviewFinding(f, disp)
		if was && !IsBlocking(f.Severity, f.Status) {
			cleared = true
		}
	}
	flipV2 := func(h *Hallazgo) {
		was := IsBlocking(h.Severity, h.Status)
		applyToHallazgo(h, disp)
		if was && !IsBlocking(h.Severity, h.Status) {
			cleared = true
		}
	}
	match := -1
	for i := range result.Hallazgos {
		if EffectiveFingerprint(result.Hallazgos[i]) != fp {
			continue
		}
		if match >= 0 {
			return false
		}
		match = i
	}
	if match < 0 {
		return false
	}
	h := &result.Hallazgos[match]
	flipV2(h)
	for j := range result.Findings {
		f := &result.Findings[j]
		if f.File == h.Location.Archivo && int(f.Line) == h.Location.LineaInicio && f.Description == h.Description {
			flipV1(f)
		}
	}
	return cleared
}

// ResolveDispositionTarget finds the single effective finding of one
// revision addressed by a stable fingerprint. A missing fingerprint and an
// ambiguous one are both errors: the caller must fail closed without
// persisting anything.
func ResolveDispositionTarget(revision Revision, fingerprint string) (Hallazgo, error) {
	return resolveDispositionTarget(revision.FindingsWithDispositions(), fingerprint)
}

// ResolveDispositionTargetWithDispositions resolves a fingerprint after
// applying the authoritative append-only human answers. It keeps target
// identity and the effective lifecycle in one projection, so a second human
// refutation cannot be appended after the first one already cleared it.
func ResolveDispositionTargetWithDispositions(revision Revision, fingerprint string, dispositions []FindingDisposition) (Hallazgo, error) {
	return resolveDispositionTarget(ApplyDispositions(revision.FindingsWithDispositions(), dispositions), fingerprint)
}

func resolveDispositionTarget(findings []Hallazgo, fingerprint string) (Hallazgo, error) {
	fp := strings.TrimSpace(fingerprint)
	if fp == "" {
		return Hallazgo{}, errors.New("the finding fingerprint is empty")
	}
	var matches []Hallazgo
	for _, h := range findings {
		if EffectiveFingerprint(h) == fp {
			matches = append(matches, h)
		}
	}
	switch len(matches) {
	case 0:
		return Hallazgo{}, fmt.Errorf("no finding with fingerprint %q in the reviewed revision", fp)
	case 1:
		return matches[0], nil
	default:
		return Hallazgo{}, fmt.Errorf("fingerprint %q matches %d findings, refusing the ambiguous identity", fp, len(matches))
	}
}

// ValidateHumanRefutationRange enforces the existing refutation gate for a
// human-issued answer: a non-empty reason and an evidence line range checked
// against the immutable snapshot with the exact bounds and range hashing the
// automated refuter satisfies. The evidence is read from the audited Git
// object, never supplied by the caller, so a human cannot smuggle text the
// commit does not contain. It returns the sanitized path, the exact snapshot
// extract, and its range hash.
func ValidateHumanRefutationRange(leer SnapshotReader, sha, findingFile string, findingLine int, reason, file string, lineStart, lineEnd int) (safePath, evidence, rangeHash string, err error) {
	if strings.TrimSpace(reason) == "" {
		return "", "", "", errors.New("the refutation reason is empty")
	}
	if leer == nil {
		leer = leerContenidoSnapshot
	}
	safe := RutasRevisionSeguras([]string{file})
	if len(safe) != 1 {
		return "", "", "", fmt.Errorf("the evidence path %q is unsafe", file)
	}
	content, err := leer(sha, safe[0])
	if err != nil {
		return "", "", "", fmt.Errorf("reading the audited snapshot: %w", err)
	}
	lines := strings.Split(content, "\n")
	if lineStart < 1 || lineEnd < lineStart || lineEnd > len(lines) {
		return "", "", "", fmt.Errorf("the evidence range %d-%d is outside the audited file", lineStart, lineEnd)
	}
	extract := strings.Join(lines[lineStart-1:lineEnd], "\n")
	respuesta := respuestaRefutador{
		Refuted: true, Reason: reason, SHA: sha, File: safe[0],
		LineStart: lineStart, LineEnd: lineEnd, Evidence: extract,
	}
	hash, ok := validarEvidenciaRefutacion(leer, sha, ReviewFinding{File: findingFile, Line: Linea(findingLine)}, respuesta)
	if !ok {
		return "", "", "", errors.New("the evidence range does not satisfy the refutation gate for this finding")
	}
	return safe[0], extract, hash, nil
}
