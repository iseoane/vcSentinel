package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/review"
)

// dispositionsFile is the append-only log of human finding dispositions,
// beside decisions.jsonl under <gitCommonDir>/vcsentinel. It is separate
// from the immutable review revisions by design: answering a finding never
// rewrites the revision that reported it.
const dispositionsFile = "dispositions.jsonl"

// AppendDisposition records one human finding disposition at the end of the
// log. Like decisions.jsonl this file is append-only by nature: every line
// is an immutable fact about the past, so it opens with
// O_APPEND|O_CREATE|O_WRONLY instead of the temp+rename pattern. The record
// is normalised in place before writing: surrounding whitespace is trimmed,
// the status is canonicalised, and a missing timestamp defaults to now.
func (s *Store) AppendDisposition(d *review.FindingDisposition) error {
	record, err := normalizeAndValidateDisposition(d)
	if err != nil {
		return err
	}
	if record.At.IsZero() {
		record.At = time.Now().UTC()
	}
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(&record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, dispositionsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	*d = record
	return nil
}

// normalizeAndValidateDisposition is the single persisted-schema boundary
// for the append-only human disposition log. Reads use it too: accepting a
// line here only on write would let corrupted history create a disposition
// that projections could interpret as a real human answer.
func normalizeAndValidateDisposition(d *review.FindingDisposition) (review.FindingDisposition, error) {
	if d == nil {
		return review.FindingDisposition{}, errors.New("store: nil finding disposition")
	}
	record := *d
	record.SHA = strings.TrimSpace(record.SHA)
	record.Fingerprint = strings.TrimSpace(record.Fingerprint)
	record.Status = review.NormalizeStatus(record.Status)
	record.Actor = strings.TrimSpace(record.Actor)
	record.Source = strings.TrimSpace(record.Source)
	if record.SHA == "" {
		return review.FindingDisposition{}, errors.New("store: finding disposition without reviewed SHA")
	}
	if record.Fingerprint == "" {
		return review.FindingDisposition{}, errors.New("store: finding disposition without fingerprint")
	}
	switch record.Status {
	case review.StatusRefuted, review.StatusAcceptedByUser, review.StatusFixed, review.StatusReopened:
	default:
		return review.FindingDisposition{}, fmt.Errorf("store: finding disposition with unknown status %q", d.Status)
	}
	if record.Actor != review.RefutationActorHuman {
		return review.FindingDisposition{}, fmt.Errorf("store: finding disposition with non-human actor %q", d.Actor)
	}
	if record.Source != review.DispositionSourceHuman {
		return review.FindingDisposition{}, fmt.Errorf("store: finding disposition with non-human source %q", d.Source)
	}
	return record, nil
}

// ReadDispositions returns every recorded disposition in write order. A log
// that was never written returns (nil, nil); a corrupt line is an explicit
// error, never a silent skip: every consumer of these answers fails closed.
func (s *Store) ReadDispositions() ([]review.FindingDisposition, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, dispositionsFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []review.FindingDisposition
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var d review.FindingDisposition
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			return nil, fmt.Errorf("store: corrupt finding disposition on line %d: %w", i+1, err)
		}
		record, err := normalizeAndValidateDisposition(&d)
		if err != nil {
			return nil, fmt.Errorf("store: corrupt finding disposition on line %d: %w", i+1, err)
		}
		out = append(out, record)
	}
	return out, nil
}

// ReadDispositionsForSHA returns the dispositions recorded against one
// reviewed SHA, in write order. A corrupt log fails the whole read: a
// caller that continued with a partial answer set could clear a block on
// incomplete evidence.
func (s *Store) ReadDispositionsForSHA(sha string) ([]review.FindingDisposition, error) {
	all, err := s.ReadDispositions()
	if err != nil {
		return nil, err
	}
	return review.FilterDispositionsForSHA(all, sha), nil
}
