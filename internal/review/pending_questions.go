package review

// ResolveBlob resolves the content-blob hash of a file, used to identify a
// question's subject across commits (e.g. after a rebase that keeps content
// identical). Injected by the caller (cmd/sentinel) because internal/review
// cannot import internal/git or internal/store without creating an import
// cycle (internal/store already imports internal/review for Finding).
type ResolveBlob func(file string) (string, error)

// KnownAnswer looks up whether questionID about blob was already answered in
// a previous run. Injected by the caller for the same reason as ResolveBlob.
type KnownAnswer func(blob, questionID string) (answer string, ok bool, err error)

// AnsweredQuestion pairs a question with the answer already registered for
// its own content blob — kept together (not collapsed into a map keyed by
// ID alone) because AgentQuestion.ID is chosen independently by the model
// per dimension call and has no cross-dimension uniqueness guarantee; two
// different questions about different files can legitimately share an ID.
type AnsweredQuestion struct {
	Question AgentQuestion
	Answer   string
}

// SplitPendingQuestions separates already-answered questions (found via
// knownAnswer, keyed by content blob so a rebase that doesn't change content
// doesn't re-ask) from the ones still pending, and returns each answered
// question paired with its own answer (AnsweredQuestion) so the caller can
// fold them into a retry round — as if the user had repeated them via
// --answer within the same invocation — without collapsing two different
// questions that happen to share the same ID.
//
// A question with no File cannot be deduplicated by blob and always stays
// pending. A resolveBlob failure is currently indistinguishable from a
// genuine "never answered" cache miss: both silently produce "pending, no
// diagnostic" for that question — a known gap, not something this function
// tries to fix.
func SplitPendingQuestions(questions []AgentQuestion, resolveBlob ResolveBlob, knownAnswer KnownAnswer) (pending []AgentQuestion, answered []AnsweredQuestion, err error) {
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
		answered = append(answered, AnsweredQuestion{Question: q, Answer: answer})
	}
	return pending, answered, nil
}
