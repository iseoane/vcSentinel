package store

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

// Decision is a human decision recorded in decisions.jsonl. The same
// five-field schema covers three distinct uses (T7.5), distinguished by the
// value of Decision instead of a separate "type" field:
//   - Decision about a concrete finding: Fingerprint = fingerprint of the
//     finding, Decision = e.g. "accept"/"reject".
//   - Bypass of --force (M3 report): empty Fingerprint (there is no single
//     finding being overridden, but the validation of a whole run),
//     Decision = "force_bypass", Reason = the user's --reason,
//     Scope = where the bypass happened (e.g. "pr-create").
//   - Answer to an agent question: Fingerprint = answerKey(blob,
//     questionID) (see RecordAnswer in question.go for the exact format,
//     with a length prefix per component), Decision =
//     "question_answered", Reason = the text of the answer.
type Decision struct {
	Fingerprint string    `json:"fingerprint"`
	Decision    string    `json:"decision"`
	Actor       string    `json:"actor"`
	At          time.Time `json:"at"`
	Reason      string    `json:"reason,omitempty"`
	Scope       string    `json:"scope,omitempty"`
}

// DecisionForceBypass and ScopePrCreate are the exported vocabulary of the
// --force bypass (M3 report), so that a caller outside this package
// (cmd/vcsentinel) does not compose the literals by hand: a typo in a loose
// literal would produce a row that no reader of decisions.jsonl recognizes,
// with no compilation error to reveal it.
const (
	DecisionForceBypass = "force_bypass"
	ScopePrCreate       = "pr-create"
)

// RecordDecision appends a line to decisions.jsonl. Unlike
// units/runs/findings/commits, this file is append-only by nature: each
// line is an immutable fact about the past, never re-read to replace a
// previous record. That is why it does NOT use the temp+rename pattern of
// writeJSON (which assumes "there is a last valid state to replace"): it
// opens with O_APPEND|O_CREATE|O_WRONLY, which in POSIX guarantees that
// each write() lands at the end of the file as an atomic operation, without
// interleaving with another concurrent writer.
func (s *Store) RecordDecision(d *Decision) error {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.decisionsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

// ReadDecisions returns every recorded decision, in write order. A missing
// file returns (nil, nil); a corrupt line is an explicit error, like in the
// rest of the store.
func (s *Store) ReadDecisions() ([]Decision, error) {
	data, err := os.ReadFile(s.decisionsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var decisions []Decision
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var d Decision
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			return nil, err
		}
		decisions = append(decisions, d)
	}
	return decisions, nil
}
