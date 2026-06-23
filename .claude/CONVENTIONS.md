# FluxDocs — Code Conventions for AI & Humans

> The single source of truth for **how code is written** in this repo: comments,
> inputs, outputs, return values, errors. Read this before writing any Go.
> It complements (does not replace) `CLAUDE.md` (project rules), `AGENTS.md`
> (agent workflow), and `SECURITY.md` (security constraints).
>
> **Language rule:** All code, comments, identifiers, commit messages, and docs
> are written in **English**. Vietnamese is for chat with the maintainer only —
> never in committed files.

---

## 0. Golden rule

New code must read like the code already around it. Match the existing file's
comment density, naming, error style, and idioms. If this document and a
well-established local pattern disagree, follow the local pattern and flag it.

---

## 1. Doc comments (godoc)

Every exported identifier (type, func, method, const, var) has a doc comment.
It starts with the identifier's name and is a full sentence.

```go
// RenderPage renders one page to a buffer in opts.Format ("png"|"jpeg"|"svg").
//
// Returns the encoded image bytes, or ErrPageNotFound if index is out of range.
func (d *Document) RenderPage(index int, opts RenderOptions) ([]byte, error) {
```

Rules:
- **Start with the name.** `// RenderPage renders…`, not `// This renders…`.
- **Describe behavior, not implementation.** The body shows *how*; the comment
  says *what* and *why*.
- **State the contract** for anything non-obvious: units, coordinate space,
  ownership, mutation, ordering, concurrency safety. PDF is full of traps
  (points vs pixels, origin at bottom-left) — spell them out.
- **Document the return**, see §3.
- Keep lines ≤ ~90 cols to match the codebase.

Package comment: exactly one per package (on any file, conventionally the
central one), describing scope and the spec section it implements:

```go
// Package parse implements the PDF parsing layer per ISO 32000-1:2008: object
// model, lexer, xref, page tree, and content streams. Clean-room — based only
// on the public specification (see .claude/SECURITY.md §6).
package parse
```

Inline comments explain *why*, never restate *what* the code already says:

```go
start := skipStreamEOL(p.data, ni)   // `stream` must be followed by EOL (§7.3.8.1)
```

Cite the spec when behavior is spec-driven: `(§7.3.4.3)`, `(Appendix B)`,
`(SECURITY.md §2)`. This is how a future reader verifies correctness.

---

## 2. Inputs (parameters)

- **Order:** receiver, then the primary subject, then options/config last.
  `RenderPage(index int, opts RenderOptions)` — subject `index`, then `opts`.
- **Group options in a struct** once a function needs more than ~3 knobs
  (`RenderOptions`, `ExtractOptions`, `SignatureOptions`). Add fields to the
  struct instead of growing the signature — it keeps call sites stable.
- **IDs are typed, never bare strings.** Use `DocumentID`, `PageID`,
  `AnnotationID`, `LayerID`. A function taking a page identity takes `PageID`.
- **Validate at the boundary, fail fast.** Bounds-check and argument-check
  *before* doing work, and before reaching any unimplemented path:

  ```go
  func (d *Document) RenderPage(index int, opts RenderOptions) ([]byte, error) {
      if index < 0 || index >= len(d.Pages) {
          return nil, ErrPageNotFound          // checked first, always
      }
      // ... real work
  }
  ```
- **Untrusted input is hostile.** Parser/loader inputs are attacker-controlled:
  enforce size/recursion/iteration bounds (see SECURITY.md §2). Never assume a
  PDF obeys the spec.
- **Don't mutate input the caller still owns.** If you keep bytes from an input
  slice beyond the call, copy them (as `maybeStream` copies stream `Raw`).

---

## 3. Outputs & return values

This is the part most worth getting consistent. Patterns, in order of
preference:

### 3.1 `(T, error)` — the default

Return a value and an error. On error, the value is the zero value and callers
must not use it.

```go
func OpenDocument(path string) (*Document, error)
func (d *Document) ExtractText(opts ExtractOptions) (string, error)
```

- **Error is always last.**
- **On error, return the zero value** for the other results (`nil`, `""`, `0`),
  never a half-built value. Tests assert this (e.g. non-nil doc with an error is
  a bug).
- **No naked error sentinels as success.** A `nil` error means success, period.

### 3.2 `(T, bool)` — lookups that can legitimately miss

When "not found" is normal control flow, not an error, return a comma-ok pair.
Used by all `Dict` accessors:

```go
// GetInt returns the Integer value at key.
func (d Dict) GetInt(key Name) (Integer, bool)
```

`ok == false` means absent or wrong type; the value is the zero value. Use this
instead of an error when the caller routinely branches on presence.

### 3.3 `(T, bool, error)` — lookup that can miss *or* fail

When resolution can both legitimately miss *and* hit a hard error, separate the
two so callers can tell them apart:

```go
// ResolveDict resolves obj to a Dict, following a reference if needed.
// Returns (dict, true, nil) on success, (nil, false, nil) if obj is not a
// dictionary, or (nil, false, err) if resolution fails.
func (r *Resolver) ResolveDict(obj Object) (Dict, bool, error)
```

### 3.4 Incremental parser return shape

Functions that consume bytes return the next offset so the caller can continue:

```go
// Returns the token and the offset just past it; EOF token at end of input.
func nextToken(data []byte, i int) (token, int, error)

// Returns num, gen, the object, and the offset just past 'endobj' (next == off
// on failure).
func ParseIndirectObjectAt(data []byte, off int) (num, gen int, obj Object, next int, err error)
```

Rule: **on failure, return the *input* offset (no progress)**, so a caller that
retries or scans doesn't skip bytes.

### 3.5 Slices and maps

- Return `nil` (not an empty non-nil slice) for "no results" unless a specific
  caller needs to distinguish — `nil` ranges fine and reads cleanly.
- Returned collections are owned by the caller; if they alias internal state,
  say so in the doc comment.

---

## 4. Errors

- **Return errors, don't panic.** `panic` is only for unrecoverable internal
  logic bugs, never for malformed input or runtime conditions.
- **Public sentinels** live in `core/errors.go`, are prefixed `fluxdocs:`, and
  are matched with `errors.Is`. Add one when callers need to *distinguish* a
  case; don't multiply sentinels for cases nobody branches on.
- **Wrap with context using `%w`**, preserving the chain:

  ```go
  return nil, fmt.Errorf("loading object %d: %w", num, err)
  ```
- **Package-internal errors** (lowercase, e.g. `errLex`, `errNotImplemented`)
  are not part of the public API. The core layer wraps them into public
  sentinels (`errLex` → `ErrInvalidPDF`) at the boundary.
- **Lenient parsing:** for malformed-but-recoverable PDF input, recover and
  continue (most real PDFs violate the spec) — but never let leniency mask a
  security boundary (decompression bombs, xref loops). Recovery has limits;
  those limits are hard.
- Error message style: lowercase, no trailing punctuation, no "failed to" noise.
  `"missing 'obj' keyword at offset 42"`, not `"Failed to find OBJ keyword."`.

---

## 5. Naming

- Exported: `PascalCase`, verb-first for actions (`RenderPage`, `AddAnnotation`).
  No redundant `Get` prefix on simple accessors per Effective Go — but the
  spec's public API (§7) does use `GetPage`/`GetFormFields`, so **follow the
  spec for the documented surface** and drop `Get` for new internal helpers.
- Unexported: `camelCase`. Short, local, unsurprising.
- Packages: short, lowercase, no underscores, no stutter (`parse`, not
  `pdfparse`; `parse.Object`, not `parse.ParseObject` when avoidable).
- Acronyms keep case: `PDF`, `ID`, `DPI`, `OCR`, `URL` (`DocumentID`, `parseXref`).
- Test names: `TestThing`, subtests describe the case in plain English.

---

## 6. Concurrency

- Bound parallelism by `runtime.NumCPU()` via a semaphore channel
  (`make(chan struct{}, runtime.NumCPU())`) — see `RenderAllPages`.
- Pages render in parallel; **within** a page, content-stream operators are
  sequential (graphics state is stateful — §13.2).
- Guard shared mutable state with a mutex; document what a lock protects.
- All tests must pass under `go test -race`.

---

## 7. Tiers & build tags (license boundary — non-negotiable)

- Default build is **pure Go, CGO-disabled, MIT-clean**. `core/` (except `ocr/`,
  `sign/`) must not import CGO or Pro/Cloud packages.
- CGO/AGPL/optional deps go **behind build tags** (`//go:build ocr`,
  `//go:build mupdf`) with the license consequence documented.
- Core code must not import Pro or Cloud code. Check direction of imports.

---

## 8. Tests

- **Table-driven** for parsers and pure functions (see `parser_test.go`).
- **Golden-file** for rendering; regenerate goldens deliberately and review the
  diff.
- Build fixtures with **computed offsets**, never hardcoded byte positions
  (`buildMinimalPDF` computes every offset) — hardcoding rots instantly.
- Test the **contract**: zero values on error, bounds errors before
  unimplemented paths, idempotency where promised.
- **Redaction has a mandatory security gate**: after redaction, `ExtractText`
  over the redacted region must return empty. A redaction PR does not merge
  unless `testing/security/redaction/` passes 100% (SECURITY.md §1).

---

## 9. Pre-commit checklist (mirrors CI)

```bash
gofmt -l .                                        # must print nothing
go vet ./...
CGO_ENABLED=0 go build ./...                      # pure-Go build stays green
go test -race ./...
GOOS=js GOARCH=wasm go build ./core/... ./wasm/... # WASM stays green
```

Commit messages: `package: imperative summary` (e.g.
`core/parse: handle compressed xref streams`), body explains *why*, and ends
with the `Co-Authored-By` trailer.
