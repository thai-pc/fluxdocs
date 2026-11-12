# aes256vectors

Maintainer-run tooling for the AES-256 fixtures of `testing/corpus/encrypted`
(docs/design/aes256-decryption.md §11.1). Its output is committed; nothing here
runs in CI.

- `main.go` writes a synthetic, unencrypted one-page source PDF showing a
  marker (`-marker`, optional XMP `-meta`).
- `generate.sh` encrypts those sources with the qpdf command line (documented
  options only), has the FluxDocs test-only encryptor write the cases qpdf's
  command line cannot (`core/parse/crypt_v5_fixtures_test.go`), checks every
  file with Ghostscript and, when installed, qpdf, and writes `MANIFEST.tsv`.
  Without qpdf it still writes and checks the encryptor files.

```
testing/tools/aes256vectors/generate.sh [output-dir]   # default testing/corpus/encrypted
```

Requirements: Go, Ghostscript (`gs`); qpdf for the `qpdf_*` cases. Both
external programs are used as black boxes only: they run as separate
processes, and their source is never read (clean room, SECURITY.md §6).
