// Package metrics provides deterministic, read-only aggregation over the
// durable review and execution evidence stored by vas-sentinel.
package metrics

const unknownLabel = "unknown"

// Aggregate folds input without I/O. It is deterministic for equivalent input
// regardless of slice order, deduplicates logical findings and retries, keeps
// currencies in separate rows, and preserves unavailable measurements as nil.
func Aggregate(input Input) Report {
	findings := aggregateFindings(input.Findings, input.Decisions)
	remediation := aggregateRemediations(input.Remediations)
	executions, costs, stages := aggregateExecutions(input.Executions, input.Stages, findings.Confirmed)
	return Report{Findings: findings, Remediation: remediation, Executions: executions, Costs: costs, Stages: stages}
}

// AggregateStore reads all available ledger, decision, execution, event, and
// retained metrics evidence from a Git common directory and aggregates it.
func AggregateStore(gitCommonDir string) (Report, error) {
	input, err := ReadStore(gitCommonDir)
	if err != nil {
		return Report{}, err
	}
	return Aggregate(input), nil
}
