# FluxDocs

> **View. Annotate. Extract. All in Go, all in MIT.**

FluxDocs là bộ SDK đọc và chú thích (annotate) PDF với phần lõi viết **hoàn toàn bằng Go**, phát hành theo giấy phép MIT và biên dịch được sang **WASM** để chạy ngay trên trình duyệt — không cần gửi tài liệu lên máy chủ. Đây là mảnh còn thiếu giữa hai thái cực: các SDK thương mại đắt đỏ (Nutrient/PSPDFKit) và những thư viện mã nguồn mở rời rạc, thiếu tính năng (pdf.js, pdf-lib).

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Go core](https://img.shields.io/badge/core-100%25%20Go-00ADD8)
![Status](https://img.shields.io/badge/status-pre--build-orange)

> **Trạng thái:** Pre-build / Planning (v0.1.0) — đang dựng nền móng. Xem mục [Tiến độ](#tiến-độ).

## Vì sao chọn FluxDocs

- **Lõi Go thực thụ** — toàn bộ việc phân tích và kết xuất PDF chạy trong Go, không phải lớp bọc mỏng (binding) gọi xuống thư viện C/C++ qua CGO.
- **Một codebase, hai đích biên dịch** — cùng một mã nguồn cho ra binary native (máy chủ, CLI) lẫn WASM (trình duyệt).
- **Ưu tiên quyền riêng tư** — tài liệu nhạy cảm (hợp đồng, hồ sơ y tế, tài chính) được xử lý ngay trong trình duyệt, không một byte nào rời khỏi máy người dùng.
- **Giấy phép MIT** — tự vận hành phần lõi miễn phí, giá minh bạch, không phải liên hệ sales để báo giá như Nutrient ($25k–220k/năm).

## Ba cấp sản phẩm

| Cấp | Giấy phép | Tính năng |
|---|---|---|
| **Core** | MIT, miễn phí | kết xuất, chú thích cơ bản, trích xuất văn bản, gộp/tách, build WASM |
| **Pro** | Mua một lần | điền biểu mẫu, ký số, redaction an toàn, OCR, so sánh tài liệu, watermark |
| **Cloud** | Thuê bao | hỏi đáp tài liệu bằng AI, trích xuất có cấu trúc, tự động che PII, cộng tác, API lưu trữ |

## Cấu trúc kho mã

```
core/        engine Go (parse, render, annotation, docops, form, sign, extract, ocr)
cmd/         công cụ dòng lệnh `fluxdocs`
cloud/       backend Go cho cấp Cloud
wasm/        điểm vào để build WASM
packages/    wrapper JS/TS (@fluxdocs/react, /vue, …)
examples/    demo: go-server, react, vue, cli-batch-redact, ai-extraction
apps/        trang tài liệu + landing page
testing/     corpus PDF, golden file, test bảo mật, benchmark
docs/        spec.md — bản đặc tả kỹ thuật đầy đủ (tài liệu tham chiếu chuẩn)
.claude/     hướng dẫn cho AI: CLAUDE.md, AGENTS.md, SECURITY.md
```

## Tiến độ

Đã có:
- Bộ kiểu dữ liệu lõi và toàn bộ bề mặt API của `Document` (theo §6–§7 đặc tả).
- Lớp phân tích PDF: object model, lexer, indirect object, stream, bảng xref (cổ điển) và resolver giải tham chiếu.
- CI tự động (GitHub Actions): build native + WASM, `go test -race`, cổng kiểm tra redaction.

Đang làm tiếp: trang resolver thành page tree, kết xuất trang đầu tiên ra ảnh raster.

## Bắt đầu phát triển

Chi tiết lệnh build/test và tiêu chí hoàn thành nằm trong [`.claude/AGENTS.md`](.claude/AGENTS.md). Tóm tắt:

```bash
go build ./...                                    # build native
GOOS=js GOARCH=wasm go build -o /dev/null ./wasm  # build WASM (phải xanh)
go test -race ./...                               # chạy test kèm race detector
```

> Cổng bảo mật redaction (`go test ./testing/security/redaction/...`) sẽ chạy trong CI khi đã có test thực sự; xem [`.claude/SECURITY.md`](.claude/SECURITY.md).

## Bảo mật

Redaction phải xóa nội dung **vĩnh viễn** khỏi tài liệu (không chỉ vẽ đè hình chữ nhật đen), trình phân tích luôn coi tệp đầu vào là không đáng tin, và đường WASM không gửi bất kỳ dữ liệu nào ra ngoài. Chi tiết: [`.claude/SECURITY.md`](.claude/SECURITY.md). Báo lỗ hổng: security@fluxdocs.dev.

## Giấy phép

Phần lõi: [MIT](LICENSE). Các tính năng tùy chọn bật bằng build tag (`mupdf` — AGPL, `ocr` — Apache-2.0) tuân theo giấy phép riêng — xem ghi chú trong [`LICENSE`](LICENSE).
