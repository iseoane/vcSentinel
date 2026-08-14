package review

import "path"

// SupersedeDeterministicFindings removes semantic findings (Source ==
// SourceReview) that a deterministic one (Source == SourceValidation, e.g.
// gofmt/go vet) already reports for the exact same category of problem in
// the same location: if the linter already caught it, the LLM's guess about
// the same spot adds nothing and only produces noise (T6.2).
//
// Equivalence requires BOTH the same Dimension (so a formatting finding can
// never discard an unrelated security/logic finding just because they share
// a line) AND an overlapping location. A deterministic finding with an empty
// Dimension supersedes nothing: callers that cannot map a validation
// capability to a specific semantic dimension must leave it empty rather
// than have it silently swallow unrelated findings.
//
// Any finding whose Source isn't exactly SourceReview/SourceValidation is
// left untouched by this function on either side — the guarantee doesn't
// depend on the caller only ever passing those two sources.
//
// deterministic is never removed — there is nothing more authoritative than
// an exit code — and stays the caller's responsibility to append to the
// final result. Only semantic is filtered, so a caller that also aggregates
// semantic findings (T6.1) can choose the order: superseding before
// aggregating avoids a deterministic finding ever being merged into a
// semantic one by proximity.
func SupersedeDeterministicFindings(semantic, deterministic []Hallazgo) []Hallazgo {
	var applicable []Hallazgo
	for _, d := range deterministic {
		if d.Source == SourceValidation && d.Dimension != "" {
			applicable = append(applicable, d)
		}
	}
	if len(applicable) == 0 {
		return semantic
	}

	kept := make([]Hallazgo, 0, len(semantic))
	for _, finding := range semantic {
		if finding.Source == SourceReview && supersededByAny(finding, applicable) {
			continue
		}
		kept = append(kept, finding)
	}
	return kept
}

func supersededByAny(finding Hallazgo, deterministic []Hallazgo) bool {
	for _, d := range deterministic {
		if finding.Dimension == d.Dimension && sameLocation(finding.Location, d.Location) {
			return true
		}
	}
	return false
}

// sameLocation considers a finding superseded when it shares the
// deterministic finding's file (compared after normalizePath, so "./a.go"
// and "a.go" match) and, if the deterministic one has line information, an
// overlapping range (reusing T6.1's overlap check). A deterministic finding
// with no line (LineaInicio <= 0, e.g. `gofmt -l`, which only lists bare
// file paths) covers the whole file: nothing about that file needs a second
// opinion within the same category.
func sameLocation(semantic, deterministic Ubicacion) bool {
	if semantic.Archivo == "" || deterministic.Archivo == "" {
		return false
	}
	if normalizePath(semantic.Archivo) != normalizePath(deterministic.Archivo) {
		return false
	}
	if deterministic.LineaInicio <= 0 {
		return true
	}
	return sourceRangesOverlap(semantic, deterministic)
}

// normalizePath makes common equivalent path spellings ("./a.go" vs "a.go")
// compare equal without attempting to resolve symlinks or relativize against
// an arbitrary root: both sides of this comparison already come from git
// output rooted at the same worktree.
func normalizePath(p string) string {
	return path.Clean(p)
}
