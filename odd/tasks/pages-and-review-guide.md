# Publish the diagram and explain review stages

## Objective
Make the existing Archify HTML diagram browser-readable through GitHub Pages and clearly explain the scope of `vcsentinel review`, `vcsentinel gate`, and `vcsentinel pr review` in both README translations.

## Scope and constraints
- The user has reauthorized Pages publication and explicitly enabled GitHub Actions Pages for the now-public `iseoane/vcSentinel` repository. Publish only `docs/diagrams/vcsentinel-multi-commit-pr.html` in the Pages artifact; do not publish other repository content.
- The repository may move again: explain that the owner-specific Pages URL would need updating after a transfer.
- The Spanish README remains the explicit translation exception; new workflow and task documentation stay in English.
- Authorized edit surfaces: `README.md`, `README.es.md`, `.github/workflows/pages-diagram.yml`, this task document. Do not change the diagram bytes or existing CI workflow.
- Route: delegated writer for multi-file authoring; independent verification of workflow content boundaries and README facts.
- TDD: not applicable to docs and declarative workflow; verify links, YAML structure and a live deployment after push.
- Delivery: one documentation/deployment work-unit commit on `docs/pages-and-review-guide`, then fast-forward main and push to launch Pages (user requested the live HTML); record CI and Pages outcomes separately. Unrelated CI TempDir failure is not silently waived or changed here.

## Tasks
- [x] P1: Explain risk-dependent review dimensions, current configured gate profile and branch net-diff review in English and Spanish. Check: independent verifier matched source contracts and translation.
- [x] P2: Add minimal least-privilege Pages workflow that publishes only the approved HTML and change both README links to its expected deployment URL. Check: independent verifier checked artifact scope, pins, permissions and YAML. Live URL remains pending until deployment.
- [x] P3: Commit the work unit, fast-forward/push main, watch Pages deployment and CI, and report any unresolved failures. Commit: `27fcf48`; Pages run `36127320154` passed; CI run `36127320152` passed.

## Progress and evidence
- Writer has already modified both README files to expand review/gate/pr review semantics; verification is pending.
- Pages API confirms `https://iseoane.github.io/vcSentinel/`, build_type `workflow`, public true. Before any deploy, homepage and diagram return 404, and only CI is registered as a workflow.
- Previous Pages setup attempts returned HTTP 422 while private and HTTP 404 under the old owner; the user has since enabled Pages from the new owner account.
- Main CI run 36109082529 failed in unrelated `internal/execution` TempDir cleanup; that defect is not part of this documentation/Pages unit.
- P1/P2 completed by delegated writer. Independent verifier checked role dimensions, gate configuration, branch review, translation, action pins, workflow artifact scope and YAML structure. Writer ran `vcsentinel check` and `git diff --check`; both passed.
- Committed `27fcf48` (`docs(review): explain audit stages and publish diagram`) and fast-forwarded/pushed `main` to `iseoane/vcSentinel`.
- GitHub Pages run `36127320154` passed. `https://iseoane.github.io/vcSentinel/vcsentinel-multi-commit-pr.html` returned HTTP 200 and `text/html`; unrelated `docs/design/runs-cli.md` and the diagram JSON returned HTTP 404 on Pages.
- GitHub CI run `36127320152` passed formatting, vet, tests and build. Previous `internal/execution` TempDir cleanup failures were intermittent, not repaired by this documentation work.

## Next step
No further change required for this documentation unit. Investigate the independent intermittent TempDir CI failure as a separate task if requested.
