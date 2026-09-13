package review

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// columnOrder returns the canonical column order of the commit × dimension
// matrix. It is a function (not a package-level slice) so no caller can
// mutate the matrix's global order.
func columnOrder() []string {
	return []string{DimLogic, DimStyle, DimDesign, DimTests, DimSecurity, DimSpec}
}

// verdictEmoji maps a dimension verdict (or a revision result) to its table
// icon. The current (last authoritative) revision of each record decides.
func verdictEmoji(verdict string) string {
	switch verdict {
	case VerdictOK:
		return "✅"
	case VerdictWarn:
		return "⚠️"
	case VerdictBlock:
		return "🚨"
	case VerdictQuestion:
		return "❓"
	case VerdictUnavailable:
		return "⛔"
	}
	return "❔"
}

// recoveredFromBlock reports whether the record's current verdict came out ok
// while some earlier AUTHORITATIVE revision was in block (re-audit after a
// fix). Only authoritative revisions carry the commit's verdict, so a
// supplementary block never counts and a supplementary OK never clears one.
func recoveredFromBlock(record Record) bool {
	last, idx, ok := LastAuthoritativeRevision(record)
	if !ok || last.Result != VerdictOK {
		return false
	}
	for i := 0; i < idx; i++ {
		rev := record.Revisions[i]
		if rev.IsAuthoritative() && rev.Result == VerdictBlock {
			return true
		}
	}
	return false
}

// matrixCell builds a dimension's cell for the current verdict revision: the
// verdict emoji and, when that revision cleared a previous authoritative block,
// the guide marker "spec ✅ (rev N — CRITICAL cleared)". A supplementary-only
// record has no authoritative verdict, so every cell renders "—".
func matrixCell(record Record, dim string) string {
	last, idx, ok := LastAuthoritativeRevision(record)
	if !ok {
		return "—"
	}
	for _, dr := range last.Dims {
		if dr.Dim != dim {
			continue
		}
		if dr.Verdict == VerdictOK && recoveredFromBlock(record) {
			return fmt.Sprintf("✅ (rev %d — CRITICAL cleared)", idx+1)
		}
		return verdictEmoji(dr.Verdict)
	}
	return "—"
}

// shortenMessage truncates the commit message so the matrix row does not
// overflow the table.
func shortenMessage(message string) string {
	const maxLen = 48
	runes := []rune(message)
	if len(runes) <= maxLen {
		return message
	}
	return string(runes[:maxLen]) + "..."
}

// RenderUnauditedNotice reports commits that carry no review record: it must
// inform, never gate (the unaudited-commits decision in docs/issues/decisions.md) — the net verdict is
// the only thing that blocks publication. Empty when every commit on the
// branch already has a record, so a fully audited branch's report carries
// nothing extra.
func RenderUnauditedNotice(pending []UnauditedCommit) string {
	if len(pending) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "? %d commit(s) have no review record (not audited by default):\n", len(pending))
	shas := make([]string, 0, len(pending))
	for _, c := range pending {
		sha := c.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		fmt.Fprintf(&b, "  - `%s` %s\n", sha, shortenMessage(c.Subject))
		shas = append(shas, c.SHA)
	}
	fmt.Fprintf(&b, "  Audit them with: sentinel review %s\n", strings.Join(shas, " "))
	return b.String()
}

// RenderMatrix builds a branch's markdown commit × dimension table: one row
// per record (short SHA + message) with the six canonical dimensions as
// fixed columns. Each cell shows the latest revision's verdict for that
// dimension; "—" means that dimension was not audited.
func RenderMatrix(records []Record) string {
	if len(records) == 0 {
		return "_No audited commits._"
	}

	columns := columnOrder()
	var b strings.Builder
	b.WriteString("| Commit | " + strings.Join(columns, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(columns)+1) + "\n")
	for _, record := range records {
		sha := record.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		cells := make([]string, 0, len(columns))
		for _, dim := range columns {
			cells = append(cells, matrixCell(record, dim))
		}
		b.WriteString(fmt.Sprintf("| `%s` %s | %s |\n", sha, shortenMessage(record.Message), strings.Join(cells, " | ")))
	}
	return b.String()
}

// verdictCounts counts the records by the result of their current verdict
// revision (RULE 1: the last authoritative revision) and returns the summary
// line, including question/unavailable only when present. Records without an
// authoritative revision (supplementary-only) are not counted: they were never
// enough to review the commit.
func verdictCounts(records []Record) string {
	counts := map[string]int{}
	for _, record := range records {
		last, _, ok := LastAuthoritativeRevision(record)
		if !ok {
			continue
		}
		counts[last.Result]++
	}
	line := fmt.Sprintf("🟢 ok: %d · 🟡 warn: %d · 🚨 block: %d",
		counts[VerdictOK], counts[VerdictWarn], counts[VerdictBlock])
	if counts[VerdictQuestion] > 0 {
		line += fmt.Sprintf(" · ❓ question: %d", counts[VerdictQuestion])
	}
	if counts[VerdictUnavailable] > 0 {
		line += fmt.Sprintf(" · ⛔ unavailable: %d", counts[VerdictUnavailable])
	}
	return line
}

// severityEmoji maps a finding severity to its risk icon.
func severityEmoji(severity string) string {
	switch severity {
	case SevCritical:
		return "🚨"
	case SevWarning:
		return "⚠️"
	case SevAdvisory:
		return "🔵"
	}
	return "❔"
}

// VerifiedCommand is a verification command executed with its real
// (deterministic) exit code — EVIDENCE, never an invented PASS.
type VerifiedCommand struct {
	Comando string
	Exit    int
}

// TemplateVerification is the PR's verification section: only real
// EVIDENCE, never an invented PASS (the guide's §12.3 golden rule).
type TemplateVerification struct {
	Mode     string            // wire value from ops.Verify: "determinista" | "delegado" | "omitido" | "configurar"
	Comandos []VerifiedCommand // real exit codes per command
	Tested   []string          // the agent's tested contract (delegation)
	Reason   string            // why it did not run (skipped/configuration)

	// Validation carries the real exit codes of internal/validation (T1.8),
	// run BEFORE the branch's semantic review. It is a different evidence
	// nature from Comandos (which translates ops.Verify, the post-hoc
	// verification of tests/build): the same SHAPE (command + real exit
	// code, which is why VerifiedCommand is reused instead of duplicating
	// the type), but a different ORIGIN — hence its own field and its own
	// section in the template, never mixed with Comandos.
	Validation []VerifiedCommand
}

// RecordPending decides whether a record still contributes to the branch's
// verdict. Crediting a fix (FixedIn) is provenance, not proof: a record only
// stops counting once its CURRENT findings no longer block, so a partial fix
// cannot retire a record that still carries live CRITICAL findings. It is the
// package's only definition of "pending", and it decides over the record's
// current findings (RULE 2 in coverage.go) overlaid with the standing human
// answers, so a refuted finding retires the record here too.
//
// It answers whether a record counts, never what its verdict reads: a record
// that still counts contributes the raw Result of its authoritative revision
// to VerdictDeBranch, which no standing answer overlays. That is the
// pre-existing verdict rule (RULE 1), unchanged here, so a refuted finding
// can still leave the branch verdict at block while the risks and the
// blockers of the same report are empty.
func RecordPending(record Record, dispositions []FindingDisposition) bool {
	if record.FixedIn == "" {
		return true
	}
	for _, h := range ApplyDispositions(CurrentFindings(record), FilterDispositionsForSHA(dispositions, record.SHA)) {
		if IsBlocking(h.Severity, h.Status) {
			return true
		}
	}
	return false
}

// pendingRisks collects the CRITICAL and WARNING findings of each pending
// record's CURRENT findings (RULE 2 in coverage.go), not of its raw latest
// revision (ADVISORY ones are information, not risks). Records whose current
// findings no longer block contribute no pending risks.
//
// T6.5: reads the record's current findings through CurrentFindings
// (coverage.go) — the single selection point BranchBlockers further down
// also consumes — instead of forking
// between AggregatedFindings and Dims right here. That avoids the T6.5
// design bug (a semantic finding already superseded by T6.2 only
// disappeared from the pending risks, never from BranchBlockers) and the
// unconditional continue that could take over the record without checking
// severity first.

func pendingRisksWithDispositions(records []Record, dispositions []FindingDisposition) []string {
	var lines []string
	for _, candidate := range effectiveBranchFindings(records, dispositions) {
		h := candidate.finding
		if h.Severity != SevWarning && !IsBlocking(h.Severity, h.Status) {
			continue
		}
		sha := candidate.sha
		if len(sha) > 7 {
			sha = sha[:7]
		}
		lines = append(lines, renderMergedFinding(sha, h))
	}
	return lines
}

type branchFinding struct {
	sha     string
	finding Finding
}

// effectiveBranchFindings is the one branch-level projection for consumers
// that need an individual finding rather than only its legacy v1 rendering.
// It keeps SHA-scoped disposition overlaying and pending-record selection
// out of renderer and gate call sites. It reads the record's CURRENT findings
// (RULE 2 in coverage.go): the authoritative revision's findings plus any
// surfaced supplementary alarms, so a narrow run's CRITICAL still reaches the
// risks and blockers.
func effectiveBranchFindings(records []Record, dispositions []FindingDisposition) []branchFinding {
	var out []branchFinding
	for _, record := range records {
		for _, h := range effectiveRecordFindings(record, dispositions) {
			out = append(out, branchFinding{sha: record.SHA, finding: h})
		}
	}
	return out
}

// mergedFindingSourceLabel distinguishes a merged Finding's origin for
// rendering (T6.5): SourceReview is a semantic LLM inference, SourceValidation
// a deterministic command. Any other value (or none) is labeled "unknown"
// rather than guessed, since a Finding can only be trusted to say what it
// actually declares.
func mergedFindingSourceLabel(source string) string {
	switch source {
	case SourceReview:
		return "review"
	case SourceValidation:
		return "validation"
	default:
		return "unknown"
	}
}

// evidenceEmbedMaxBytes bounds how much of a finding's raw evidence text is
// embedded in the PR body Markdown (T6.5 review finding: security WARNING).
// Evidence/FindingEvidence.Evidence can be the incriminating code fragment
// itself — for a security finding, possibly an embedded secret — and the PR
// body is an external, indexable, cached surface. truncateRunes (already
// used by TruncateBody for the whole body) bounds it the same way, never
// splitting a UTF-8 rune.
const evidenceEmbedMaxBytes = 300

// sanitizeEvidence prepares a finding's raw evidence text before it is
// embedded in a Markdown list item published to the PR body (T6.5 review
// findings: security WARNING x2). Evidence/FindingEvidence.Evidence is
// untrusted output (an LLM inference or a command's literal stdout/stderr),
// so it is: bounded in size with truncateRunes; collapsed to a single line
// so an embedded newline cannot break or forge the surrounding Markdown
// list structure; and wrapped in inline code, replacing any literal
// backtick it already contains so it can never terminate the code span
// early.
func sanitizeEvidence(evidence string) string {
	bounded := truncateRunes(evidence, evidenceEmbedMaxBytes)
	return "`" + sanitizeText(bounded) + "`"
}

// sanitizeText neutralizes free-form untrusted text before it is
// interpolated into the same Markdown list item sanitizeEvidence already
// guards. Finding.Description and Location.File share Evidence's
// untrusted origin — both are decoded straight from the LLM's rawFinding
// JSON output (finding.go), not from a value the guardian resolves itself —
// so an embedded newline or backtick in either could otherwise break or
// forge the surrounding Markdown list structure (T6.5bis review finding:
// security WARNING). Unlike sanitizeEvidence, it does not wrap the result
// in inline code or bound its size: a description/path is prose, not an
// evidence code fragment, and wrapping it in a code span would change its
// visual meaning.
func sanitizeText(text string) string {
	flattened := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(text)
	return strings.ReplaceAll(flattened, "`", "'")
}

// mergedFindingEvidenceLines renders every corroborating evidence T6.1's
// aggregation retained in EvidenceSet, one line per source. A Finding that
// never went through aggregation (EvidenceSet nil, or non-nil but with no
// Values — e.g. deserialized from {"evidence_set":{"values":[]}}) falls back
// to its single legacy Evidence string, so a merged-but-unique finding still
// shows its one piece of evidence instead of nothing.
func mergedFindingEvidenceLines(h Finding) []string {
	if h.EvidenceSet == nil || len(h.EvidenceSet.Values) == 0 {
		if strings.TrimSpace(h.Evidence) == "" {
			return nil
		}
		return []string{fmt.Sprintf("  - evidence [%s]: %s (confidence %.2f)", h.Dimension, sanitizeEvidence(h.Evidence), h.Confidence)}
	}
	lines := make([]string, 0, len(h.EvidenceSet.Values))
	for _, v := range h.EvidenceSet.Values {
		lines = append(lines, fmt.Sprintf("  - evidence [%s]: %s (confidence %.2f)", v.Dimension, sanitizeEvidence(v.Evidence), v.Confidence))
	}
	return lines
}

// renderMergedFinding renders one aggregated Finding — the T6.1 merge +
// T6.2 supersede result carried in AuditResult.Findings — showing its
// distinguished Source and every accumulated evidence plus the combined
// confidence, instead of the single Evidence string a raw per-dimension
// ReviewFinding line shows. The location suffix is omitted entirely when no
// location was resolved, instead of rendering the empty placeholder "(:0)"
// (T6.5 review finding: logic ADVISORY). The "(source, confidence)" segment
// is likewise omitted entirely when h.Source == "": that only happens for a
// legacy v1 ReviewFinding converted by findingFromReviewFinding, which
// never had a real Source to report — stampEffectiveProducer/
// stampSourceReview always stamp one on a real Finding — so showing
// "(unknown, confidence 0.00)" would invent a datum that does not exist
// instead of reporting its absence (T6.5bis review finding: logic WARNING).
// Description and Location.File are sanitized with sanitizeText before
// interpolation: they share Evidence's untrusted LLM origin, so an embedded
// newline or backtick in either must not be able to forge a Markdown list
// line the same way an unsanitized Evidence could (T6.5bis review finding:
// security WARNING).
func renderMergedFinding(sha string, h Finding) string {
	location := ""
	if h.Location.File != "" {
		location = fmt.Sprintf(" (%s:%d)", sanitizeText(h.Location.File), h.Location.LineStart)
	}
	origin := ""
	if h.Source != "" {
		origin = fmt.Sprintf(" (%s, confidence %.2f)", mergedFindingSourceLabel(h.Source), h.Confidence)
	}
	line := fmt.Sprintf("- %s `%s` [%s] %s%s — %s%s",
		severityEmoji(h.Severity), sha, h.Dimension, h.Severity,
		origin, sanitizeText(h.Description), location)
	for _, evidence := range mergedFindingEvidenceLines(h) {
		line += "\n" + evidence
	}
	return line
}

// RenderSummary builds the branch's verdict and risk summary: the global
// count, one row per commit (result, model, revisions and fix) and the
// pending CRITICAL/WARNING risks.
func RenderSummary(records []Record, dispositions []FindingDisposition) string {
	if len(records) == 0 {
		return "_No audited commits._"
	}

	var b strings.Builder
	b.WriteString("### Summary\n")
	b.WriteString(verdictCounts(records) + "\n\n")
	b.WriteString("| Commit | Result | Model | Revs | Fixed |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, record := range records {
		last, _, ok := LastAuthoritativeRevision(record)
		if !ok {
			continue
		}
		sha := record.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		fixed := "—"
		if !RecordPending(record, dispositions) {
			fi := record.FixedIn
			if len(fi) > 7 {
				fi = fi[:7]
			}
			fixed = fmt.Sprintf("🔧 fix credited to `%s`", fi)
		}
		b.WriteString(fmt.Sprintf("| `%s` | %s %s | %s | %d | %s |\n",
			sha, verdictEmoji(last.Result), last.Result, record.Model, len(record.Revisions), fixed))
	}

	b.WriteString("\n### Risks\n")
	pending := pendingRisksWithDispositions(records, dispositions)
	if len(pending) == 0 {
		b.WriteString("- None\n")
	} else {
		for _, line := range pending {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// TruncationMarker is the text that marks a truncated body.
var TruncationMarker = "\n\n> ⚠️ Body truncated: %d bytes omitted.\n"

// PRBodyLimit is the size limit of a PR body (GitHub caps it). The template
// render never exceeds this size.
const PRBodyLimit = 65536

// truncateRunes truncates text to maxBytes without splitting a UTF-8 rune.
// A non-positive limit returns the empty text. The cut is valid if the
// first discarded byte starts a rune; if it is a continuation byte, the
// rune was split and one byte is backed off.
func truncateRunes(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	cut := text[:maxBytes]
	for len(cut) > 0 && !utf8.RuneStart(text[len(cut)]) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// TruncateBody truncates a text to the PR body's byte limit (GitHub caps
// the body), marking the truncation explicitly with the exact number of
// omitted bytes. The result's size never exceeds maxBytes and the cut never
// splits a UTF-8 rune.
func TruncateBody(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}

	// Budget for the content: the marker without the number takes its fixed
	// size; the count digits are adjusted afterwards.
	base := strings.Replace(TruncationMarker, "%d", "", 1)
	budget := maxBytes - len(base)
	if budget <= 0 {
		return truncateRunes(fmt.Sprintf(TruncationMarker, 0), maxBytes)
	}

	kept := truncateRunes(text, budget)
	marker := fmt.Sprintf(TruncationMarker, len(text)-len(kept))
	if len(kept)+len(marker) > maxBytes {
		// The count digits shift the marker: trim the content by just that
		// excess and recompute with the exact number.
		excess := len(kept) + len(marker) - maxBytes
		kept = truncateRunes(kept, len(kept)-excess)
		marker = fmt.Sprintf(TruncationMarker, len(text)-len(kept))
	}
	if len(kept)+len(marker) > maxBytes {
		// Edge case (counts with many digits): even the marker alone does
		// not fit, so it trims itself.
		marker = truncateRunes(marker, maxBytes-len(kept))
	}
	return kept + marker
}

// VerdictDeBranch summarizes the branch's worst global verdict: the one of
// the commit with the most severe revision. It ignores records that no longer
// block (see RecordPending): their block was resolved, and the gate cannot block
// publication over a resolved finding. With no pending records it returns
// VerdictOK. The PR template and the pr create
// block gate use it.
func VerdictDeBranch(records []Record, dispositions []FindingDisposition) string {
	worst := VerdictOK
	for _, record := range records {
		if !RecordPending(record, dispositions) {
			// Resolved: it no longer contributes to the branch verdict.
			continue
		}
		last, _, ok := LastAuthoritativeRevision(record)
		if !ok {
			continue
		}
		if verdictRank(last.Result) > verdictRank(worst) {
			worst = last.Result
		}
	}
	return worst
}

// verdictRank orders verdicts so their severity can be compared. Sync
// contract: when adding a new verdict (a Verdict* constant), update this
// ranking and VerdictDeBranch too.
func verdictRank(verdict string) int {
	switch verdict {
	case VerdictBlock:
		return 4
	case VerdictQuestion:
		return 3
	case VerdictWarn:
		return 2
	case VerdictUnavailable:
		return 1
	default:
		return 0
	}
}

// riskLine is the template's first line: the audit verdict emoji (NOT the
// CI status, guide §12.4) plus the global count.
func riskLine(records []Record, dispositions []FindingDisposition) string {
	vd := VerdictDeBranch(records, dispositions)
	return fmt.Sprintf("%s **Audit verdict: %s** — %s",
		verdictEmoji(vd), vd, verdictCounts(records))
}

func VerdictLine(res *BranchResult, dispositions []FindingDisposition) string {
	if res.Net == nil {
		return riskLine(res.Records, dispositions)
	}
	vd := res.Net.Audit.Verdict
	return fmt.Sprintf("%s **Net audit verdict: %s** — %d finding(s) on net diff %.7s..%.7s",
		verdictEmoji(vd), vd, len(res.Net.Audit.Findings), res.Net.From, res.Net.To)
}

func InheritedSection(inherited []InheritedFinding) string {
	if len(inherited) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## INHERITED (non-blocking)\n")
	for _, h := range inherited {
		fmt.Fprintf(&b, "- %.7s %s/%s\n", h.SHA, h.Finding.Dimension, h.Finding.Severity)
	}
	return b.String() + "\n"
}

// verificationSection describes the verification honestly (§12.3): real
// exit codes per command, the agent's tested contract, or the explicit
// reason why it did not run. Never an invented PASS.
func verificationSection(v TemplateVerification) string {
	var b strings.Builder
	switch v.Mode {
	case "determinista":
		for _, c := range v.Comandos {
			icon := "✅"
			if c.Exit != 0 {
				icon = "❌"
			}
			b.WriteString(fmt.Sprintf("- %s `%s` (exit %d)\n", icon, sanitizeText(c.Comando), c.Exit))
		}
	case "delegado":
		for _, tested := range v.Tested {
			b.WriteString(fmt.Sprintf("- 🤖 agent: `%s`\n", sanitizeText(tested)))
		}
	case "configurar":
		b.WriteString("- ⏸️  Verification not run: stopped to configure `vassentinel.yml`.\n")
	case "omitido":
		reason := v.Reason
		if reason == "" {
			reason = "skipped"
		}
		b.WriteString(fmt.Sprintf("- ⚪ Tests not run (%s).\n", sanitizeText(reason)))
	default:
		b.WriteString("- ⚪ Tests not run.\n")
	}
	return b.String()
}

// validationSection describes the pre-audit validation (T1.8,
// internal/validation): real exit codes of the capabilities run BEFORE the
// semantic review. It never invents a PASS: with no commands it says so
// explicitly instead of omitting it in silence (the same golden rule as
// verificationSection).
func validationSection(cmds []VerifiedCommand) string {
	if len(cmds) == 0 {
		return "- ⚪ No validation commands configured.\n"
	}
	var b strings.Builder
	for _, c := range cmds {
		icon := "✅"
		if c.Exit != 0 {
			icon = "❌"
		}
		b.WriteString(fmt.Sprintf("- %s `%s` (exit %d)\n", icon, sanitizeText(c.Comando), c.Exit))
	}
	return b.String()
}

// BranchBlockers returns the CRITICAL findings of the latest revision of
// each record after overlaying the supplied standing human dispositions. Records
// that no longer block (see RecordPending) contribute no blockers: their block was
// already resolved.
//
// T6.5 review finding (design, the most important one): it used to read
// only Dims, without the T6.2 supersede — a semantic finding already
// discarded for being superseded by a deterministic one kept blocking here
// even though the pending risks no longer showed it. It now consumes
// last.EffectiveFindings() (ledger.go), the same selection point as
// pendingRisksWithDispositions, and projects the result back to
// []ReviewFinding, preserving the structured fields its callers render.
//
// FU-6: the selection moved from EffectiveFindings to the effective
// disposition view (FindingsWithDispositions plus the supplied standing human
// answers, decided by the shared IsBlocking rule), so this projection, the
// engine, and the gate package agree about the same record. A CRITICAL finding
// the ledger already records as refuted or fixed no longer blocks here.
func BranchBlockers(records []Record, dispositions []FindingDisposition) []ReviewFinding {
	var blockers []ReviewFinding
	for _, candidate := range effectiveBranchFindings(records, dispositions) {
		if IsBlocking(candidate.finding.Severity, candidate.finding.Status) {
			blockers = append(blockers, reviewFindingFromFinding(candidate.finding))
		}
	}
	return blockers
}

// RenderBranchPRTemplate renders a PR template from the same effective
// finding projection used by the branch blockers, including supplied standing
// human dispositions.
func RenderBranchPRTemplate(res *BranchResult, verification TemplateVerification, version string, dispositions []FindingDisposition) string {
	var b strings.Builder
	b.WriteString(VerdictLine(res, dispositions) + "\n\n")

	// Pre-audit validation (T1.8): what ran BEFORE auditing, in its own
	// section — never mixed with the post-hoc verification below.
	b.WriteString("## Validation\n")
	b.WriteString(validationSection(verification.Validation) + "\n")

	b.WriteString("## Rationale\n")
	if res.Overview != nil {
		b.WriteString(res.Overview.Rationale + "\n\n")
	} else {
		b.WriteString("_No overview: review the individual commits._\n\n")
	}

	b.WriteString("OWN (per-commit audit)\n")
	b.WriteString(RenderMatrix(res.Records) + "\n\n")
	b.WriteString(RenderUnauditedNotice(res.Unaudited))

	b.WriteString(InheritedSection(res.Inherited))

	b.WriteString("## Risks\n")
	var pending []string
	if res.Net == nil {
		pending = pendingRisksWithDispositions(res.Records, dispositions)
	} else {
		for _, h := range res.Net.Audit.Findings {
			if h.Severity == SevCritical || h.Severity == SevWarning {
				pending = append(pending, renderMergedFinding(fmt.Sprintf("%.7s", res.Net.To), h))
			}
		}
	}
	if len(pending) == 0 {
		b.WriteString("_No pending risks in the latest review._\n\n")
	} else {
		for _, r := range pending {
			b.WriteString(r + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Verification\n")
	b.WriteString(verificationSection(verification) + "\n")

	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("_Generated by VAS Sentinel %s — commit audit, not CI._\n", version))
	return TruncateBody(b.String(), PRBodyLimit)
}

// RenderPRTemplate renders a PR template from records and an overview using
// the supplied standing human dispositions.
func RenderPRTemplate(records []Record, overview *OverviewResult, verification TemplateVerification, version string, dispositions []FindingDisposition) string {
	return RenderBranchPRTemplate(&BranchResult{Records: records, Overview: overview}, verification, version, dispositions)
}
