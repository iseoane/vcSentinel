# 03b: Narrow The Concurrency Detector The Same Way

**What to build:** `sentinel explain` stops reporting `concurrency` because a
Markdown document mentions `context.` or `chan `. The detector keeps firing on
real code.

**Blocked by:** 03.

**Blocks:** 04. Migrating the planner before this lands would import the false
positive wholesale.

**Status:** complete.

## Why this exists

Discovered by ticket 03's measurement, not predicted. Feeding the planner the
shared evidence takes prose-only commits from 43 to 119 agent invocations, and
17 of those 52 commits are unlocked by `concurrency` alone.

`detectarConcurrencia` matches `\bgo `, `\bsync\.`, `\bchan ` and `\bcontext\.`
in the added lines of **every** path, with no path class filter. That is exactly
the defect ticket 02 removed from the security detector, still present in this
one. The reengineering documentation in this repository discusses Go
concurrency constantly, so every ficha that mentions `context.` currently reads
as a concurrent change.

The word-boundary guard already on these marks is a different fix for a
different problem: it stops `miscontext.` matching inside a longer identifier.
It does nothing about a sentence in a Markdown file that legitimately contains
`context.Background()`.

Reuse the deny-list ticket 02 introduced rather than writing a second one. Two
detectors excluding the same classes by two mechanisms is how the FU-10 defect
started.

## Scope note

Only the concurrency detector. `detectarCambioDeComportamiento` already filters
by class, and `detectarCodigoGenerado` reads added lines deliberately, because a
generation marker in prose is still weak evidence of generated content and that
detector's own class check runs first.

**Acceptance criteria:**

- [x] A focused RED test exists before the production change and fails for the
      stated reason.
- [x] A Markdown file whose added lines contain `context.Background()` no longer
      marks `concurrency` present.
- [x] A generated file whose added lines contain `chan ` no longer marks it
      present.
- [x] A source file whose added lines contain `go ejecutar()` or `context.`
      still marks it present, and the existing Spanish-word false-positive
      guard still holds.
- [x] A config-class file still marks it present, keeping the deny-list
      behaviour consistent with the security detector.
- [x] The exclusion reuses the class list ticket 02 introduced; no second
      deny-list is created.
- [x] `go build ./...`, `go vet ./...`, focused tests, and the full suite pass,
      with the exact commands and outcomes recorded.
- [x] The divergence harness is re-run and the corrected source-bearing delta is
      recorded in the evidence document, replacing the 358 figure.
- [x] `sentinel review` of the commit completes with every finding either
      resolved or dispositioned with a verified premise and a stated reason.

## Evidence

- RED first: `go test ./internal/change -run TestConcurrency` failed on the
  Markdown and generated cases and passed on the three positive ones, so the
  test was not green by inversion.
- GREEN after the change: `go test ./internal/change` passes, including the
  pre-existing Spanish-word false-positive guard.
- Both detectors now exclude through one helper over the existing class list.
  No second deny-list was created.
- Re-measurement with the corrected harness, pinned at `fce2da5`: prose-only
  commits moved 43 to 119 before this change and move 48 to 54 after it.
  Source-bearing commits are 304 to 364; the earlier 358 figure is replaced in
  the evidence document.
- `go build ./...`, `go vet ./...`, and the full suite pass.
- Implementation commit: `4c1d5b3`.
