# FluxDocs — Project Context & Rules for AI

> Bản tóm tắt định hướng để bất kỳ AI assistant nào (Claude Code, Copilot, v.v.)
> làm việc trong repo này hiểu **dự án là gì, viết code thế nào, và ranh giới nào không được vượt**.
> Nguồn sự thật đầy đủ: `/Users/thai-pc/Downloads/fluxdocs-spec.md` (Technical Specification v0.1.0).

---

## 1. Dự án là gì

**FluxDocs** = SDK xử lý & annotate PDF, **core viết 100% bằng Go**, license MIT, thuộc họ Flux (FluxFiles, FluxGantt).

Mục tiêu: lấp khoảng trống giữa SDK enterprise đắt đỏ (Nutrient/PSPDFKit $25k–220k/năm) và các lib OSS rời rạc (pdf.js chỉ render, pdf-lib chỉ edit cơ bản).

Điểm khác biệt kỹ thuật cốt lõi — **luôn giữ đúng khi code**:
1. **Core Go thật**, không phải binding mỏng qua CGO. Phần nặng (parse + render PDF) chạy hoàn toàn trong Go.
2. **Một core, hai build target**: native binary (server/CLI) + WASM (`GOOS=js GOARCH=wasm`) chạy client-side.
3. **Privacy-first qua WASM**: tài liệu nhạy cảm xử lý hoàn toàn trong browser, không gửi byte nào lên server.
4. **Annotation tách biệt PDF gốc**: lưu dưới dạng JSON layer riêng, áp lên khi render hoặc khi flatten.

## 2. Ba tầng sản phẩm (ảnh hưởng tới việc đặt code ở đâu)

| Tier | License | Tính năng | Vị trí code |
|---|---|---|---|
| **Core** | MIT, free | render, annotation cơ bản, text extraction, merge/split, WASM | `core/` (trừ `ocr/`, `sign/`) |
| **Pro** | One-time | form fill, e-signature, redaction, OCR, compare, watermark, PDF/A | `core/form`, `core/sign`, `core/docops/redact`, `core/ocr` |
| **Cloud** | Subscription | AI Q&A, structured extraction, auto-redact PII, collaboration, hosted API | `cloud/` |

**Quy tắc license-tách-tầng:** Code MIT thuần không được `import` package phụ thuộc CGO (OCR Tesseract, MuPDF). Phần CGO PHẢI nằm sau **build tag** (`//go:build ocr` / `//go:build mupdf`) để build mặc định giữ MIT 100%. Đây là ràng buộc pháp lý, không phải tùy chọn.

## 3. Tech stack

- **Ngôn ngữ core:** Go 1.23+. Build target thêm WASM.
- **PDF parsing:** tự viết parser theo **PDF 32000-1:2008 (ISO)**. Clean-room — KHÔNG tham khảo source Nutrient/Apryse (xem `SECURITY.md` mục Legal).
- **Render:** raster PNG/JPEG (server thumbnail) + SVG (client vector-accurate) + Canvas API (WASM).
- **Concurrency:** goroutine worker pool, giới hạn bằng `runtime.NumCPU()`.
- **OCR:** Tesseract qua CGO, build tag `ocr` (Pro).
- **AI:** gọi LLM API (Claude) qua HTTP client Go chuẩn — không thêm SDK ngôn ngữ khác.
- **Cloud backend:** Go + Chi/Echo + PostgreSQL 16 + sqlc + Cloudflare R2 + River queue + Stripe + Fly.io.
- **Client wrappers:** `@fluxdocs/react`, `@fluxdocs/vue` (Wave 1); web-components, cloud-sdk sau.
- **Monorepo:** `go.work` (Go multi-module) + `pnpm-workspace.yaml` (phần JS).

## 4. Cấu trúc thư mục (xem cây thật trong repo)

```
core/        # engine Go chính: parse, render, annotation, docops, form, sign, extract, ocr
cmd/         # CLI tool `fluxdocs`
cloud/       # backend Go (Cloud tier): api, queue, db
wasm/        # entry point build WASM, export func qua syscall/js
packages/    # JS/TS wrapper (@fluxdocs/*)
examples/    # demo go-server, react, vue, cli-batch-redact, ai-extraction
apps/        # docs site + landing page
testing/     # corpus PDF, golden files, security tests, e2e, benchmark, fixtures
.claude/     # tài liệu này + AGENTS.md + SECURITY.md
```

Đặt code đúng package theo §10.4 spec: `core`, `render`, `annotation`, `extract`, `ocr`, `sign`, `cloud`.

## 5. Coding conventions (BẮT BUỘC tuân theo)

### Go
- **Naming:** PascalCase exported, verb đứng trước. KHÔNG dùng prefix `Get` cho field đơn giản (theo Effective Go). Tên package: ngắn, lowercase, không underscore.
  - Đúng: `doc.RenderPage(0, opts)`, `doc.AddAnnotation(a)`, `doc.ExtractText(opts)`
  - Sai: `doc.GetRenderedPageAsImage(0)`, `doc.render_page(...)`, `doc.DoOperation("render", 0)`
- **Error handling:** trả `error` là giá trị thứ hai. KHÔNG `panic` cho lỗi runtime thường (chỉ panic cho bug logic không phục hồi được). Dùng sentinel error + `errors.Is`:
  - `ErrPageNotFound`, `ErrEncryptedDocument`, `ErrInvalidPDF` (định nghĩa trong `core/errors.go`).
- **ID types:** dùng type alias có kiểm soát (`DocumentID`, `PageID`, `AnnotationID`, `LayerID`) — tránh nhầm lẫn ID, không truyền `string` trần.
- **Concurrency:** worker pool giới hạn bằng semaphore `make(chan struct{}, runtime.NumCPU())`. Mỗi trang render độc lập (giữa các trang parallel, trong 1 trang tuần tự vì content stream stateful).
- **Build tags:** mọi thứ CGO/license-phụ-thuộc nằm sau build tag riêng.

### CSS / Client UI (BEM, prefix `fd-`)
- Class: `.fd-viewer`, `.fd-viewer__canvas`, `.fd-annotation--highlight`, `.fd-annotation--selected`.
- CSS custom property prefix: `--fd-*`.
- Tránh xung đột với host application — luôn prefix `fd-`.

### Design tokens (xem §8.2 spec)
- Primary `#6366f1` (indigo), annotation default amber `#f59e0b`, redact fill `#18181b` (đen đặc, KHÔNG trong suốt).
- Font: Inter (UI), JetBrains Mono (code). Base size 13px (density cao).
- Tôn trọng `prefers-reduced-motion`. WCAG 2.1 AA cho toolbar/sidebar.

## 6. Testing rules (xem `testing/README.md` chi tiết)

- **Parser:** table-driven test. Corpus PDF thật trong `testing/corpus/` — bao gồm cả file **malformed** (đa số PDF thực tế vi phạm spec). Lenient parsing + fallback brute-force scan.
- **Render:** golden-file test — so output PNG với reference trong `*/testdata/golden/`. Khi đổi render, regenerate golden có chủ đích, review kỹ diff.
- **CI (GitHub Actions):** phải green cả **native** và **WASM** target trước khi merge.
- **Redaction:** xem mục 7 — test bắt buộc, không thương lượng.
- **Coverage tăng dần:** Wave 1 chỉ cần hỗ trợ tốt subset 90% PDF thực tế (sinh từ Word, LaTeX, Chrome print, Adobe). Không ép 100% spec ngày đầu.

## 7. SECURITY — đọc kỹ, đây là rủi ro reputation #1

Chi tiết đầy đủ trong `.claude/SECURITY.md`. Tóm tắt ràng buộc cứng:

1. **Redaction phải XÓA VĨNH VIỄN nội dung trong content stream**, không chỉ vẽ hình chữ nhật đen đè lên. Lỗi "vẽ đè" là lỗ hổng phổ biến đã phá hủy uy tín nhiều tổ chức.
   - Sau mọi thao tác redact, test BẮT BUỘC chạy lại `ExtractText()` trên vùng đã redact → phải trả về rỗng / không khớp nội dung gốc.
   - PR đụng vào redaction KHÔNG được merge nếu `testing/security/redaction/` không pass 100%.
   - Phải xóa cả metadata/annotation ẩn có thể chứa nội dung tương tự.
2. **Privacy WASM:** ở chế độ client-side, KHÔNG được thêm bất kỳ network call nào gửi nội dung PDF ra ngoài. Đây là lời hứa lõi của sản phẩm.
3. **Parser an toàn:** input là file không tin cậy. Chống decompression bomb, vòng lặp xref vô hạn (incremental update `/Prev`), object stream lồng nhau, integer overflow khi tính kích thước canvas. Fuzz seeds trong `testing/security/fuzz/`.
4. **Cloud:** secret/PII không log. `api_keys` lưu **hash** (không plaintext), share link có `password_hash` + `expires_at`. Mọi query scoped theo `org_id` (multi-tenant isolation). Xác thực JWT/OAuth.
5. **License/legal:** clean-room implementation từ ISO 32000 công khai. MuPDF (AGPL) chỉ qua build tag `mupdf`, document rõ binary build tag đó chịu AGPL.

## 8. Roadmap (biết đang ở đâu)

- **Wave 1 (Tuần 1–8, Core MIT):** parser, render raster+WASM, annotation 4 loại, React/Vue wrapper, merge/split, text extraction, docs+launch.
- **Wave 2 (Tuần 11–18, Pro):** forms, e-signature PKCS#7, **redaction an toàn**, OCR, compare, watermark, PDF/A.
- **Wave 3 (Tháng 6+, Cloud):** backend API, AI extraction/Q&A, auto-redact PII, collaboration CRDT-lite, webhook/Zapier.

## 9. Khi AI làm việc trong repo này

- Mặc định **pure-Go, MIT-safe**. Nếu một thay đổi cần CGO → đặt sau build tag và nói rõ.
- Giữ annotation **tách biệt** PDF gốc (JSON layer), đừng bake trừ khi gọi `Flatten*`.
- Trước khi viết code redaction/parser/crypto → đọc `.claude/SECURITY.md`.
- Tôn trọng ranh giới tier: đừng để code Core import code Pro/Cloud.
- Khi không chắc về hành vi đúng của PDF → tra ISO 32000-1:2008, không đoán.
- Brand voice trong docs/comment: trực tiếp, kỹ thuật, benchmark cụ thể. Tránh "revolutionary", "enterprise-grade" sáo rỗng.
