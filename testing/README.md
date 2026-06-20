# Testing — FluxDocs

Chiến lược test cho core Go + WASM. Xem ràng buộc bảo mật ở `.claude/SECURITY.md`.

## Cấu trúc

```
testing/
├── corpus/              # PDF thật để test parser/render (KHÔNG chứa PII thật)
│   ├── clean/           # PDF đúng spec (Word, LaTeX, Chrome print, Adobe)
│   ├── malformed/       # PDF vi phạm spec / corrupt — phải lenient-parse được
│   ├── encrypted/       # PDF mã hóa — test ErrEncryptedDocument
│   ├── forms/           # AcroForm/XFA — test parse + fill (Pro)
│   └── scanned/         # ảnh scan — test OCR fallback (Pro)
├── golden/              # render reference dùng chung (PNG)
├── security/
│   ├── redaction/       # GATE: extract-after-redact phải rỗng. Pass 100% mới merge.
│   ├── fuzz/            # seed corpus cho go test -fuzz (parser/tokenizer)
│   └── encryption/      # test crypto / signature / share-link
├── e2e/                 # end-to-end qua client wrapper (React/Vue + WASM)
├── benchmark/           # benchmark suite (ms/page, MB/s, bundle size)
└── fixtures/            # JSON annotation layer, processing job mẫu
```

Mỗi package Go cũng có `testdata/` riêng (vd `core/render/testdata/golden/`).

## Loại test

| Loại | Phạm vi | Cách làm |
|---|---|---|
| **Table-driven** | parser, tokenizer, type | input/expected dạng bảng, `go test` |
| **Golden-file** | render raster/svg | so output PNG với reference; regenerate có chủ đích |
| **Fuzz** | parse/xref/content stream | `go test -fuzz`, seed trong `security/fuzz/` |
| **Security gate** | redaction, isolation | extract-after-redact, cross-org access |
| **Race** | goroutine pool render | `go test -race` |
| **Benchmark** | hiệu năng | `go test -bench`, publish so với pdf.js/MuPDF |
| **E2E** | wrapper + WASM | chạy viewer thật trong browser headless |

## Gate trước khi merge (CI GitHub Actions)

1. `go build ./...` + `GOOS=js GOARCH=wasm go build ./wasm` — cả hai xanh.
2. `go test -race ./...` xanh.
3. `gofmt -l .` rỗng, `go vet ./...` sạch.
4. `go test ./testing/security/redaction/...` **pass 100%** (chặn cứng).
5. Golden diff (nếu có) được review thủ công.

## Nguyên tắc corpus

- Coverage tăng dần: Wave 1 ưu tiên 90% PDF thực tế (Word/LaTeX/Chrome/Adobe).
- File `malformed/` quan trọng — đa số PDF ngoài đời vi phạm spec; parser phải lenient + fallback brute-force.
- KHÔNG commit tài liệu chứa PII thật. Dùng synthetic / đã ẩn danh.
