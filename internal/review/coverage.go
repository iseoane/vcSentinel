package review

// Piece 2 of docs/design/review-flow-ownership.md: the coverage contract and
// the shared authoritative-revision selector. Everything that answers "what is
// the current verdict / the current findings of this commit" lives here, in ONE
// exported place. There are deliberately TWO rules, and both must stay here so
// nobody later "simplifies" them back into one:
//
//   - RULE 1 (the verdict rule): the commit's current VERDICT (its Result) is
//     the last AUTHORITATIVE revision. A supplementary (operator-narrowed,
//     sentinel review --dims) run can never set, clear or downgrade it.
//     Consumers of the Result string — status, the commit × dimension matrix,
//     the summary counts, the branch verdict, the "corrected a previous block"
//     flag — use LastAuthoritativeRevision.
//
//   - RULE 2 (the finding rule): the findings that currently surface as risks
//     and blockers are the current authoritative revision's findings PLUS the
//     CRITICAL findings of supplementary revisions that a later authoritative
//     audit of the same dimension did not supersede. A narrow run does not earn
//     the right to say "all clear", but it does earn the right to raise an
//     alarm; the re-blocking risk is accepted and recoverable by design through
//     refute/accept. Consumers of findings — BranchBlockers, pendingRisks, the
//     net/inherited context, and the disposition commands — use CurrentFindings.
//
// The asymmetry is deliberate (owner decision, recorded in
// docs/design/review-flow-ownership.md, piece 2): verdict-level and
// finding-level selection follow different rules; both live here, one place to
// read, not one rule.

// RevisionCoverage classifies the coverage a revision actually had: how its
// dimension plan was chosen.
type RevisionCoverage string

const (
	// CoverageAuthoritative marks a run whose dimension plan was derived from
	// the change being audited (PlanForProfile). It is the only coverage that
	// can establish or change a commit's verdict.
	CoverageAuthoritative RevisionCoverage = "authoritative"

	// CoverageSupplementary marks a run whose dimension plan was explicitly
	// chosen by the operator (sentinel review --dims). It is recorded and
	// visible, never authoritative, and never enough to call a commit reviewed;
	// its CRITICAL findings still surface as alarms under CurrentFindings.
	CoverageSupplementary RevisionCoverage = "supplementary"
)

// IsAuthoritative reports whether the revision carries change-derived coverage
// and may therefore establish a verdict.
//
// A legacy revision written before the coverage field existed (Coverage == "")
// classifies as authoritative: old writers all derived their plans by default
// and there is no way to tell a narrowed legacy run apart, so the conservative
// choice (and the one that keeps every historical verdict intact) is to keep
// counting them as authoritative. An unknown value fails closed as
// non-authoritative rather than silently authorizing a writer the package does
// not recognize.
func (r Revision) IsAuthoritative() bool {
	return r.Coverage == "" || r.Coverage == CoverageAuthoritative
}

// Audited reports whether the revision audited the given dimension. A later
// authoritative audit of a dimension is the only thing that can supersede an
// earlier supplementary alarm in that dimension.
func (r Revision) Audited(dimension string) bool {
	for _, dr := range r.Dims {
		if dr.Dim == dimension {
			return true
		}
	}
	return false
}

// LastAuthoritativeRevision is RULE 1: it returns the record's current verdict
// revision — the last AUTHORITATIVE revision — its 0-based position within
// record.Revisions, and whether one exists. It is false when the record has no
// authoritative revision (empty, or only supplementary revisions).
//
// A supplementary revision can never be the returned revision: a narrow run
// cannot clear or downgrade an authoritative verdict. Renderers use the index
// to number the revision in the "CRITICAL cleared" marker.
func LastAuthoritativeRevision(record Record) (revision Revision, index int, ok bool) {
	for i := len(record.Revisions) - 1; i >= 0; i-- {
		if record.Revisions[i].IsAuthoritative() {
			return record.Revisions[i], i, true
		}
	}
	return Revision{}, -1, false
}

// CurrentFindings is RULE 2: it returns the findings of a record that currently
// surface as risks and blockers. It is the union of:
//
//   - the current authoritative revision's findings (FindingsWithDispositions),
//     when one exists, and
//   - the blocking (CRITICAL, not refuted/fixed as recorded) findings of every
//     supplementary revision that a later authoritative audit of the same
//     dimension did not supersede — the alarms a narrow run is allowed to raise.
//
// Findings are NOT deduplicated: the domain contract treats two findings
// addressing the same fingerprint as ambiguous, and the disposition commands
// fail closed rather than guessing which one the human meant (see
// ResolveRecordDispositionTarget). The set is returned in stable order
// (authoritative first, then alarms in revision order) with its recorded
// lifecycle statuses so the caller can overlay standing human dispositions
// (ApplyDispositions — which applies one answer to every matching fingerprint)
// and apply the shared IsBlocking rule.
func CurrentFindings(record Record) []Finding {
	var out []Finding

	base, _, ok := LastAuthoritativeRevision(record)
	if ok {
		out = append(out, base.FindingsWithDispositions()...)
	}

	for i, rev := range record.Revisions {
		if rev.IsAuthoritative() {
			continue
		}
		for _, f := range rev.FindingsWithDispositions() {
			if !IsBlocking(f.Severity, f.Status) {
				continue
			}
			if alarmSuperseded(record, i, f.Dimension) {
				continue
			}
			out = append(out, f)
		}
	}
	return out
}

// effectiveRecordFindings is the single per-record projection every branch
// reporting surface reads: nothing when the record no longer counts
// (RecordPending), and otherwise its current findings (RULE 2) overlaid with
// the standing human answers for that SHA. The branch blockers, the pending
// risks and the inherited section of a stacked PR all consume it, so a
// refuted finding cannot surface on one of them while another treats the
// record as resolved.
func effectiveRecordFindings(record Record, dispositions []FindingDisposition) []Finding {
	if !RecordPending(record, dispositions) {
		return nil
	}
	return ApplyDispositions(CurrentFindings(record), FilterDispositionsForSHA(dispositions, record.SHA))
}

// alarmSuperseded reports whether a later authoritative audit of the same
// dimension has re-verified it since a supplementary alarm was recorded. Only a
// derived-plan audit that actually covers the dimension is authority enough to
// clear its alarm; a re-audit whose plan skipped the dimension cannot.
func alarmSuperseded(record Record, suppIdx int, dimension string) bool {
	for i := suppIdx + 1; i < len(record.Revisions); i++ {
		if record.Revisions[i].IsAuthoritative() && record.Revisions[i].Audited(dimension) {
			return true
		}
	}
	return false
}

// RecordHasActiveBlock reports whether a record currently reads as blocked:
// its current authoritative revision carries a blocking finding, or a
// supplementary alarm is active. Dispositions are NOT applied — callers that
// read the raw record (the fix-retirement path in recordFixes) deliberately do
// not load the dispositions log, mirroring how that flow has always treated an
// authoritative block.
func RecordHasActiveBlock(record Record) bool {
	for _, f := range CurrentFindings(record) {
		if IsBlocking(f.Severity, f.Status) {
			return true
		}
	}
	return false
}

// ResolveRecordDispositionTarget finds the single effective finding of a
// record's current view (RULE 2) addressed by a stable fingerprint. It is the
// record-level counterpart of ResolveDispositionTarget: dispositions must be
// able to address the alarm a supplementary run raised, not only findings of
// the authoritative revision — that is how a re-raised finding is recoverable
// by design. Missing and ambiguous fingerprints fail closed.
func ResolveRecordDispositionTarget(record Record, fingerprint string) (Finding, error) {
	return resolveDispositionTarget(CurrentFindings(record), fingerprint)
}

// ResolveRecordDispositionTargetWithDispositions resolves a fingerprint after
// overlaying the standing human answers, exactly like its revision-level
// counterpart but over the record's current findings.
func ResolveRecordDispositionTargetWithDispositions(record Record, fingerprint string, dispositions []FindingDisposition) (Finding, error) {
	return resolveDispositionTarget(ApplyDispositions(CurrentFindings(record), dispositions), fingerprint)
}
