package metrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// ReadStore is the narrow source reader for the aggregation package. Retained
// metrics are scanned independently of execution directories so snapshots
// remain visible after execution pruning.
func ReadStore(gitCommonDir string) (Input, error) {
	if strings.TrimSpace(gitCommonDir) == "" {
		return Input{}, errors.New("metrics: git common directory is empty")
	}
	var input Input
	if err := readLedger(gitCommonDir, &input); err != nil {
		return Input{}, err
	}
	if err := readStoredFindings(gitCommonDir, &input); err != nil {
		return Input{}, err
	}

	st := store.NuevoStore(gitCommonDir)
	decisions, err := st.LeerDecisiones()
	if err != nil {
		return Input{}, fmt.Errorf("metrics: read decisions: %w", err)
	}
	input.Decisions = append(input.Decisions, decisions...)

	ids, err := st.ListExecutionIDs()
	if err != nil {
		return Input{}, fmt.Errorf("metrics: list executions: %w", err)
	}
	metricIDs, err := listRetainedMetricIDs(gitCommonDir)
	if err != nil {
		return Input{}, fmt.Errorf("metrics: list retained metrics: %w", err)
	}
	idSet := make(map[string]struct{}, len(ids)+len(metricIDs))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}
	for _, id := range metricIDs {
		idSet[id] = struct{}{}
	}
	allIDs := make([]string, 0, len(idSet))
	for id := range idSet {
		allIDs = append(allIDs, id)
	}
	sort.Strings(allIDs)
	for _, id := range allIDs {
		metricsSnapshot, readErr := st.ReadExecutionMetrics(id)
		if readErr != nil {
			return Input{}, fmt.Errorf("metrics: read execution metrics %q: %w", id, readErr)
		}
		observation := ExecutionObservation{RunID: id, Metrics: metricsSnapshot}
		// A retained-only snapshot has no execution directory. Reading
		// outcomes for it would turn valid historical evidence into a
		// not-found error, so use the execution listing as the authority.
		if containsSorted(ids, id) {
			outcomes, outcomeErr := st.ReadAttemptOutcomes(id)
			if outcomeErr != nil {
				return Input{}, fmt.Errorf("metrics: read outcomes %q: %w", id, outcomeErr)
			}
			observation.Outcomes = outcomes
		}
		input.Executions = append(input.Executions, observation)
	}

	events, err := ops.UltimosEventos(gitCommonDir, 0)
	if err != nil {
		return Input{}, fmt.Errorf("metrics: read events: %w", err)
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	input.Events = events
	for _, event := range events {
		if stage, ok := stageFromEvent(event); ok {
			input.Stages = append(input.Stages, stage)
		}
		if remediation, ok := remediationFromEvent(event); ok {
			input.Remediations = append(input.Remediations, remediation)
		}
	}
	return input, nil
}

func readLedger(gitCommonDir string, input *Input) error {
	ledger := review.NuevoLedger(gitCommonDir)
	shas, err := ledger.ListarFichas()
	if err != nil {
		return fmt.Errorf("metrics: list review fichas: %w", err)
	}
	// Standing human answers are overlaid through the same domain projection
	// every other consumer uses. A corrupt dispositions log fails the whole
	// report rather than measuring a population with missing answers.
	dispositions, err := store.NuevoStore(gitCommonDir).ReadDispositions()
	if err != nil {
		return fmt.Errorf("metrics: read dispositions: %w", err)
	}
	for _, sha := range shas {
		ficha, readErr := ledger.LeerFicha(sha)
		if readErr != nil {
			return fmt.Errorf("metrics: read review ficha %q: %w", sha, readErr)
		}
		if ficha == nil {
			continue
		}
		for revisionIndex, revision := range ficha.Revisions {
			// FindingsWithDispositions, not HallazgosEfectivos: the latter
			// is the blocking gate's selection point and reports no
			// lifecycle status at all, so reading through it measures a
			// disposition-free ledger (FU-7).
			findings := review.ApplyDispositions(revision.FindingsWithDispositions(), review.FilterDispositionsForSHA(dispositions, sha))
			for _, finding := range findings {
				fingerprint := finding.Fingerprint
				if fingerprint == "" {
					fingerprint = review.Fingerprint(finding)
				}
				input.Findings = append(input.Findings, FindingObservation{
					Fingerprint: fingerprint, Commit: sha, Revision: revisionIndex,
					At: revision.At, Origin: "ledger", Finding: finding,
				})
				// Interpreted through review.NormalizeStatus, never compared
				// raw: the aggregated finding's status is persisted as its
				// producer wrote it, so a padded or differently-cased value
				// would otherwise be excluded from the effective population
				// by aggregateFindings while still counting as remediated
				// here — the two halves disagreeing about one record.
				status := review.NormalizeStatus(finding.Status)
				// A refuted finding was never a defect, so a revision that
				// fixed its siblings did not remediate it. Before FU-7 made
				// the refuted findings reachable this branch could not see
				// one.
				if (revision.Fixed && status != review.StatusRefuted) || status == review.StatusFixed {
					input.Remediations = append(input.Remediations, RemediationObservation{
						Target: fingerprint, LogicalID: "fixed:" + fingerprint + ":" + ficha.FixedIn,
						Dimension: finding.Dimension, Success: true, At: revision.At,
					})
				}
				if status == review.StatusReopened {
					input.Remediations = append(input.Remediations, RemediationObservation{
						Target: fingerprint, LogicalID: "reopened:" + fingerprint + ":" + revision.At.UTC().Format(time.RFC3339Nano),
						Dimension: finding.Dimension, Success: false, At: revision.At,
					})
				}
			}
		}
	}
	return nil
}

func readStoredFindings(gitCommonDir string, input *Input) error {
	directory := filepath.Join(gitCommonDir, "vas-sentinel", "findings")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			return fmt.Errorf("metrics: read stored finding %q: %w", entry.Name(), readErr)
		}
		var finding review.Hallazgo
		if unmarshalErr := json.Unmarshal(data, &finding); unmarshalErr != nil {
			return fmt.Errorf("metrics: decode stored finding %q: %w", entry.Name(), unmarshalErr)
		}
		fingerprint := strings.TrimSuffix(entry.Name(), ".json")
		if finding.Fingerprint != "" {
			fingerprint = finding.Fingerprint
		}
		input.Findings = append(input.Findings, FindingObservation{Fingerprint: fingerprint, Origin: "store", Finding: finding})
	}
	return nil
}

func listRetainedMetricIDs(gitCommonDir string) ([]string, error) {
	directory := filepath.Join(gitCommonDir, "vas-sentinel", "metrics", "v1")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		ids = append(ids, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}

func containsSorted(values []string, needle string) bool {
	index := sort.SearchStrings(values, needle)
	return index < len(values) && values[index] == needle
}

func eventFields(event ops.Evento) (map[string]any, bool) {
	switch fields := event.Detail.(type) {
	case map[string]any:
		return fields, true
	case ops.EventDetail:
		return map[string]any(fields), true
	default:
		return nil, false
	}
}

func stageFromEvent(event ops.Evento) (StageObservation, bool) {
	fields, ok := eventFields(event)
	if !ok {
		return StageObservation{}, false
	}
	stage := stringField(fields, "stage", "stage_id", "capability", "capability_id")
	duration, ok := intField(fields, "duration_ns", "duration_nanos", "duration")
	if !ok || stage == "" || duration < 0 {
		return StageObservation{}, false
	}
	logicalRunID := strings.TrimSpace(stringField(fields, "logical_run_id", "run_id", "execution_id", "execution_run_id"))
	return StageObservation{Stage: stage, DurationNanos: duration, LogicalRunID: logicalRunID}, true
}

func remediationFromEvent(event ops.Evento) (RemediationObservation, bool) {
	fields, ok := eventFields(event)
	if !ok {
		return RemediationObservation{}, false
	}
	kind := strings.ToLower(stringField(fields, "kind", "type", "operation"))
	if kind != "remediation" && kind != "fix" && kind != "repair" && fields["remediation"] != true {
		return RemediationObservation{}, false
	}
	success, ok := boolField(fields, "success", "succeeded")
	if !ok {
		success = event.Exit == 0
	}
	logicalID := stringField(fields, "logical_id", "remediation_id", "attempt_id", "invocation_id")
	target := stringField(fields, "fingerprint", "target")
	dimension := stringField(fields, "dimension")
	if logicalID == "" {
		logicalID = eventRemediationIdentity(event, target, dimension, success)
	}
	return RemediationObservation{
		Target: target, LogicalID: logicalID,
		Dimension: dimension, Success: success, At: event.At,
	}, true
}

func eventRemediationIdentity(event ops.Evento, target, dimension string, success bool) string {
	data, _ := json.Marshal(struct {
		At        string `json:"at"`
		Cmd       string `json:"cmd"`
		Target    string `json:"target"`
		Dimension string `json:"dimension"`
		Success   bool   `json:"success"`
	}{
		At: event.At.UTC().Format(time.RFC3339Nano), Cmd: event.Cmd,
		Target: target, Dimension: dimension, Success: success,
	})
	return "event:" + string(data)
}

func stringField(fields map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := fields[name].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func intField(fields map[string]any, names ...string) (int64, bool) {
	for _, name := range names {
		switch value := fields[name].(type) {
		case float64:
			if value >= 0 && value <= math.MaxInt64 && math.Trunc(value) == value {
				return int64(value), true
			}
		case int:
			if value >= 0 {
				return int64(value), true
			}
		case int64:
			if value >= 0 {
				return value, true
			}
		case json.Number:
			parsed, err := strconv.ParseInt(string(value), 10, 64)
			if err == nil && parsed >= 0 {
				return parsed, true
			}
		}
	}
	return 0, false
}

func boolField(fields map[string]any, names ...string) (bool, bool) {
	for _, name := range names {
		if value, ok := fields[name].(bool); ok {
			return value, true
		}
	}
	return false, false
}
