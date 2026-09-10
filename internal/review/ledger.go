package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Revision is one concrete audit of a SHA. The revisions[] array is
// append-only: re-auditing the same SHA adds a revision, never overwrites
// the previous one. The displayed verdict is the latest revision's. Fixed
// marks that this revision came out with no criticals while the previous
// one was in block. Agent, Model and Effort identify the agent that REALLY
// attended the revision, not the profile it was asked for (H4/T0.2): with
// `active_agent: auto` the chain can fall to another binary and the verdict
// would be left without an author. They are optional: a v1 record written
// before T0.2 lacks them and is still read without error.
type Revision struct {
	At     time.Time         `json:"at"`
	Result string            `json:"result"`
	Fixed  bool              `json:"fixed,omitempty"`
	Agent  string            `json:"agent,omitempty"`
	Model  string            `json:"model,omitempty"`
	Effort string            `json:"effort,omitempty"`
	Dims   []DimensionResult `json:"dims"`
	// Coverage records the coverage this revision actually had: how its
	// dimension plan was chosen (piece 2, docs/design/review-flow-ownership.md).
	// A revision whose plan was derived from the change is CoverageAuthoritative;
	// a revision explicitly narrowed by the operator (sentinel review --dims) is
	// CoverageSupplementary. It is decided at write time by every writer that
	// persists a revision: derive-from-change ⇒ authoritative, explicit-operator
	// dims ⇒ supplementary. Absent (legacy, pre-piece-2 records) is classified
	// as authoritative by IsAuthoritative: there is no way to tell which legacy
	// runs were narrowed, and flipping them all to non-authoritative would erase
	// every historical verdict — the conservative choice keeps the old
	// guarantee intact.
	Coverage RevisionCoverage `json:"coverage,omitempty"`
	// AggregatedFindings is the deduplicated, cross-dimension merged, and
	// supersede-applied result review.AuditCommit already computes
	// (AuditResult.Findings, T6.1+T6.2). It is persisted here, separate
	// from Dims, so the renderer (T6.5) can show fused evidence and Source
	// per finding instead of only the raw per-dimension findings in Dims.
	// Empty for a Revision saved before this field existed, or when the
	// caller never propagated it: consumers must fall back to Dims.
	AggregatedFindings []Finding `json:"aggregated_findings,omitempty"`
}

// EffectiveFindings returns the effective findings for this revision: the
// deduplicated, cross-dimension merged and supersede-applied result
// (AggregatedFindings, T6.1+T6.2) when the caller propagated it, or every
// raw per-dimension v1 ReviewFinding from Dims converted to the v2 Finding
// shape otherwise. It is the single selection point pendingRisks() and
// BranchBlockers (internal/review/renderer.go) both consume (T6.5 review
// finding: before this method existed, BranchBlockers read only Dims and
// could still block on a semantic finding T6.2 had already superseded by a
// deterministic one, defeating the point of supersede in the one flow where
// it gates pr create --force).
func (r Revision) EffectiveFindings() []Finding {
	if len(r.AggregatedFindings) > 0 {
		return r.AggregatedFindings
	}
	var findings []Finding
	for _, dr := range r.Dims {
		for _, h := range dr.Findings {
			findings = append(findings, findingFromReviewFinding(dr.Dim, h))
		}
	}
	return findings
}

// FindingsWithDispositions returns the effective findings of this revision
// carrying the lifecycle disposition each one actually recorded. It exists
// because the two persisted finding shapes hold complementary halves of the
// same evidence: refuteCriticalFindingsWithEvidence (engine.go) writes
// Status onto the raw per-dimension v1 ReviewFinding, while aggregateFindings
// (aggregation.go) persists the merged v2 set with an empty Status and skips
// the refuted findings outright. A consumer reading either shape alone sees
// a disposition-free ledger (FU-7).
//
// It is deliberately NOT what EffectiveFindings returns, and never replaces
// it: that method is the single selection point pendingRisks() and
// BranchBlockers consume, so changing what it returns would change branch
// blocking and pr create --force. This one is for observation only. Like the
// findingFromReviewFinding/reviewFindingFromFinding pair it lives beside,
// the v1-to-v2 correspondence it applies is a rule of the domain, not of the
// consumer that happens to need it today.
//
// The join is by dimension, file, start line and description, the same key
// refuteFindingV2 (engine.go) already uses to find a v1 finding's v2
// counterpart. Fingerprints cannot serve: aggregation recomputes them, and
// the v1 shape has neither Evidence nor Title, the two components Fingerprint
// hashes besides dimension and location. Merging is honoured rather than
// guessed at: mergeFindings keeps one whole contributor as the representative
// of a merged group, so exactly that contributor matches; the siblings it
// absorbed stay unknown instead of lending their disposition to a finding
// that may no longer be theirs.
//
// Only refuted raw findings with no counterpart are appended, never every
// undisposed one. Aggregation drops precisely the refuted findings, so this
// restores exactly what it removed. SupersedeDeterministicFindings
// (supersede.go) also drops semantic findings, silently and without marking
// them, so a broader rule would resurrect superseded findings as live
// observations.
func (r Revision) FindingsWithDispositions() []Finding {
	if len(r.AggregatedFindings) == 0 {
		var findings []Finding
		for _, dr := range r.Dims {
			for _, h := range dr.Findings {
				findings = append(findings, findingWithDisposition(dr.Dim, h))
			}
		}
		return findings
	}

	dispositions := rawDispositions(r.Dims)
	findings := make([]Finding, len(r.AggregatedFindings))
	copy(findings, r.AggregatedFindings)
	counterparts := make(map[string]int, len(findings))
	for i := range findings {
		counterparts[dispositionKey(findings[i].Dimension, findings[i].Location.File, findings[i].Location.LineStart, findings[i].Description)]++
	}
	for i := range findings {
		key := dispositionKey(findings[i].Dimension, findings[i].Location.File, findings[i].Location.LineStart, findings[i].Description)
		// Canonicalise the value, do not merely test it in canonical form:
		// normalizing the check and discarding the result would return the
		// aggregate's status exactly as persisted while every raw-path
		// finding comes back canonical, so one observation API would speak
		// two status representations and a consumer comparing against
		// StatusRefuted would miss a padded refutation.
		findings[i].Status = NormalizeStatus(findings[i].Status)
		// A status the aggregated finding recorded itself is evidence, not an
		// absence: it wins over the raw one rather than being overwritten.
		if findings[i].Status != "" {
			continue
		}
		// Ambiguity is refused rather than guessed at. The key omits Evidence
		// and Title because the v1 shape carries neither, so two aggregates
		// can legitimately share it; attributing one raw disposition to both
		// would mark a finding nobody disposed of, and a security finding
		// carrying a refutation nobody issued reads as dismissed. Under-report
		// instead, exactly as contradictory raw statuses are under-reported.
		if counterparts[key] != 1 {
			continue
		}
		if status, ok := dispositions[key]; ok {
			findings[i].Status = status
		}
	}
	for _, dr := range r.Dims {
		for _, h := range dr.Findings {
			if NormalizeStatus(h.Status) != StatusRefuted {
				continue
			}
			// Keyed on counterpart presence, not on whether the status was
			// adopted: a raw finding whose aggregate kept its own status, or
			// whose key matched several aggregates, is already represented
			// among them and must not be observed a second time.
			if counterparts[dispositionKey(dr.Dim, h.File, int(h.Line), h.Description)] > 0 {
				continue
			}
			findings = append(findings, findingWithDisposition(dr.Dim, h))
		}
	}
	return findings
}

// rawDispositions indexes the dispositions the raw per-dimension findings
// recorded, by the same key FindingsWithDispositions joins on. A key whose
// findings disagree records no disposition at all: contradictory evidence is
// not evidence, and picking a winner would invent a lifecycle answer the
// ledger never gave. Dropping it keeps the result deterministic without
// needing a precedence order nothing in the domain authorises.
func rawDispositions(dims []DimensionResult) map[string]string {
	statusesByKey := make(map[string]map[string]struct{})
	for _, dr := range dims {
		for _, h := range dr.Findings {
			status := NormalizeStatus(h.Status)
			if status == "" {
				continue
			}
			key := dispositionKey(dr.Dim, h.File, int(h.Line), h.Description)
			if statusesByKey[key] == nil {
				statusesByKey[key] = make(map[string]struct{}, 1)
			}
			statusesByKey[key][status] = struct{}{}
		}
	}
	dispositions := make(map[string]string, len(statusesByKey))
	for key, statuses := range statusesByKey {
		if len(statuses) != 1 {
			continue
		}
		for status := range statuses {
			dispositions[key] = status
		}
	}
	return dispositions
}

func dispositionKey(dimension, file string, line int, description string) string {
	return packWithLengthPrefixes(dimension, file, strconv.Itoa(line), description)
}

// findingWithDisposition projects a v1 finding exactly like
// findingFromReviewFinding and then restores the Status that projection
// drops. The drop is correct there: EffectiveFindings feeds the blocking
// gate, which selects on severity and supersede rather than on lifecycle.
// Here the lifecycle is the whole point.
func findingWithDisposition(dimension string, h ReviewFinding) Finding {
	finding := findingFromReviewFinding(dimension, h)
	finding.Status = NormalizeStatus(h.Status)
	return finding
}

// findingFromReviewFinding projects a legacy v1 ReviewFinding onto the v2
// Finding shape so EffectiveFindings can return one uniform type
// regardless of origin. dimension comes from the containing DimensionResult
// (dr.Dim), not the finding itself, matching rawFinding.toFinding's own
// convention (finding.go) for the same v1-to-v2 projection.
//
// Source is deliberately dropped, never copied from h.Source: v1 has no
// Confidence field, and Finding treats Source and Confidence as a pair
// that must co-occur — a Source without its matching real Confidence is an
// invented datum, not an absent one. This makes the round-trip with
// reviewFindingFromFinding intentionally asymmetric for Source: it is
// dropped going v1-to-v2 here, but reviewFindingFromFinding still copies
// it going v2-to-v1, because a v2 Finding built any other way always pairs
// Source with a real Confidence.
func findingFromReviewFinding(dimension string, h ReviewFinding) Finding {
	return Finding{
		Dimension:   dimension,
		Severity:    h.Severity,
		Description: h.Description,
		Location:    Location{File: h.File, LineStart: int(h.Line)},
	}
}

// reviewFindingFromFinding projects a v2 Finding back onto the v1 shape
// ReviewFinding that BranchBlockers (renderer.go) still returns publicly.
// It lives beside findingFromReviewFinding instead of in the renderer: both
// directions of the v1↔v2 pair are a mapping rule of the domain, not of
// rendering, and keeping them together avoids a new field in
// Finding/ReviewFinding forcing two files to be touched with the risk of
// silent divergence. It is not the exact inverse of findingFromReviewFinding
// for Source: see that function's comment for why the asymmetry is
// intentional, not an oversight.
func reviewFindingFromFinding(h Finding) ReviewFinding {
	return ReviewFinding{
		Dimension:   h.Dimension,
		File:        h.Location.File,
		Line:        Line(h.Location.LineStart),
		Severity:    h.Severity,
		Description: h.Description,
		Source:      h.Source,
	}
}

// Record is the complete audit record of a commit, saved as
// <git-dir>/vas-sentinel/<sha>.json. FixedIn is the SHA of the commit that
// fixed the findings (filled in when a fix re-audits the files).
type Record struct {
	SHA       string     `json:"sha"`
	Message   string     `json:"message"`
	Bucket    string     `json:"bucket"`
	Model     string     `json:"model"`
	OriginSHA string     `json:"origin_sha,omitempty"`
	FixedIn   string     `json:"fixed_in,omitempty"`
	Revisions []Revision `json:"revisions"`
}

// Ledger gives access to the records by SHA inside Git's common-dir.
type Ledger struct {
	dir string
	// releaseLockFile releases the lock file. It is a field, not a direct
	// call, because the failure branch decides whether an already-made
	// mutation is reported or lost and there is no way to provoke it from
	// the public API: it happens inside the critical section. Per-Ledger and
	// not a package variable, so a fixture that injects a failure cannot
	// reach another instance or another test running beside it.
	releaseLockFile func(string) error
}

// NewLedger creates a ledger anchored at <gitDir>/vas-sentinel. It does not
// create the directory: that happens on the first write.
//
// It files records under whatever directory it is given and imposes no
// choice, exactly like store.NewStore. Callers that read or write review
// evidence must pass the Git common directory so linked worktrees share one
// ledger; passing a per-checkout gitDir files the record where `git
// worktree remove` destroys it. In cmd/sentinel that choice lives in
// sharedReviewLedger. The exception is `runs prune`, which enumerates every
// per-checkout ledger on purpose so no execution stream loses its
// provenance.
func NewLedger(gitDir string) *Ledger {
	return &Ledger{dir: filepath.Join(gitDir, "vas-sentinel"), releaseLockFile: os.Remove}
}

// RecordPath returns the file path of a SHA's record.
func (l *Ledger) RecordPath(sha string) string {
	return filepath.Join(l.dir, sha+".json")
}

// ReadRecord returns the record of a SHA or nil if it does not exist yet. A
// corrupt file is an explicit error: the ledger is never wiped in silence.
func (l *Ledger) ReadRecord(sha string) (*Record, error) {
	data, err := os.ReadFile(l.RecordPath(sha))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// ListRecords returns the SHAs with a saved audit record, in alphabetical
// order. It does not read the content: use ReadRecord per SHA for that.
//
// Enumerated with os.ReadDir and NOT with filepath.Glob. Glob reports only
// ErrBadPattern and swallows every I/O error it meets while reading a
// directory, so a static pattern over an unreadable ledger returned an empty
// list and a nil error. That is indistinguishable from a ledger holding no
// records, and it fails open in the callers whose whole contract is to fail
// closed: collectProvenanceReferences decides from this list which execution
// streams a prune may destroy, and PurgeOrphans decides which records it may
// delete (FU-16).
//
// Absence is separated from breakage with Lstat before the read. NewLedger
// does not create the directory — the first saved revision does — so a
// ledger nobody has written to yet genuinely holds no records. But
// os.ReadDir resolves symlinks, so it answers ErrNotExist for a ledger path
// pointing nowhere too, and only Lstat tells the two apart.
func (l *Ledger) ListRecords() ([]string, error) {
	if _, err := os.Lstat(l.dir); errors.Is(err, os.ErrNotExist) {
		return []string{}, nil // no revision was ever saved in this checkout
	} else if err != nil {
		return nil, fmt.Errorf("checking the review ledger path %s: %w", l.dir, err)
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, fmt.Errorf("enumerating the review ledger in %s: %w", l.dir, err)
	}
	shas := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue // temp files from saveRecord, and anything else
		}
		shas = append(shas, strings.TrimSuffix(name, ".json"))
	}
	sort.Strings(shas)
	return shas, nil
}

// maxRecordLockWait bounds how long a writer waits for another process's
// lock. A review that cannot persist safely fails loudly: dropping a
// revision in silence is the exact failure this lock exists to prevent.
const maxRecordLockWait = 30 * time.Second

// recordLockRetryInterval is the poll interval while waiting. The critical
// section is one read, one marshal and one rename, so a short poll costs
// nothing and keeps an uncontended second writer from stalling.
const recordLockRetryInterval = 25 * time.Millisecond

// ErrLockNotReleased marks the one failure that must not be read as "the
// mutation did not happen": the protected operation COMPLETED, and only the
// lock protocol around it broke. It says nothing about which mutation ran,
// so it is equally correct for a write, for a deletion, and for a callback
// that decided to change nothing — what a caller may infer is exactly that
// the operation reached its own end.
//
// The distinction has to exist because a deletion reported as a plain
// failure leaves the record gone and its events pointing at it.
var ErrLockNotReleased = errors.New("the review record operation completed but its lock protocol broke")

// withRecordLock runs fn while holding an exclusive lock on a SHA's record.
//
// The ledger became repository-wide when review, status and pr moved their
// anchor to the Git common directory, so two checkouts now audit into the
// same record file. Every mutation here is a read-append-rename cycle, and
// without serialization each writer reads the same record, appends its own
// revision and replaces the other: a clean result can erase a blocking one,
// and `review --all` then reads that SHA as audited so the lost verdict
// never resurfaces. Measured: eight concurrent writers left one revision of
// eight.
//
// The lock is a file created with O_EXCL, which is atomic on POSIX and on
// Windows. It is advisory and only between sentinel processes; nothing
// stops an editor from rewriting a record by hand. Its name ends in .lock
// and not .json, so ListRecords never reports it as a SHA.
//
// A process killed inside the critical section leaves its lock behind, and
// the wait then fails naming the path so an operator can remove it. That is
// deliberate: stealing a lock after a timeout would guess that the holder
// is dead, and guessing wrong reintroduces the lost update this prevents.
func (l *Ledger) withRecordLock(sha string, fn func() error) (err error) {
	if err := os.MkdirAll(l.dir, 0755); err != nil {
		return err
	}
	lockPath := l.RecordPath(sha) + ".lock"
	deadline := time.Now().Add(maxRecordLockWait)
	for {
		lock, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if openErr == nil {
			if closeErr := lock.Close(); closeErr != nil {
				os.Remove(lockPath)
				return fmt.Errorf("locking the review record of %s: %w", sha, closeErr)
			}
			// Released through defer so a panic inside fn cannot leak the
			// lock. The removal failure is reported rather than discarded,
			// because a lock left behind makes every later writer of this
			// SHA wait the full timeout and then fail — but ONLY when fn
			// itself succeeded. When fn already failed its error is the one
			// the caller needs, and replacing it with a cleanup complaint
			// would hide the real cause; the stale lock still surfaces on
			// the next writer, with its path.
			defer func() {
				removeErr := l.releaseLockFile(lockPath)
				if removeErr == nil || err != nil {
					return
				}
				// An already-absent lock is NOT quietly fine. Once fn
				// starts, this deferred call is the only remover left — the
				// failed-Close branch above removes the lock too, but it
				// returns without ever running fn — so finding it gone means
				// mutual exclusion broke while fn ran and another writer may
				// have entered. Reporting success there would hide a lost
				// update behind the one signal that could have revealed it.
				//
				// The cause is wrapped, not formatted: a caller inspecting
				// the filesystem failure with errors.Is or errors.As is
				// exactly the caller this error exists for.
				err = fmt.Errorf("%w (%s of %s): %w", ErrLockNotReleased, lockPath, sha, removeErr)
			}()
			return fn()
		}
		if !errors.Is(openErr, os.ErrExist) {
			return fmt.Errorf("locking the review record of %s: %w", sha, openErr)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the review record of %s stayed locked for %s; if no sentinel process is running, delete %s",
				sha, maxRecordLockWait, lockPath)
		}
		time.Sleep(recordLockRetryInterval)
	}
}

// SaveRevision appends a revision to the SHA's record (creating it if it is
// the first) and persists it with an atomic temp + rename write. On Windows
// the existing destination is deleted before the rename because the system
// does not allow overwriting with os.Rename.
//
// The whole read-append-write runs under the record's lock: revisions[] is
// append-only, and two unserialized writers make that a claim rather than a
// fact.
func (l *Ledger) SaveRevision(sha, message, bucket, model string, revision Revision) error {
	return l.withRecordLock(sha, func() error { return l.saveRevisionLocked(sha, message, bucket, model, revision) })
}

// WithLockedRecord runs fn while holding this SHA's exclusive record lock,
// handing it the authoritative current record. It is the compare-and-append
// seam for writers that persist outside the record file itself (FU-6 human
// dispositions): resolving against a record read before the lock and then
// appending after it lets a concurrent re-audit change the revision in
// between, so the writer must re-read under the same lock the revision
// writer holds and refuse on any difference. It reuses withRecordLock,
// never a second lock.
func (l *Ledger) WithLockedRecord(sha string, fn func(*Record) error) error {
	return l.withRecordLock(sha, func() error {
		record, err := l.ReadRecord(sha)
		if err != nil {
			return err
		}
		if record == nil {
			return fmt.Errorf("ledger: no review record for %s", sha)
		}
		return fn(record)
	})
}

func (l *Ledger) saveRevisionLocked(sha, message, bucket, model string, revision Revision) error {
	record, err := l.ReadRecord(sha)
	if err != nil {
		return err
	}
	if record == nil {
		record = &Record{SHA: sha, Message: message, Bucket: bucket, Model: model}
	}
	if record.Message == "" {
		record.Message = message
	}
	if record.Bucket == "" {
		record.Bucket = bucket
	}
	if record.Model == "" {
		record.Model = model
	}
	record.Revisions = append(record.Revisions, revision)
	return l.saveRecord(record)
}

// MarkFixed records that the SHA's findings were fixed by the fixedIn
// commit (finding → fix traceability). It does not overwrite an existing
// FixedIn: the first fix wins.
func (l *Ledger) MarkFixed(sha, fixedIn string) error {
	// Checked on the DIRECTORY, not on the record, and with Lstat. Two
	// separate reasons, both learned the hard way in this file.
	//
	// The directory rather than the record: saveRecord deletes the
	// destination before renaming over it, so an unlocked check on the
	// record lands in that window and reports a live record as absent.
	// Measured — it dropped the correction in silence. The guard exists only
	// to keep a ledger that never stored anything from having its directory
	// created by a call that is a documented no-op, and this one runs for
	// every earlier commit of every fix commit.
	//
	// Lstat rather than Stat: Stat resolves symlinks, so a ledger path
	// pointing nowhere answers ErrNotExist exactly like an absent one. That
	// is FU-16, and ListRecords separates the two the same way. A broken
	// ledger must fail, not silently skip the correction.
	if _, err := os.Lstat(l.dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// Under the record's lock: "the first correction wins" is decided by
	// reading FixedIn and then writing it, so two unserialized callers can
	// both read it empty and the second one wins instead.
	return l.withRecordLock(sha, func() error {
		record, err := l.ReadRecord(sha)
		if err != nil {
			return err
		}
		if record == nil || record.FixedIn != "" {
			return nil
		}
		record.FixedIn = fixedIn
		return l.saveRecord(record)
	})
}

// AdoptRecord copies the record at from under the SHA to. It is the T2.7
// fix for commitCoveredByBlobs: when a rebase rewrites a commit without
// touching its content, the commit is blob-covered under an earlier SHA,
// but if AnalyzeBranch only did "continue" without writing anything under
// the new SHA, ledger.ReadRecord(to) would return nil and the real findings
// of the old record (an orphan, under a SHA that no longer exists in the
// branch) would disappear from res.Records. Adopting the record under the
// new SHA is what keeps them recoverable.
//
// from must exist when a caller asks this method to adopt: DecideBlobReuse
// rejects stale blob-index candidates whose ledger record was pruned before
// reaching this method. Unlike MarkFixed (where "there is nothing to mark" is
// a valid state resolved as a no-op), a from with no record here is a real
// caller error, not something to ignore in silence.
//
// Idempotent: calling it twice with the same arguments overwrites with the
// same content, without duplicating anything. Revisions is copied to a new
// slice (the origin record's underlying one is not shared) for aliasing
// hygiene, not because Revision is mutated after being saved.
func (l *Ledger) AdoptRecord(from, to string) error {
	return l.adoptRecord(from, to, "", false)
}

// AdoptRecordWithMessage copies a reviewed record under a new SHA while retaining
// the destination commit message and the source SHA as provenance. Reuse callers
// must use this variant because spec review evaluates the destination message.
func (l *Ledger) AdoptRecordWithMessage(from, to, message string) error {
	return l.adoptRecord(from, to, message, true)
}

func (l *Ledger) adoptRecord(from, to, message string, useDestinationMessage bool) error {
	// Locked on to, which is the record this writes. Locking from too would
	// be a second lock in a fixed-order pair and buys nothing: the source is
	// only read, and a concurrent append to it either lands in the copy or
	// does not, whereas an unserialized adoption can overwrite a revision
	// another writer just appended to the destination.
	return l.withRecordLock(to, func() error {
		source, err := l.ReadRecord(from)
		if err != nil {
			return err
		}
		if source == nil {
			return fmt.Errorf("ledger: no record at %s to adopt into %s", from, to)
		}
		if !useDestinationMessage {
			message = source.Message
		}
		revisions := make([]Revision, len(source.Revisions))
		copy(revisions, source.Revisions)
		adopted := &Record{
			SHA:       to,
			Message:   message,
			Bucket:    source.Bucket,
			Model:     source.Model,
			OriginSHA: from,
			FixedIn:   source.FixedIn,
			Revisions: revisions,
		}
		return l.saveRecord(adopted)
	})
}

// SaveReusedSpecRevision appends an authoritative revision that retains the
// adopted record's non-spec dimensions and replaces only spec with a fresh audit.
// It never accepts another dimension: accepting one would silently overwrite a
// content-derived verdict that reuse deliberately preserves.
func (l *Ledger) SaveReusedSpecRevision(sha string, spec Revision) error {
	return l.withRecordLock(sha, func() error {
		record, err := l.ReadRecord(sha)
		if err != nil {
			return err
		}
		if record == nil || record.OriginSHA == "" {
			return fmt.Errorf("ledger: no adopted record at %s for spec reuse", sha)
		}
		base, _, ok := LastAuthoritativeRevision(*record)
		if !ok {
			return fmt.Errorf("ledger: no authoritative revision at %s for spec reuse", sha)
		}
		merged, err := mergeReusedSpecRevision(base, spec)
		if err != nil {
			return err
		}
		record.Revisions = append(record.Revisions, merged)
		return l.saveRecord(record)
	})
}

func mergeReusedSpecRevision(base, spec Revision) (Revision, error) {
	for _, result := range spec.Dims {
		if result.Dim != DimSpec {
			return Revision{}, fmt.Errorf("ledger: reused spec revision includes %q", result.Dim)
		}
	}
	merged := spec
	// Revision is passed by value but its slices still share backing arrays.
	// Copy before appending so this merge cannot mutate the caller's spec input.
	merged.Dims = append([]DimensionResult(nil), spec.Dims...)

	// A caller may provide only raw dimension results, as records written before
	// aggregated findings existed do. Preserve the revision's normal fallback so
	// those fresh spec findings are not lost while merging the reused dimensions.
	specFindings := spec.AggregatedFindings
	if len(specFindings) == 0 {
		specFindings = spec.EffectiveFindings()
	}
	merged.AggregatedFindings = append([]Finding(nil), specFindings...)
	for _, result := range base.Dims {
		if result.Dim != DimSpec {
			merged.Dims = append(merged.Dims, result)
		}
	}
	for _, finding := range base.EffectiveFindings() {
		if finding.Dimension != DimSpec {
			merged.AggregatedFindings = append(merged.AggregatedFindings, finding)
		}
	}
	dimensions := make([]DimensionOutcome, 0, len(merged.Dims))
	for i := range merged.Dims {
		dimensions = append(dimensions, DimensionOutcome{Dim: merged.Dims[i].Dim, Result: &merged.Dims[i]})
	}
	merged.Result, _ = globalVerdict(dimensions)
	merged.Coverage = CoverageAuthoritative
	return merged, nil
}

// DeleteRecord deletes the record of a SHA. It returns no error when the
// record does not exist: deleting something already gone is a no-op.
func (l *Ledger) DeleteRecord(sha string) error {
	path := l.RecordPath(sha)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// PurgeOrphans deletes the records whose SHA is no longer reachable from any
// ref (commits rewritten by rebase, amend or squash: they remain in the
// object store as dangling, but ContentInSomeRef detects them). It
// returns the list of deleted SHAs; if something fails halfway, it returns
// the error together with the SHAs it did delete. The case of the commit
// rewritten by amend is covered by TestLedgerPurgeOrphansDangling.

// The existence criterion is injected and MUST be able to fail. There is no
// variant without it on purpose: the one there used to be wrapped a helper
// that turned any git error into "not there", so the convenient path was
// exactly the one deleting live records in the face of a broken query. A
// caller purging the ledgers of several checkouts must also anchor the
// criterion to the right repository: classifying with the process's working
// directory turns every live record of another checkout into an orphan.
func (l *Ledger) PurgeOrphans(exists func(sha string) (bool, error)) ([]string, error) {
	shas, err := l.ListRecords()
	if err != nil {
		return nil, err
	}
	deleted := []string{}
	for _, sha := range shas {
		present, err := exists(sha)
		if err != nil {
			// Abort with what was already deleted: asking and never being
			// able to answer authorizes the deletion.
			return deleted, err
		}
		if present {
			continue
		}
		// Deletion takes the same per-SHA lock as every mutation. Without it
		// the purge could remove a record while a locked SaveRevision was
		// writing it, so a revision that reported success would simply not
		// exist.
		if err := l.withRecordLock(sha, func() error { return l.DeleteRecord(sha) }); err != nil {
			// ErrLockNotReleased means the record IS deleted and only the
			// lock survived. Recording it before aborting is what lets the
			// caller clean its events, which are purged from this very list.
			if errors.Is(err, ErrLockNotReleased) {
				deleted = append(deleted, sha)
			}
			return deleted, err
		}
		deleted = append(deleted, sha)
	}
	return deleted, nil
}

// saveRecord persists the record with an atomic temp + rename write. On
// Windows the existing destination is deleted before the rename because the
// system does not allow overwriting with os.Rename.
func (l *Ledger) saveRecord(record *Record) error {
	if err := os.MkdirAll(l.dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}

	destination := l.RecordPath(record.SHA)
	tmp, err := os.CreateTemp(l.dir, "record-*.tmp")
	if err != nil {
		return err
	}
	tempPath := tmp.Name()
	defer os.Remove(tempPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// The destination is removed ONLY on Windows, which refuses to rename
	// over an existing file. Elsewhere rename replaces it atomically, and
	// deleting first opened a window in which the record did not exist at
	// all: a concurrent reader saw a live record as absent, which
	// AnalyzeBranch reads as "never audited". Narrowing that window to the
	// platform that needs it costs nothing.
	//
	// Not covered by a test, deliberately. Observing the window needs a
	// reader running inside another process's rename, and the only seam that
	// would expose it is a hook in this function — which would be a test
	// scaffold in the write path of every record. The behaviour it protects
	// is pinned indirectly: MarkFixed's guard reads the directory rather
	// than the record precisely because that window existed, and its
	// comment says so.
	if runtime.GOOS == "windows" {
		if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(tempPath, destination)
}
