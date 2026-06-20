# FluxDocs

> **View. Annotate. Extract. All in Go, all in MIT.**

SDK xử lý & annotate PDF với **core viết 100% bằng Go**, license MIT — compile được sang **WASM** để chạy hoàn toàn client-side (privacy-first). Lựa chọn còn thiếu giữa SDK enterprise đắt đỏ (Nutrient/PSPDFKit) và các lib OSS rời rạc (pdf.js, pdf-lib).

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Go core](https://img.shields.io/badge/core-100%25%20Go-00ADD8)
![Status](https://img.shields.io/badge/status-pre--build-orange)

> **Trạng thái:** Pre-build / Planning (v0.1.0). Repo đang ở giai đoạn dựng nền móng.

## Vì sao FluxDocs

- **Core Go thật** — parse + render PDF chạy hoàn toàn trong Go, không phải binding mỏng qua CGO.
- **Một core, hai build target** — native binary (server/CLI) và WASM (browser) từ cùng codebase.
- **Privacy-first** — tài liệu nhạy cảm (hợp đồng, hồ sơ y tế, tài chính) xử lý ngay trong browser, không gửi byte nào lên server.
- **MIT** — self-host core miễn phí, không sales-gated như Nutrient ($25k–220k/năm).

## Ba tầng

| Tier | License | Tính năng |
|---|---|---|
| **Core** | MIT, free | render, annotation cơ bản, text extraction, merge/split, WASM |
| **Pro** | One-time | form fill, e-signature, redaction an toàn, OCR, compare, watermark |
| **Cloud** | Subscription | AI Q&A, structured extraction, auto-redact PII, collaboration, hosted API |

## Cấu trúc repo

```
core/        engine Go (parse, render, annotation, docops, form, sign, extract, ocr)
cmd/         CLI tool `fluxdocs`
cloud/       backend Go cho Cloud tier
wasm/        entry point build WASM
packages/    JS/TS wrapper (@fluxdocs/react, /vue, …)
examples/    demo go-server, react, vue, cli-batch-redact, ai-extraction
apps/        docs site + landing page
testing/     corpus PDF, golden files, security tests, benchmark
docs/        spec.md — Technical Specification đầy đủ (nguồn sự thật)
.claude/     rules cho AI (CLAUDE.md), agent guide (AGENTS.md), SECURITY.md
```

## Bắt đầu phát triển

Xem `.claude/AGENTS.md` để biết build/test commands và Definition of Done. Tóm tắt:

```bash
go build ./...                                  # native
GOOS=js GOARCH=wasm go build -o /dev/null ./wasm  # WASM (phải green)
go test -race ./...                             # test
go test ./testing/security/redaction/...        # security gate
```

## Bảo mật

Redaction phải xóa nội dung **vĩnh viễn** (không chỉ vẽ đè), parser nhận input không tin cậy, WASM không gửi dữ liệu ra ngoài. Chi tiết: [`.claude/SECURITY.md`](.claude/SECURITY.md). Báo lỗ hổng: security@fluxdocs.dev.

## License

Core: [MIT](LICENSE). Tính năng tùy chọn sau build tag (`mupdf` AGPL, `ocr` Apache-2.0) chịu license riêng — xem ghi chú trong `LICENSE`.
