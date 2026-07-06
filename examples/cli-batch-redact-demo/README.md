# cli-batch-redact-demo

Batch-redact a regex pattern from every PDF in a directory, entirely locally.

```bash
go run ./examples/cli-batch-redact-demo \
  -in ./pdfs -out ./redacted \
  -pattern '\d{3}-\d{2}-\d{4}'   # e.g. US SSNs
```

Each output PDF has the matched text **removed from the content stream** (not
painted over) — verify with `fluxdocs extract redacted/file.pdf`.
