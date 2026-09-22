---
name: vcsentinel
description: "Trigger: vcSentinel, worktree volume, staged commits, review, gate, pull request, durable runs. Guide repository changes without bypassing human decisions."
license: Apache-2.0
metadata:
  author: "iseoane"
  version: "1.0"
---

<!-- vcsentinel:managed-skill -->

## Activation Contract

Load for repository changes governed by vcSentinel, large-change planning,
commit review, lifecycle validation, pull requests, or durable runs.

## Hard Rules

- Before making changes or proposing a plan, run vcsentinel check. It measures
  the whole worktree and is advisory, including when the state is CRITICAL.
- The pre-commit hook runs vcsentinel check --staged. More than 400 authored
  lines in the staged candidate are rejected.
- Do not commit, publish, answer decisions, bypass a gate, invent a verdict,
  or clear findings on the human's behalf.

## Decision Gates

| Situation | Action |
| --- | --- |
| slice plan reports pending_decisions or exits 3 | Show decisions verbatim; wait for the human's bypass or abort answer. |
| review | review audits commits and records findings and verdicts; never silently clear a finding. |
| gate | gate runs deterministic validation; it does not replace semantic review. |
| pr review / pr create | pr review records branch evidence; pr create publishes only after requirements and human approval. |
| runs | runs controls and observes durable execution; status, logs, verification, and delivery remain human-controlled. |

## Execution Steps

1. Run vcsentinel check before changing files or proposing a plan.
2. Create a plan without committing:

~~~sh
vcsentinel slice plan --json > plan.json
~~~

3. If decisions exist, show them verbatim and wait. Write only the human's
   literal bypass or abort answers to answers.json. Otherwise write
   {"plan_id":"<plan_id>","answers":{}} to answers.json.
4. Apply the answered plan:

~~~sh
vcsentinel slice apply --plan plan.json --answers answers.json
~~~

5. Follow help and exit codes; keep review, gate, pull-request, and run
   evidence visible. Never invent authority or bypass human control.

## Output Contract

Return commands and observed results, decisions and human answers,
review/gate/pull-request/run evidence, blockers, and what was not run. Never
claim approval, publication, or a semantic verdict without authoritative
evidence.

## References

- ../../../AGENTS.md — repository volume, slicing, and lifecycle rules.
- ../../../docs/design/runs-cli.md — durable-run commands and exit codes.
