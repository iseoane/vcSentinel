package review

// SupersedeDeterministicFindings removes semantic findings (Source ==
// SourceReview) that report the same location as a deterministic one
// (Source == SourceValidation, e.g. gofmt/go vet/go test): if the linter or
// build already caught it, the LLM's guess about the same spot adds nothing
// and only produces noise (T6.2).
//
// deterministic is untouched — a deterministic finding is never superseded,
// there is nothing more authoritative than an exit code — and stays the
// caller's responsibility to append to the final result. Only semantic is
// filtered, so a caller that also aggregates semantic findings (T6.1) can
// choose the order: superseding before aggregating avoids a deterministic
// finding ever being merged into a semantic one by proximity.
func SupersedeDeterministicFindings(semantic, deterministic []Hallazgo) []Hallazgo {
	if len(deterministic) == 0 {
		return semantic
	}
	kept := make([]Hallazgo, 0, len(semantic))
	for _, finding := range semantic {
		if !supersededByAny(finding.Location, deterministic) {
			kept = append(kept, finding)
		}
	}
	return kept
}

func supersededByAny(location Ubicacion, deterministic []Hallazgo) bool {
	for _, d := range deterministic {
		if sameLocation(location, d.Location) {
			return true
		}
	}
	return false
}

// sameLocation considers a semantic finding superseded when it shares the
// deterministic finding's file and, if the deterministic one has line
// information, an overlapping range (reusing T6.1's overlap check). A
// deterministic finding with no line (LineaInicio <= 0, e.g. `gofmt -l`,
// which only lists bare file paths) covers the whole file: nothing about
// that file's formatting needs a second, semantic opinion.
func sameLocation(semantic, deterministic Ubicacion) bool {
	if semantic.Archivo == "" || deterministic.Archivo == "" || semantic.Archivo != deterministic.Archivo {
		return false
	}
	if deterministic.LineaInicio <= 0 {
		return true
	}
	return sourceRangesOverlap(semantic, deterministic)
}
