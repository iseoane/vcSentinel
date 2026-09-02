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

Whole-word matching does not belong in this ticket. Measured against the real
key list, `\b`-anchoring does reject `cached_input_tokens`, and it still catches
bare identifiers and string keys such as `token = os.Getenv(...)` and `"auth":`.
But it is insufficient on its own: in Go identifiers neither `_` nor a camelCase
hump is a word boundary, so it also rejects `accessToken` and `refreshTokens`,
the dominant spelling for credential identifiers. It is orthogonal to the defect
FU-10 demonstrates, which is in a documentation file, and adding it here would
only lose detections in source files. Left out deliberately, not overlooked.

The discriminator is the path class, not the lexeme. The content-based security
detector scans the added lines of every path. The behaviour-change detector, in
the same file, already filters by path class. That asymmetry is the defect.
Closing it narrows one detector, which is permitted; FU-10 forbids widening one,
which this is not.

The narrowing excludes documentation and generated paths rather than admitting
only source paths. Documentation is prose and generated files are output, so
neither is evidence of credential handling; config and infrastructure paths
genuinely can hold it, and an allow-list of source alone would lose them. The
deny-list is the conservative direction for a risk detector.

Path-pattern detection is a separate input and stays as it is: a sensitive path
marks the characteristic present regardless of what its content says.

**Acceptance criteria:**

- [ ] A focused RED test exists before the production change, and it fails for
      the stated reason rather than for a missing symbol.
- [ ] A documentation-class file whose added lines contain `cached_input_tokens`
      no longer marks `security_sensitive` present.
- [ ] A generated-class file whose added lines contain a security key no longer
      marks it present.
- [ ] A source-class file whose added lines contain `accessToken` or
      `authToken` still marks it present.
- [ ] A config-class file whose added lines contain a security key still marks
      it present, proving the narrowing is a deny-list and not source-only.
- [ ] Detection driven by sensitive path patterns is unchanged and proved
      unchanged by test.
- [ ] `sentinel explain` on `a1a5803..780c900` no longer reports
      `high por security_sensitive presente`.
- [ ] `go build ./...`, `go vet ./...`, the focused package tests, and the full
      suite pass, with the exact commands and outcomes recorded.
- [ ] `sentinel review` of the commit completes with no unresolved finding.
