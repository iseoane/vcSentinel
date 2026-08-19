package review

// ResolveBlob resolves the content-blob hash of a file, used to identify a
// question's subject across commits (e.g. after a rebase that keeps content
// identical). Injected by the caller (cmd/sentinel) because internal/review
// cannot import internal/git or internal/store without creating an import
// cycle (internal/store already imports internal/review for Hallazgo).
type ResolveBlob func(file string) (string, error)

// KnownAnswer looks up whether questionID about blob was already answered in
// a previous run. Injected by the caller for the same reason as ResolveBlob.
type KnownAnswer func(blob, questionID string) (answer string, ok bool, err error)

// SplitPendingQuestions separates already-answered questions (found via
// knownAnswer, keyed by content blob so a rebase that doesn't change content
// doesn't re-ask) from the ones still pending, and returns the known answers
// keyed by question ID so the caller can fold them into a retry round — as if
// the user had repeated them via --answer within the same invocation.
//
// A question with no File cannot be deduplicated by blob and always stays
// pending.
func SplitPendingQuestions(questions []AgentQuestion, resolveBlob ResolveBlob, knownAnswer KnownAnswer) (pending []AgentQuestion, knownAnswers map[string]string, err error) {
	knownAnswers = make(map[string]string)
	for _, q := range questions {
		if q.File == "" {
			pending = append(pending, q)
			continue
		}
		blob, blobErr := resolveBlob(q.File)
		if blobErr != nil {
			pending = append(pending, q)
			continue
		}
		answer, ok, lookupErr := knownAnswer(blob, q.ID)
		if lookupErr != nil {
			return nil, nil, lookupErr
		}
		if !ok {
			pending = append(pending, q)
			continue
		}
		knownAnswers[q.ID] = answer
	}
	return pending, knownAnswers, nil
}
