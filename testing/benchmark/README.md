# Benchmarks

Throughput of the core operations, so performance claims rest on numbers.

```bash
go test ./testing/benchmark -bench=. -benchmem
```

Fixtures are synthetic and built in-process, so results are reproducible. The
benchmark package has no `Test` functions, so `go test ./...` skips it (it does
not slow CI).

## Representative results

Apple M-series laptop, Go 1.x, synthetic fixtures — indicative only; run it on
your own hardware and inputs.

| Operation | Throughput |
|---|---|
| Parse / open | ~570 MB/s |
| Text extraction | ~22,000 pages/sec (~0.05 ms/page) |
| Render text page @150 DPI (PNG) | ~27 pages/sec (~38 ms/page) |
| Render 16 pages @100 DPI, parallel | ~300 pages/sec (the `NumCPU` worker pool) |
| Redact (mark + permanently remove) | ~0.2 ms/page |
| Merge / save | sub-millisecond per document |

The parallel render throughput (~300 vs ~27 pages/sec single-page) is the
goroutine worker pool at work — pages render independently, a natural advantage
of the Go core over single-threaded JS.
