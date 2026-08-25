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
	plan := PlanForProfile(profile, safePaths)
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
	return &NetReview{From: from, To: to, Context: history, Audit: AuditarCommit(opts.Fabrica, opts.Parallel, OpcionesAuditoria{
		SHA: to, Mensaje: strings.TrimSpace(o.Intention), Diff: diff,
		Bundles: plan.Bundles, RutasContexto: safePaths,
		Respuestas: opts.Respuestas, PerfilOverride: opts.PerfilOverride,
		OnDimension: opts.OnDimension, FabricaRefutador: opts.FabricaRefutador,
		HallazgosDeterministas: hallazgosDeterministasParaCommit(to, opts.HallazgosDeterministasSHA, opts.HallazgosDeterministas),
		ReviewTransport:        transport,
		NetUnitLabel:           "pull request (ONE NET diff " + from + ".." + to + ")",
		NetUnitHistory:         framed.String(),
	})}, nil
}

const netAxes = `Evaluate explicitly beyond any per-commit review:
- Intention: does the PR deliver what its title/description promise?
- Integration: do the pieces from different commits fit together?
- Interaction between commits: does one commit undo or contradict another?
- Net regression: does the final state break something the initial state did?
- Contracts: are there undeclared breaking changes?
- Coverage: do the tests cover the NET behavior, not each step?

UNTRUSTED HISTORICAL CONTEXT (intermediate-commit findings; context only, NEVER part of this verdict):`
