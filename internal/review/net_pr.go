package review

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

type NetReviewOptions struct {
	Intention  string // PR title/description supplied by the caller
	Validation string // supplied full-validation evidence, carried as structured net evidence
	// Dispositions carries the append-only human answers recorded against
	// the commits in range (FU-6 follow-up unit A). Only the net review
	// carries them across SHAs, by exact fingerprint plus evidence
	// revalidation at the head; per-commit audits stay SHA-bound and never
	// read this field. Empty by default: callers without human answers
	// behave exactly as before.
	Dispositions []FindingDisposition
}

// HistoricalFinding: untrusted per-commit context — never merged into net Findings, never blocking; ArchiveReason non-empty = archived.
type HistoricalFinding struct {
	SHA string // commit whose recorded revision carried the finding
	Hallazgo
	ArchiveReason string // empty means still ACTIVE context
	// ClassificationError, non-empty, marks a failed archive classification:
	// history is context-only, so the finding stays ACTIVE, never aborting.
	ClassificationError string
}
type NetReview struct {
	From, To string // merge_base(base_or_resolved_parent, HEAD)..resolved HEAD
	Audit    ResultadoAuditoria
	Context  []HistoricalFinding
}

// runNetReview: immutable [from,to] range via standard engine seams; archive needs positive typed removal evidence.
func runNetReview(o *NetReviewOptions, opts OpcionesRama, from, to string, revisions []Ficha) (*NetReview, error) {
	if opts.Fabrica == nil {
		return nil, fmt.Errorf("net review needs a reviewer factory")
	}
	diff, paths, err := git.RangeEvidence(from, to)
	if err != nil {
		return nil, err // real Git failure: never fabricate net evidence
	}
	safePaths := rutasRevisionSeguras(paths)
	// Rename evidence only classifies historical context whose old path is absent
	// at the final state, so maps load lazily through this cache and a load
	// failure becomes ClassificationError on exactly those findings — never an
	// abort. Paths born INSIDE the range pair against neither endpoint, hence
	// the origin-scoped fallback (same cache, keyed by origin).
	renameCache := map[string]map[string]string{}
	renamesFor := func(rev string) (map[string]string, error) {
		if m, ok := renameCache[rev]; ok {
			return m, nil
		}
		m, err := git.RangeRenames(rev, to)
		if err != nil {
			return nil, err
		}
		renameCache[rev] = m
		return m, nil
	}
	classify := func(finding Hallazgo, originSHA string) (string, error) {
		path := filepath.ToSlash(strings.TrimSpace(finding.Location.Archivo))
		if path == "" {
			return "", nil // unlocated findings stay active context: no Git call at all
		}
		originContent, opresent, err := git.ReadPathAtRevision(originSHA, path)
		if err != nil {
			return "", err // real Git failure: never fabricate archive evidence
		}
		flat := func(s string) string { return strings.Join(strings.Fields(s), "") }
		lines := strings.Split(strings.ReplaceAll(originContent, "\r\n", "\n"), "\n")
		var snippet string
		if opresent && finding.Location.LineaInicio > 0 && finding.Location.LineaInicio <= len(lines) {
			end := min(max(finding.Location.LineaFin, finding.Location.LineaInicio), len(lines))
			snippet = flat(strings.Join(lines[finding.Location.LineaInicio-1:end], "\n"))
		}
		headContent, present, err := git.ReadPathAtRevision(to, path)
		if err != nil {
			return "", err
		}
		if !present {
			mapping, rerr := renamesFor(from)
			if rerr != nil {
				return "", rerr
			}
			destination, renamed := mapping[path]
			if !renamed {
				if mapping, rerr = renamesFor(originSHA); rerr != nil {
					return "", rerr
				}
				destination, renamed = mapping[path]
			}
			if renamed { // renamed away: classify against the destination content
				if headContent, present, err = git.ReadPathAtRevision(to, destination); err != nil {
					return "", err
				}
				if !present {
					return "", nil // contradictory Git state: keep active rather than fabricate evidence
				}
			} else {
				// -M detection is heuristic and misses heavily edited renames (D+A).
				// Archive only when the referenced snippet is proven absent from every
				// changed path of the final state; without a snippet, stay uncertain.
				if snippet == "" {
					return "", fmt.Errorf("no referenced code available; deletion unproven")
				}
				for _, candidate := range safePaths {
					content, cpresent, cerr := git.ReadPathAtRevision(to, candidate)
					if cerr != nil {
						return "", cerr
					}
					if cpresent && strings.Contains(flat(content), snippet) {
						headContent, present = content, true // survived an undetected rename: stays ACTIVE
						break
					}
				}
				if !present {
					return "path absent from the final net state", nil // proven gone from all relevant paths
				}
			}
		}
		if snippet != "" && !strings.Contains(flat(headContent), snippet) { // origin absence alone never archives
			return "referenced lines removed from the final net state", nil
		}
		return "", nil
	}
	var history []HistoricalFinding
	for _, record := range revisions {
		last, ok := ultimaRevision(record)
		if !ok {
			continue
		}
		for _, finding := range last.HallazgosEfectivos() {
			entry := HistoricalFinding{SHA: record.SHA, Hallazgo: finding}
			reason, cerr := classify(finding, record.SHA)
			if cerr != nil {
				entry.ClassificationError = cerr.Error()
			} else {
				entry.ArchiveReason = reason
			}
			history = append(history, entry)
		}
	}
	profile, err := change.PerfilDeCambio(from, to)
	if err != nil {
		return nil, fmt.Errorf("net change profile: %w", err)
	}
	netDiff, derr := git.DiffRango(from, to)
	if derr != nil {
		return nil, fmt.Errorf("net range diff: %w", derr)
	}
	netAtributos, aerr := git.Attributes(to)
	if aerr != nil {
		return nil, fmt.Errorf("net range attributes: %w", aerr)
	}
	// The COMPLETE path list, not safePaths. rutasRevisionSeguras drops any name
	// containing *?[]{}!, a control character or a leading dash, which is right
	// for the surfaces that interpolate those names into reviewer prompts and
	// Git arguments — and wrong here. Classification only matches globs against
	// strings and has no such exposure, so the sanitised list silently removed
	// route evidence: measured, infra/main[1].tf classifies as infrastructure
	// present with the complete list and absent with the sanitised one (FU-13).
	//
	// Widening the sanitiser would have traded this coverage gap for an
	// injection surface. The two uses want different lists, and they now get
	// them: safePaths still feeds RutasContexto, the transport factory and
	// git.ReadPathAtRevision below.
	plan := PlanForProfile(profile, paths, netDiff, netAtributos)
	evidence, merr := json.Marshal(map[string]any{"profile": profile, "risk": plan.Risk, "characteristics": plan.Characteristics, "validation": o.Validation})
	if merr != nil {
		return nil, fmt.Errorf("marshal net evidence: %w", merr)
	}
	var framed strings.Builder
	framed.WriteString("NET EVIDENCE (deterministic aggregates):\n" + string(evidence) + "\n\n" + netAxes + "\n")
	for _, f := range history {
		state, detail := "ACTIVE", ""
		if f.ArchiveReason != "" {
			state, detail = "ARCHIVED (excluded from the report)", " ("+f.ArchiveReason+")"
		} else if f.ClassificationError != "" {
			detail = " (classification uncertain: " + f.ClassificationError + ")"
		}
		fmt.Fprintf(&framed, "- [%s] commit %s %s/%s: %q%s\n", state, f.SHA, f.Dimension, f.Severity, f.Description, detail)
	}
	var transport ReviewTransport
	if opts.ReviewTransportFactory != nil {
		transport = opts.ReviewTransportFactory(to, safePaths)
	}
	// Standing answers recorded against the head apply SHA-bound inside the
	// engine. Answers recorded against intermediate commits carry by exact
	// fingerprint plus evidence revalidation at the head (below): they are
	// cloned to the head SHA for the engine input so the single SHA-bound
	// overlay, aggregation, and verdict downgrade stay in one place. The
	// clone is engine input only; the persisted log keeps the origin SHA.
	var rangeDispositions []FindingDisposition
	if o != nil {
		rangeDispositions = o.Dispositions
	}
	engineDispositions := mergeNetDispositionsForEngine(
		FilterDispositionsForSHA(rangeDispositions, to),
		carriedNetDispositions(rangeDispositions, revisions, to), to)
	audit := AuditarCommit(opts.Fabrica, opts.Parallel, OpcionesAuditoria{
		SHA: to, Mensaje: strings.TrimSpace(o.Intention), Diff: diff,
		Bundles: plan.Bundles, RutasContexto: safePaths,
		Respuestas: opts.Respuestas, PerfilOverride: opts.PerfilOverride,
		OnDimension: opts.OnDimension, FabricaRefutador: opts.FabricaRefutador,
		ModelVerifier:          opts.ModelVerifier,
		HallazgosDeterministas: hallazgosDeterministasParaCommit(to, opts.HallazgosDeterministasSHA, opts.HallazgosDeterministas),
		ReviewTransport:        transport,
		NetUnitLabel:           "pull request (ONE NET diff " + from + ".." + to + ")",
		NetUnitHistory:         framed.String(),
		Dispositions:           engineDispositions,
	})
	return &NetReview{From: from, To: to, Context: history, Audit: audit}, nil
}

// mergeNetDispositionsForEngine joins head-SHA answers with carried
// intermediate answers into the single SHA-bound engine input. Head
// precedence is by fingerprint: a carried answer whose fingerprint already
// has a head-SHA answer is dropped, so a stale intermediate answer can never
// override the fresher head answer inside the engine's last-wins overlay.
// The empty-fingerprint guard below is defense in depth only:
// carriedNetDispositions already drops empty-fingerprint carries, so in
// production every carried fingerprint is non-empty on entry here.
func mergeNetDispositionsForEngine(head, carried []FindingDisposition, to string) []FindingDisposition {
	out := make([]FindingDisposition, 0, len(head)+len(carried))
	out = append(out, head...)
	answered := make(map[string]struct{}, len(head))
	for _, hd := range head {
		if fp := strings.TrimSpace(hd.Fingerprint); fp != "" {
			answered[fp] = struct{}{}
		}
	}
	for _, disp := range carried {
		fp := strings.TrimSpace(disp.Fingerprint)
		if fp == "" {
			continue
		}
		if _, ok := answered[fp]; ok {
			continue
		}
		clone := disp
		clone.SHA = to
		out = append(out, clone)
	}
	return out
}

// carriedNetDispositions selects the standing human answers recorded against
// the commits in range that may clear a fresh net finding at the head.
// Matching is by exact stable fingerprint only; a disposition whose
// fingerprint matches nothing clears nothing, and an ambiguous one clears
// nothing (ApplyDispositionToResult enforces both inside the engine). Only
// answers whose evidence still holds at the head are carried: the file must
// resolve through the same safe-path sanitizer, read at the head, and still
// contain the recorded evidence under the same normalization the fingerprint
// uses. Anything unverifiable is dropped, so the net re-reports instead of
// clearing on incomplete knowledge. Dispositions recorded directly against
// the head are excluded: the engine already applies them SHA-bound.
func carriedNetDispositions(dispositions []FindingDisposition, revisions []Ficha, head string) []FindingDisposition {
	if len(dispositions) == 0 {
		return nil
	}
	inRange := make(map[string]struct{}, len(revisions))
	for _, record := range revisions {
		if strings.TrimSpace(record.SHA) != "" {
			inRange[record.SHA] = struct{}{}
		}
	}
	var out []FindingDisposition
	for _, disp := range dispositions {
		if strings.TrimSpace(disp.Fingerprint) == "" {
			continue
		}
		if _, ok := inRange[disp.SHA]; !ok {
			continue
		}
		if disp.SHA == head {
			continue
		}
		if !dispositionEvidenceHoldsAtHead(disp, head) {
			continue
		}
		out = append(out, disp)
	}
	return out
}

// dispositionEvidenceHoldsAtHead revalidates one recorded answer against the
// net head snapshot. It mirrors the evidence-containment check the ledger
// uses to discard stale evidence (motivoDescarteEvidencia): normalized
// recorded evidence must still appear in the normalized file content at the
// head. A moved line still carries because the search spans the whole file;
// removed evidence, a deleted file, or an unreadable snapshot does not.
func dispositionEvidenceHoldsAtHead(disp FindingDisposition, head string) bool {
	path := strings.TrimSpace(disp.Path)
	evidence := strings.TrimSpace(disp.Evidence)
	if path == "" || evidence == "" {
		return false
	}
	if len(RutasRevisionSeguras([]string{path})) != 1 {
		return false
	}
	content, present, err := git.ReadPathAtRevision(head, path)
	if err != nil || !present {
		return false
	}
	return contieneEvidenciaNormalizada(content, evidence)
}

const netAxes = `Evaluate explicitly beyond any per-commit review:
- Intention: does the PR deliver what its title/description promise?
- Integration: do the pieces from different commits fit together?
- Interaction between commits: does one commit undo or contradict another?
- Net regression: does the final state break something the initial state did?
- Contracts: are there undeclared breaking changes?
- Coverage: do the tests cover the NET behavior, not each step?

UNTRUSTED HISTORICAL CONTEXT (intermediate-commit findings; context only, NEVER part of this verdict):`
