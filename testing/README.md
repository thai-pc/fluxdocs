# Testing — FluxDocs

Test strategy for the Go core + WASM. See security constraints in
`.claude/SECURITY.md`.

## Layout

```
testing/
├── corpus/              # real PDFs for parser/render tests (NO real PII)
│   ├── clean/           # spec-compliant PDFs (Word, LaTeX, Chrome print, Adobe)
│   ├── malformed/       # spec-violating / corrupt PDFs — outcome per file pinned (TestCorpusMalformed)
│   ├── encrypted/       # encrypted PDFs — test ErrEncryptedDocument
│   ├── forms/           # AcroForm/XFA — test parse + fill (Pro)
│   └── scanned/         # scanned images — test OCR fallback (Pro)
├── golden/              # shared render references (PNG)
├── security/
│   ├── redaction/       # GATE: extract-after-redact must be empty. 100% to merge.
│   ├── fuzz/            # seed corpus for go test -fuzz (parser/tokenizer)
│   ├── dos/             # GATE: hostile-document cost gates, relative timing (see below)
│   └── encryption/      # crypto / signature / share-link tests
├── e2e/                 # end-to-end via client wrappers (React/Vue + WASM)
├── benchmark/           # benchmark suite (ms/page, MB/s, bundle size)
└── fixtures/            # sample annotation layers, processing jobs (JSON)
```

Each Go package also has its own `testdata/` (e.g.
`core/render/testdata/golden/`).

## Test kinds

| Kind | Scope | How |
|---|---|---|
| **Table-driven** | parser, tokenizer, types | input/expected as a table, `go test` |
| **Golden-file** | raster/svg render | compare PNG output against a reference; regenerate deliberately |
| **Fuzz** | parse/xref/content stream | `go test -fuzz`, seeds in `security/fuzz/` |
| **Security gate** | redaction, isolation | extract-after-redact, cross-org access |
| **Race** | render goroutine pool | `go test -race` |
| **Benchmark** | performance | `go test -bench`, publish vs pdf.js/MuPDF |
| **E2E** | wrapper + WASM | run the real viewer in a headless browser |

## Pre-merge gate (CI GitHub Actions)

1. `go build ./...` + `GOOS=js GOARCH=wasm go build ./wasm` — both green.
2. `go test -race ./...` green.
3. `gofmt -l .` empty, `go vet ./...` clean.
4. `go test ./testing/security/redaction/...` **passes 100%** (hard block). The
   `redaction-oracle` CI job runs it with Ghostscript installed and
   `FLUXDOCS_REQUIRE_GS=1`, so the oracle cannot be skipped there (the job is
   non-blocking until it has run green and stable on CI).
5. Golden diffs (if any) reviewed by hand.

## Timing gates: relative, never absolute

A test that bounds how long something takes never compares against a fixed
time: CI runs on 2-core runners, often loaded, and an absolute threshold
either flakes there or is too loose to catch anything on a fast machine.
Every timing gate (in `core/...` and `testing/security/dos`) measures the work
it bounds and a reference in the same run, with `internal/reltime`:

- **Reference.** The same operation on a quarter (or half) of the input for a
  linearity gate (`reltime.Linear`: linear ≈ 4×, quadratic ≈ 16×, bounded at
  `reltime.LinearBound` = 8×); a benign input of the same size; or the work a
  budget allows (the font gates' default-budget flood, an inflate of the bytes
  a cap permits).
- **Measurement.** Process CPU time (wall time for deadlines and parallel
  batches), each side repeated to about 0.25 s of CPU time (`reltime.CPUTarget`;
  0.5 s of wall time, `reltime.Target`), in runs alternated with each other;
  the bound is on the median ratio. Five runs (`reltime.Runs`) where the
  measured ratio is within 2× of its bound, three (`reltime.FewRuns`) where it
  is under half of it. A reference several gates of one test share (the font
  gates' default-budget flood, an inflate of the bytes a cap permits, a
  normal page) is measured once per test (`reltime.PerCall`, the dos
  package's `perTestRef`) and each gate bounded against it
  (`reltime.AgainstFixed`).
- **Deadlines.** `reltime.AfterDeadline` sets the deadline at a quarter of a
  reference render stopped by a scaled paint budget, and bounds the time to
  return at 0.75× the reference.
- They are skipped under `-short` and `-race` (`reltime.Skip`); the functional
  checks around them still run. CI's native job tests with `-race`, so the
  gates in `core/...` run only in the `nostd14` job (`go test -tags nostd14
  ./core/...`, no `-race`); the `testing/security/dos` gates run in the
  native job's "Render budget DoS gate" step, without `-race`.

## Cross-reference repair: the byte-identity gate

Repair of damaged cross-reference data (docs/design/xref-reconstruction.md)
runs only when the strict open fails, so a file that opens today must open
exactly as before. `TestOpenManifest` (`corpus/manifest_test.go`) checks it:
`corpus/testdata/open_manifest.tsv`, recorded at the commit before repair,
holds per file the open outcome, page count, `ExtractText` hash, 72-DPI page-0
pixel hash and a canonical `SaveTo` hash (object numbering is not stable from
run to run). A row that opened there must match exactly; only `open_failed`
rows may change. With `FLUXDOCS_EXTRA_CORPUS`, the extra corpus is compared
with the manifest named by `FLUXDOCS_EXTRA_MANIFEST`
(`FLUXDOCS_EXTRA_MANIFEST_WRITE` records one). `TestRepairCensus` measures the
scan's object counts over the extra corpus.

## Redaction oracle (Ghostscript)

The leak corpus (`security/redaction/`) checks every redacted output with a
differential oracle: Ghostscript's `txtwrite` text of the output must not hold
the secret (docs/design/redaction-hardening.md §12.1–12.3). Without `gs` on
`PATH` those subtests skip, unless `FLUXDOCS_REQUIRE_GS=1`, which makes its
absence a failure (set in CI). The oracle is native-only (`//go:build !js`).

Licensing: Ghostscript is AGPL. The tests run it as a separate process; nothing
links it and nothing ships it, so the MIT build is unaffected.

The corpus no-regression check (`corpus/redaction_regress_test.go`) reads
extra PDFs from `FLUXDOCS_EXTRA_CORPUS` (a file of paths) and compares refusal
outcomes with `FLUXDOCS_EXTRA_BASELINE`; `FLUXDOCS_REGRESS_EXT` writes what
automatic extension added per operation (no document text). Verification
failures must be 0.

## Corpus principles

- Coverage grows incrementally: Wave 1 prioritizes the common ~90% of real PDFs
  (Word/LaTeX/Chrome/Adobe).
- The `malformed/` files matter — most real-world PDFs violate the spec; the
  parser must be lenient + fall back to a brute-force scan.
- Do NOT commit documents containing real PII. Use synthetic / anonymized data.
