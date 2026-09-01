# T9.4a — frozen observation-sufficiency contract

Frozen 2026-09-01 at `main` `8f4b40f`, before evaluating any axis against it.
This file is the contract. The verdict lives in the T9.4a section of
[`../f9-observabilidad.md`](../f9-observabilidad.md).

## Route: no prospective window

The task says "Evaluate the same store deterministically" — the same cumulative
store. A prospective window was considered and rejected on measured grounds:

- The store already spans 2026-08-24..2026-09-01: 9 days, 850 logical runs,
  ~94/day. Metrics snapshots start 2026-08-29: 325 in 4 days.
- `sentinel metrics` has no time filter; it accepts only `--json`. Coverage is
  cumulative over the whole store, so historical unknowns never leave the
  denominator and no waiting period removes them.
- The axes that block are blocked by missing or unreadable producers, not by
  sample size. Waiting does not create a producer.

## Disclosure

The baseline was visible when these criteria were frozen. Every criterion below
is therefore justified on grounds independent of the observed values: the
tool's own evidence policy, a calendar week, or the aggregator's own
definitions. No criterion is lowered after evaluation, and this record does not
claim a blindness it did not have.

## S-0 — classification precedes any threshold

Each axis is classified first. Only class C axes reach a sample threshold.

| Class | Meaning | Consequence |
|---|---|---|
| A | No producer, or the producer's evidence is unreadable by the aggregate | Permanently blocked until its follow-up is taken. This is not "insufficient evidence" and no waiting changes it. |
| B | Producer exists, evidence incomplete | The tool renders `null`. Blocked for T9.4b. |
| C | Producer exists, evidence complete | Eligible, subject to S-2. |

A class A axis must never be recorded as "insufficient sample". Naming the
wrong blocking reason would imply that more observation fixes it.

## S-1 — presentability

An axis is presentable only when `metrics.Ratio.Known()` or
`metrics.Measurement.Known()` is true, which requires `Coverage.Complete()`
(`observed >= total`).

Source: `internal/metrics/metrics_types.go:257`, enforced at
`cmd/sentinel/metrics_json.go:244`. This threshold is read off the
implementation, not invented here.

Consequence: no coverage threshold below 1 is admissible. Below 1 the tool
emits `null`, so there is no value to calibrate against.

## S-2 — minimum duration and volume

- Minimum duration: **7 consecutive days** of observed activity. One calendar
  week, so the sample contains the repository's real daily variation instead of
  a single day's.
- Minimum volume: **500 logical runs**. At the observed daily floor of 61 runs,
  500 runs cannot appear in fewer than 8 days, so duration stays the binding
  constraint and volume cannot be satisfied by one burst.
- Both are measured from the store's own records and recorded with the
  snapshot, because the tool exposes no time filter.

## S-3 — per-dimension calibration

A dimension is calibratable only when its disposition evidence satisfies S-1
**and** it holds at least 100 findings. At n >= 100 one further confirmed
finding moves the confirmation rate by less than one percentage point.

## S-4 — model calibration

Requires complete execution-identity coverage over measured runs **and**
complete per-model attribution over findings.

The phase already recorded the governing rule: a run served by a plain CLI
adapter contributes no observed identity, because the configured model and
effort are a declaration rather than evidence. Partial `by_model` attribution
is inadmissible on its own: different models reviewed different candidates at
different times, and the task forbids presenting correlation as causality.

## S-5 — cost calibration

Requires a cost producer carrying an explicit `Provenance.Source`. An estimate
never substitutes for a reported price.

## S-6 — candidate classes, retries, failures

Frozen as the shipped aggregator already defines them, so the evaluation is
reproducible from the tool rather than from this document:

- Included: every logical run in the local common-directory store, successes
  and failures alike. The tool offers no class filter.
- One logical run is one group key. A group holding more than one unique
  outcome counts once in `LogicalRuns` and once in `RetriedRuns`.
- `SuccessRate` is successful / (successful + failed), with `LogicalRuns` as
  the coverage total.
- `executions.failures[]` is **not admissible** as a calibration axis. It
  merges `agentrun.OutcomeClass` (population 850) with `store.FailureClass`
  (population 325) into one flat list with no source tag and no coverage
  field, and it double-counts terminal classes.

## S-7 — blocking rule

An insufficient axis blocks only its own calibration axis. T9.4b opens if at
least one axis is sufficient. No threshold above is lowered after evaluation.

## S-8 — right-censoring

Execution duration is right-censored by whatever review timeout applied to each
run, and the store does not record the effective budget per run. Any use of the
duration distribution must state that censoring rather than reading a quantile
as if it were uncensored.
