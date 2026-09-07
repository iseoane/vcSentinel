package store

import (
	"strconv"
	"strings"
	"time"
)

// decisionAnswerQuestion is the Decision discriminator value for answers to
// agent questions (see the Decision comment).
const decisionAnswerQuestion = "question_answered"

// scopeAnswerQuestion identifies the context of an answer recorded by
// RecordAnswer. It is a fixed value (no more granularity is needed today:
// the blob+questionID key in Fingerprint already identifies uniquely what
// was answered); if in the future distinguishing a dimension or another
// context becomes necessary, this is the field to specialize.
const scopeAnswerQuestion = "question"

// answerKey builds the deterministic Fingerprint key for an answer: it
// combines blob + questionID so that the same question about the same
// content is recognized as already answered even if the commit SHA changes
// (e.g. after a rebase), just like the F2 blob indexes do for findings.
// Each component is prefixed by its decimal length (same pattern as
// review's packWithLengthPrefixes): a simple separator like "#" would be
// ambiguous if blob or questionID contained it literally — e.g.
// answerKey("X#question:q1", "q2") would collide with answerKey("X",
// "q1#question:q2") without this prefix, suppressing a question different
// from the one actually answered.
func answerKey(blob, questionID string) string {
	var b strings.Builder
	for _, c := range []string{blob, questionID} {
		b.WriteString(strconv.Itoa(len(c)))
		b.WriteByte(':')
		b.WriteString(c)
	}
	return b.String()
}

// RecordAnswer persists that questionID about blob was answered by actor
// with answer, as a Decision with Fingerprint = answerKey(blob,
// questionID) and Decision = "question_answered".
//
// decisions.jsonl is an UNAUTHENTICATED LOCAL record (same 0644 mode as
// the rest of the store) and Actor is self-declared attribution (see
// resolveActor in cmd/sentinel): anyone with write permission on the
// repository can seed a question_answered line in advance and suppress a
// real question. Acceptable today because nothing reads it yet (see the
// next paragraph); before wiring this into a real interactive flow, check
// whether that property suffices or whether the answer needs something
// stronger than "it is in the file".
//
// Deliberately NOT wired yet into cmd/sentinel/review, gate or pr review:
// today there is no interactive question loop in the CLI
// (internal/review/engine.go fills AuditResult.Questions, but no command
// of cmd/sentinel reads or prints it; --answer is just an extra round
// inside the same process, not something the CLI triggers upon detecting a
// pending question across separate invocations). This function is the
// persistence primitive ready to be wired when that interactive surface
// exists, following the same "no second consumer yet" pattern already used
// for several pieces of T7.1-T7.4.
func (s *Store) RecordAnswer(blob, questionID, answer, actor string) error {
	return s.RecordDecision(&Decision{
		Fingerprint: answerKey(blob, questionID),
		Decision:    decisionAnswerQuestion,
		Actor:       actor,
		At:          time.Now().UTC(),
		Reason:      answer,
		Scope:       scopeAnswerQuestion,
	})
}

// RecordedAnswer reports whether questionID about blob was already answered
// in a previous run, and returns that answer if so. If the same question
// was answered more than once (not expected in the normal flow, but
// decisions.jsonl is append-only and does not prevent it), the most recent
// answer is returned.
//
// See the RecordAnswer comment: this pair is a persistence primitive,
// deliberately not wired yet to any CLI command.
func (s *Store) RecordedAnswer(blob, questionID string) (answer string, ok bool, err error) {
	decisions, err := s.ReadDecisions()
	if err != nil {
		return "", false, err
	}
	key := answerKey(blob, questionID)
	for _, d := range decisions {
		if d.Decision == decisionAnswerQuestion && d.Fingerprint == key {
			answer, ok = d.Reason, true
		}
	}
	return answer, ok, nil
}
