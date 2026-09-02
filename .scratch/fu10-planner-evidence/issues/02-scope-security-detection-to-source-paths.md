# 02: Scope Content-Based Security Detection To Source Paths

**What to build:** `sentinel explain` stops reporting `security_sensitive`
because a documentation, data, or generated file happens to contain a
security-sounding word in its added lines. A real source change that adds
credential handling still reports it.

**Blocked by:** 01.

**Status:** ready-for-agent.

## Why this shape

FU-10 demonstrates the defect with commit `780c900`, a documentation commit
holding a metrics artifact: the substring `token` matched inside
`cached_input_tokens`, so `explain` reported `high por security_sensitive
presente` for a documentation change.

Whole-word matching is the wrong fix and must not be used. In Go identifiers
`_` and camelCase humps are not word boundaries, so a `\b`-anchored key rejects
`cached_input_tokens` but also rejects `accessToken` and `authToken` — trading a
false positive for a worse false negative on exactly the case the detector
exists to catch.

The discriminator is the path class, not the lexeme. The content-based security
detector scans the added lines of every path. The behaviour-change detector, in
the same file, already filters to source-class paths. That asymmetry is the
defect. Closing it narrows one detector, which is permitted; FU-10 forbids
widening one, which this is not.

Path-pattern detection is a separate input and stays as it is: a sensitive path
marks the characteristic present regardless of what its content says.

**Acceptance criteria:**

- [ ] A focused RED test exists before the production change, and it fails for
      the stated reason rather than for a missing symbol.
- [ ] A documentation-class file whose added lines contain `cached_input_tokens`
      no longer marks `security_sensitive` present.
- [ ] A source-class file whose added lines contain `accessToken` or
      `authToken` still marks it present.
- [ ] Detection driven by sensitive path patterns is unchanged and proved
      unchanged by test.
- [ ] `sentinel explain` on `a1a5803..780c900` no longer reports
      `high por security_sensitive presente`.
- [ ] `go build ./...`, `go vet ./...`, the focused package tests, and the full
      suite pass, with the exact commands and outcomes recorded.
- [ ] `sentinel review` of the commit completes with no unresolved finding.
