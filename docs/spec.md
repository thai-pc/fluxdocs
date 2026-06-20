# FluxDocs — Technical Specification

> **The Modern MIT-Licensed PDF Viewer & Annotation SDK — Go Core, AI-Native**

| | |
|---|---|
| **Version** | 0.1.0 (Pre-launch Draft) |
| **Status** | Planning / Pre-build |
| **Author** | Flux Toolkit Team |
| **License** | Core MIT · Pro Commercial · Cloud SaaS |
| **Date** | 2026 |

---

## Overview

FluxDocs là SDK xử lý và annotate PDF, core engine viết bằng **Go**, license MIT, nhắm vào khoảng trống giữa hai cực: SDK enterprise siêu đắt (Nutrient/PSPDFKit — license self-hosted entry-level $25,000–40,000/năm, trung bình $76,000/năm, lên tới $220,000+ cho deployment lớn) và các lựa chọn open-source rời rạc, thiếu tính năng (pdf.js chỉ render, pdf-lib chỉ chỉnh sửa cơ bản, không cái nào có annotation engine + server processing + AI extraction tích hợp).

Sản phẩm thuộc họ Flux (cùng FluxFiles, FluxGantt), kế thừa toàn bộ design system, brand voice, và mô hình monetization 3 tầng đã được kiểm chứng.

**Vì sao Go, và Go đóng vai trò gì:**

PDF processing là bài toán byte-level, CPU-bound, không cần I/O bất đồng bộ phức tạp — đúng sở trường của Go. FluxDocs dùng kiến trúc **"core Go thật"**: toàn bộ phần nặng (parse PDF structure, render trang ra raster/SVG, ghép annotation layer, merge/split, redact, OCR trigger, digital signature) chạy trong Go server hoặc Go binary CLI. Phần UI viewer ở client chỉ là lớp mỏng (Canvas/WASM) hiển thị kết quả Go trả về — không phải Go "đóng vai phụ" như nhiều dự án gắn Go gượng ép vào use case UI-heavy.

Compile sang WASM cho phép cùng một core Go chạy được cả server (Docker self-hosted) và browser (client-side rendering, không cần gửi PDF nhạy cảm lên server) — đây là đòn bẩy kỹ thuật chính để cạnh tranh với Nutrient (vốn dùng C++ core, không có lựa chọn WASM nhẹ).

**Ba tầng monetization:**

- **Core (MIT, free):** Render PDF, annotation cơ bản (highlight, note, draw), text extraction, merge/split
- **Pro (one-time):** Form fill, digital signature, redaction, OCR, compare documents, watermark removal
- **Cloud (subscription):** AI document Q&A, auto-extraction structured data, hosted processing API, collaboration

Đối tượng chính là **developer** nhúng PDF viewing/annotation vào sản phẩm của họ — SaaS quản lý hợp đồng, công cụ pháp lý, hệ thống quản lý tài liệu nội bộ, nền tảng e-signature tự dựng.

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Market Analysis](#2-market-analysis)
3. [Product Positioning & Branding](#3-product-positioning--branding)
4. [Technology Stack](#4-technology-stack)
5. [System Architecture](#5-system-architecture)
6. [Core Type System](#6-core-type-system)
7. [Public API Specification](#7-public-api-specification)
8. [UI/UX Design System](#8-uiux-design-system)
9. [Feature Roadmap (3 Waves)](#9-feature-roadmap-3-waves)
10. [API Naming Conventions](#10-api-naming-conventions)
11. [Code Organization](#11-code-organization)
12. [Database Schema (Cloud Tier)](#12-database-schema-cloud-tier)
13. [Algorithms Reference](#13-algorithms-reference)
14. [Pricing & Monetization](#14-pricing--monetization)
15. [Distribution & Launch Strategy](#15-distribution--launch-strategy)
16. [18-Week Execution Plan](#16-18-week-execution-plan)
17. [Validation Milestones](#17-validation-milestones)
18. [Risk Assessment & Mitigation](#18-risk-assessment--mitigation)
19. [Appendix A: Sample Document JSON Schema](#19-appendix-a-sample-document-json-schema)
20. [Appendix B: PDF Rendering Pipeline Pseudocode](#20-appendix-b-pdf-rendering-pipeline-pseudocode)
21. [Appendix C: Competitor Comparison Matrix](#21-appendix-c-competitor-comparison-matrix)

---

## 1. Executive Summary

FluxDocs là SDK xử lý và annotate PDF, core viết bằng Go, license MIT, nhắm vào khoảng trống giữa SDK enterprise đắt đỏ (Nutrient/PSPDFKit, ComPDF, Apryse) và thư viện open-source rời rạc thiếu tính năng (pdf.js, pdf-lib, pdfcpu dùng riêng lẻ).

Sản phẩm thuộc họ Flux, cùng FluxFiles và FluxGantt. Brand chung giảm chi phí marketing, kế thừa uy tín developer experience đã xây từ các sản phẩm trước.

Ba tầng monetization:

- **Core (MIT, free):** Render, annotation cơ bản, text extraction, merge/split
- **Pro (one-time):** Form fill, e-signature, redaction, OCR, document compare
- **Cloud (subscription):** AI document Q&A, structured data extraction, hosted API, collaboration

Khác biệt kỹ thuật cốt lõi: core Go thật (không phải binding mỏng qua CGO), compile được sang WASM để chạy client-side hoàn toàn — giải quyết bài toán privacy (PDF nhạy cảm như hợp đồng, hồ sơ y tế, tài chính không cần gửi lên server) mà các SDK cloud-first khó làm được.

---

## 2. Market Analysis

### 2.1 Competitor Landscape

**Commercial / Closed Source:**

| Product | License | Pricing | Stack | Strengths | Weaknesses |
|---|---|---|---|---|---|
| **Nutrient (PSPDFKit)** | Commercial | $25k–40k/năm entry-level self-host; trung bình $76k/năm; lên tới $220k+ enterprise | C++ core, binding nhiều ngôn ngữ | Feature-complete nhất thị trường, hỗ trợ tốt, đủ cả Web/mobile/desktop/server | Rất đắt, sales-gated pricing (phải liên hệ sales để biết giá), overkill cho team nhỏ |
| **Apryse (trước là PDFTron)** | Commercial | Tương đương Nutrient, sales-gated | C++ core | Mature, độ chính xác rendering cao | Cùng vấn đề giá, learning curve dốc |
| **ComPDF** | Commercial (có free tier hạn chế) | Rẻ hơn Nutrient nhưng vẫn theo component | C++ core | Giá cạnh tranh hơn Nutrient | Ecosystem nhỏ hơn, ít case study enterprise |

**Open Source:**

| Product | License | Stack | Status | Weaknesses |
|---|---|---|---|---|
| **pdf.js** | Apache 2.0 | JavaScript | Maintained tốt (Mozilla) | Chỉ render/view, không có annotation engine đầy đủ, không xử lý server-side |
| **pdf-lib** | MIT | JavaScript | Maintained | Tạo/chỉnh sửa PDF cơ bản, không render UI, không OCR, không AI |
| **pdfcpu** | Apache 2.0 | Go | Maintained, nhưng CLI-focused | Mạnh về merge/split/watermark/encrypt nhưng không có rendering engine hay annotation UI, không hướng tới embed vào product |
| **MuPDF** | AGPL / Commercial dual | C | Rất mature, performance cao | License AGPL ràng buộc nặng cho closed-source; phải mua commercial license để dùng thương mại — đây chính là khoảng trống FluxDocs lấp vào |

### 2.2 Market Gap

Khoảng trống rõ ràng cho một SDK MIT-licensed mang lại:

- Core Go thật — không phải JS-only (thiếu server processing) hoặc C++ đóng kín (thiếu khả năng tùy biến, audit code)
- Annotation engine đầy đủ: highlight, note, draw, shape, stamp, measurement — không chỉ render
- Server-side processing: merge, split, redact, watermark, OCR, form-fill, e-signature trong cùng một SDK
- WASM build chính thức — chạy 100% client-side khi cần privacy (không gửi file lên server)
- AI-powered từ đầu: document Q&A, structured extraction, auto-redaction theo pattern (PII, số thẻ, v.v.) — tính năng mà Nutrient phải bán riêng qua add-on XtractFlow
- Pricing minh bạch, không sales-gated — self-serve license key giống mô hình Stripe Checkout

### 2.3 Customer Profile

**Primary Customer:**
- Developer solo/small-team xây SaaS xử lý tài liệu: contract management, e-signature tự dựng, hệ thống hồ sơ pháp lý/y tế/tài chính
- Pain: Nutrient quá đắt cho giai đoạn early-stage ($25k+/năm chỉ để bắt đầu), pdf.js/pdf-lib không đủ tính năng phải ghép 3-4 lib khác nhau
- Mức chi: $299–799 one-time/developer cho Pro license

**Secondary Customer:**
- Agency dựng tool quản lý tài liệu cho khách hàng (luật, bất động sản, bảo hiểm)
- Pain: dự án khách không đủ lớn để mua site license Nutrient; ghép nhiều lib OSS tốn 2-3 tháng và vẫn thiếu annotation UI
- Mức chi: $999–1,999 team license, one-time

**Tertiary Customer (Cloud tier):**
- Team nhỏ cần xử lý hàng loạt PDF (extract data, redact PII, OCR) mà không muốn tự host infra
- Pain: tự build pipeline OCR + AI extraction tốn thời gian, các API cloud hiện có (Google Document AI, AWS Textract) đắt theo page và khó tùy biến UI
- Mức chi: $49–199/tháng theo volume xử lý

### 2.4 Total Addressable Market (TAM) Estimate

**Ước tính dựa trên tín hiệu giá của Nutrient/Apryse:**

- Nutrient có publicly visible customer base trải dài Fortune 500 tới startup, giá trung bình $76k/năm cho customer ký hợp đồng
- Thị trường document SDK/processing toàn cầu (bao gồm OCR, e-signature embed, viewer SDK): ước tính hàng trăm triệu USD/năm, phân mảnh giữa nhiều vendor

**Thị phần khả thi cho FluxDocs:**

- Năm 1: 30–60 Pro license × $499 trung bình = $15–30k
- Năm 2: 150–300 Pro + 30 Cloud sub × $99 trung bình = $90–180k ARR
- Năm 3: 500+ Pro + 150 Cloud + 5-10 Enterprise self-host deal ($5-15k/deal) = $300–600k ARR

So với Gantt chart, ceiling giá cao hơn rõ rệt nhờ benchmark thị trường đã có (Nutrient chứng minh khách enterprise sẵn sàng trả $25k-200k+/năm cho đúng category sản phẩm này).

---

## 3. Product Positioning & Branding

### 3.1 Brand Identity

| | |
|---|---|
| **Product Name** | FluxDocs |
| **Brand Family** | Flux (modern web tooling) |
| **Family Members** | FluxFiles (file manager, đang ship)<br>FluxGantt (Gantt chart)<br>FluxDocs (PDF viewer/annotator SDK, sản phẩm này)<br>FluxBoard (Kanban, tương lai)<br>FluxFlow (workflow editor, tương lai) |

### 3.2 Tagline & Positioning

> **Tagline:** "View. Annotate. Extract. All in Go, all in MIT."

> **Positioning:** "SDK PDF MIT-licensed đầu tiên với core Go thật và AI document extraction — chạy được cả server và client qua WASM."

> **Elevator Pitch:** "Mọi sản phẩm xử lý tài liệu đều cần PDF viewer và annotation, và mọi developer xây nó đều đối mặt cùng lựa chọn: trả $25,000+/năm cho Nutrient, ghép 4 thư viện JS rời rạc thiếu tính năng, hoặc đụng vào MuPDF rồi vướng AGPL license. FluxDocs là lựa chọn còn thiếu — core Go nhanh, MIT-licensed, compile WASM để xử lý hoàn toàn client-side khi cần privacy, và có AI extraction tích hợp sẵn."

### 3.3 Brand Voice

| | |
|---|---|
| **Tone** | Trực tiếp, kỹ thuật, tự tin nhưng không kiêu |
| **Reference** | Văn phong docs của Caddy (Go project nổi tiếng về docs sạch), Tiptap, Drizzle ORM |
| **Tránh** | Marketing sáo rỗng: "revolutionary", "enterprise-grade" lặp vô nghĩa |
| **Ưu tiên** | Benchmark performance cụ thể (ms/page render, MB/s throughput), so sánh giá trực tiếp với Nutrient |

### 3.4 Visual Identity

| | |
|---|---|
| **Primary Color** | Indigo `#6366f1` — kế thừa từ FluxGantt, nhất quán brand family |
| **Annotation Color** | Amber `#f59e0b` — màu mặc định cho highlight/note, khác biệt với indigo hệ thống |
| **Background** | Near-black `#0a0a0a` (dark mode), off-white `#fafafa` (light mode) |
| **Typography** | Inter (UI), JetBrains Mono (code samples) |
| **Logo Concept** | Trang giấy cách điệu với góc bo, đường annotation chạy ngang như highlight |

### 3.5 Domain & Online Presence

| | |
|---|---|
| **Primary domain** | fluxdocs.dev |
| **Secondary** | fluxdocs.com (redirect về .dev) |
| **NPM scope** | `@fluxdocs` (cho wrapper JS/WASM) |
| **Go module** | `github.com/fluxtoolkit/fluxdocs` |
| **GitHub** | github.com/fluxtoolkit/fluxdocs |
| **Twitter/X** | @fluxdocs |
| **Discord** | Flux Toolkit community server (chung với FluxFiles, FluxGantt) |

---

## 4. Technology Stack

### 4.1 Core Engine

| Layer | Choice |
|---|---|
| **Language** | Go 1.23+ |
| **Module format** | Go module chuẩn; build target thêm WASM (`GOOS=js GOARCH=wasm`) |
| **Architecture** | Headless core (parse + compute + render-to-buffer) tách biệt với mọi UI |
| **PDF parsing** | Tự viết parser theo PDF 32000-1:2008 spec cho phần core (object model, xref, stream), có thể gọi CGO tới MuPDF cho rendering chất lượng cao ở chế độ "high-fidelity" tùy chọn (build tag riêng, không bắt buộc) |
| **Rendering** | Raster (PNG/JPEG buffer) cho server-side thumbnail/preview; SVG cho client-side vector-accurate; WASM build dùng Canvas API của browser để paint |
| **Concurrency** | Goroutine pool xử lý multi-page rendering song song — lợi thế tự nhiên của Go so với JS single-thread (trừ Worker) |
| **OCR** | Binding Tesseract qua CGO (Pro tier), có thể thay bằng OCR cloud API (Cloud tier) |
| **AI integration** | Gọi LLM API (Claude, qua HTTP client Go chuẩn) cho document Q&A, structured extraction — không cần SDK ngôn ngữ khác |
| **Testing** | `go test` + table-driven test cho parser; golden-file test cho rendering (so sánh output PNG với reference) |
| **Monorepo** | Go workspace (`go.work`) cho multi-module, kết hợp pnpm workspace cho phần JS wrapper/demo |

### 4.2 Client Wrappers

**Wave 1:**
- `@fluxdocs/react` — React 18+, component `<FluxDocsViewer />` load WASM core
- `@fluxdocs/vue` — Vue 3+, Composition API tương đương

**Wave 2:**
- `@fluxdocs/web-components` — Custom element thuần, dùng được trong mọi framework hoặc vanilla HTML
- Go native package dùng trực tiếp trong backend Go (không cần wrapper — đây là lợi thế so với FluxGantt vì core đã là Go)

**Community-driven:**
- `@fluxdocs/svelte`
- Binding Python qua cgo-export (cho team data/ML muốn xử lý batch PDF)

### 4.3 Cloud Backend (Wave 3)

| | |
|---|---|
| **Runtime** | Go binary chạy trực tiếp (không cần Node.js runtime riêng cho backend) |
| **Framework** | Chi (router HTTP nhẹ, idiomatic Go) hoặc Echo |
| **Database** | PostgreSQL 16 |
| **ORM** | sqlc (generate type-safe Go code từ SQL, giữ đúng triết lý "ít magic" của Go) hoặc Drizzle nếu cần Node service phụ trợ |
| **Object storage** | Cloudflare R2 (lưu file PDF gốc + annotation layer JSON) |
| **Queue xử lý batch** | Go channel + worker pool nội bộ cho job nhỏ; River (Postgres-backed queue, Go-native) cho job lớn cần persistence |
| **Auth** | Better-Auth hoặc tự viết bằng Go (JWT + OAuth, ít dependency) |
| **CDN** | Cloudflare (free tier) |
| **Hosting** | Fly.io — đặc biệt hợp với Go vì binary nhẹ, cold-start nhanh |
| **Email** | Resend (transactional) |
| **Payments** | Stripe (Pro one-time + Cloud subscription) |
| **Analytics** | Plausible (privacy-first) |

### 4.4 Documentation Site

| | |
|---|---|
| **Framework** | Vocs (kế thừa từ FluxGantt) hoặc Hugo (Go-native static site, hợp branding "all Go") |
| **Hosting** | Cloudflare Pages |
| **Code examples** | Go Playground embed cho server-side snippet; StackBlitz cho client wrapper |

---

## 5. System Architecture

### 5.1 Layered Architecture

```
┌───────────────────────────────────────────────────────────┐
│  User Application Layer                                   │
│  (React, Vue, vanilla JS, hoặc Go backend trực tiếp)      │
└───────────────────────────────────────────────────────────┘
                              │
                ┌─────────────┴─────────────┐
                ▼                           ▼
┌───────────────────────────┐   ┌───────────────────────────┐
│  Client Wrapper Layer     │   │  Go Native Usage          │
│  @fluxdocs/react/vue      │   │  import "fluxdocs/core"   │
│  - Load WASM core         │   │  - Dùng trực tiếp trong   │
│  - Canvas paint binding   │   │    backend Go, không cần  │
│  - Event binding cho      │   │    wrapper hay network    │
│    annotation tools       │   │    hop                    │
└───────────────────────────┘   └───────────────────────────┘
                │                           │
                └─────────────┬─────────────┘
                              ▼
┌───────────────────────────────────────────────────────────┐
│  Core Engine (fluxdocs/core) — viết 100% bằng Go          │
│                                                             │
│  ┌───────────────────────────────────────────────────────┐│
│  │  Public API: OpenDocument(), Render(), Annotate()      ││
│  └───────────────────────────────────────────────────────┘│
│  ┌───────────────────────────────────────────────────────┐│
│  │  Parse Layer                                          ││
│  │  - Object model (dict, array, stream, xref)            ││
│  │  - Page tree resolver                                  ││
│  │  - Font/encoding decoder                               ││
│  │  - Content stream tokenizer                            ││
│  └───────────────────────────────────────────────────────┘│
│  ┌───────────────────────────────────────────────────────┐│
│  │  Render Layer                                         ││
│  │  - Raster renderer (PNG/JPEG buffer)                   ││
│  │  - SVG renderer (vector, client-side accurate)         ││
│  │  - Goroutine pool cho multi-page parallel render        ││
│  └───────────────────────────────────────────────────────┘│
│  ┌───────────────────────────────────────────────────────┐│
│  │  Annotation Layer                                     ││
│  │  - AnnotationStore (reactive, theo task store FluxGantt)││
│  │  - Highlight, note, draw, shape, stamp                 ││
│  │  - Serialize annotation độc lập với PDF gốc (JSON layer)││
│  └───────────────────────────────────────────────────────┘│
│  ┌───────────────────────────────────────────────────────┐│
│  │  Document Ops Layer                                   ││
│  │  - Merge / split / rotate / reorder page                ││
│  │  - Redact (xóa vĩnh viễn nội dung, không chỉ che)        ││
│  │  - Watermark                                            ││
│  │  - Form fill (AcroForm + XFA cơ bản)                     ││
│  │  - Digital signature (PKCS#7)                           ││
│  └───────────────────────────────────────────────────────┘│
│  ┌───────────────────────────────────────────────────────┐│
│  │  Extraction Layer                                     ││
│  │  - Text extraction theo layout (giữ thứ tự đọc)         ││
│  │  - Table detection                                     ││
│  │  - OCR adapter (Tesseract CGO hoặc cloud)               ││
│  │  - AI structured extraction (Cloud, gọi LLM)            ││
│  └───────────────────────────────────────────────────────┘│
│  ┌───────────────────────────────────────────────────────┐│
│  │  Sync Layer (chỉ Cloud)                                ││
│  │  - Annotation sync nhiều người dùng (CRDT-lite)          ││
│  │  - Comment thread per annotation                        ││
│  │  - Version history document                             ││
│  └───────────────────────────────────────────────────────┘│
└───────────────────────────────────────────────────────────┘
```

### 5.2 Design Principles

1. **Core Go thật, không phải Go giả** — Khác với nhiều dự án gắn Go vào use case UI-heavy (nơi Go chỉ làm relay/orchestration), ở đây phần nặng nhất — parse + render PDF — chạy hoàn toàn trong Go. Đây là điểm khác biệt kỹ thuật cốt lõi so với việc "viết Go cho có".

2. **Một core, hai target build** — Cùng codebase Go compile ra native binary (server, CLI) và WASM (browser). Không cần maintain hai implementation riêng như nhiều SDK khác (JS cho client, ngôn ngữ khác cho server).

3. **Annotation tách biệt khỏi PDF gốc** — Annotation lưu dưới dạng JSON layer riêng, áp lên PDF khi render hoặc khi export "flatten". Cho phép multi-user annotate không sửa file gốc, dễ undo, dễ đồng bộ.

4. **Goroutine-native cho batch xử lý** — Render nhiều trang, OCR nhiều file, extract nhiều document — tất cả dùng worker pool Go, không cần thread pool ngoài hay async runtime phức tạp.

5. **Privacy-first qua WASM** — Với tài liệu nhạy cảm (hợp đồng, hồ sơ y tế), toàn bộ pipeline có thể chạy trong browser, không gửi byte nào lên server. Đây là use case mà SDK cloud-first (Nutrient Cloud) không đáp ứng tốt.

6. **Type safety qua Go struct, không cần TypeScript riêng** — Vì core là Go, server-side consumer (backend Go khác) dùng trực tiếp type Go, không cần lớp transform JSON ↔ type như khi core viết bằng ngôn ngữ khác.

7. **License rõ ràng, tách core/CGO** — Phần core Go thuần (parse, render cơ bản, annotation, document ops) giữ MIT 100%. Phần tùy chọn dùng CGO (OCR Tesseract, high-fidelity render qua MuPDF) đóng gói build tag riêng để người dùng tự quyết có chấp nhận license phụ thuộc đó hay không.

---

## 6. Core Type System

### 6.1 Core Types (Go)

```go
package fluxdocs

import "time"

// DocumentID, PageID, AnnotationID dùng type alias có kiểm soát
// để tránh nhầm lẫn giữa các loại ID (tương đương branded type trong TS)
type DocumentID string
type PageID string
type AnnotationID string
type LayerID string

type Document struct {
	ID          DocumentID
	Title       string
	PageCount   int
	Pages       []Page
	Metadata    DocumentMetadata
	Encrypted   bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type DocumentMetadata struct {
	Author   string
	Subject  string
	Keywords []string
	Producer string
	Custom   map[string]string
}

type Page struct {
	ID       PageID
	Index    int     // 0-based
	Width    float64 // points (1/72 inch)
	Height   float64
	Rotation int     // 0, 90, 180, 270
}

type AnnotationType string

const (
	AnnotationHighlight AnnotationType = "highlight"
	AnnotationNote      AnnotationType = "note"
	AnnotationDraw      AnnotationType = "draw"
	AnnotationShape     AnnotationType = "shape"
	AnnotationStamp     AnnotationType = "stamp"
	AnnotationRedact    AnnotationType = "redact"
	AnnotationSignature AnnotationType = "signature"
)

type Annotation struct {
	ID        AnnotationID
	PageID    PageID
	Type      AnnotationType
	Rect      Rect
	Color     string  // hex, ví dụ "#f59e0b"
	Opacity   float64 // 0..1
	Content   string  // text nếu là note/stamp
	Points    []Point // dùng cho draw (freehand)
	AuthorID  string
	CreatedAt time.Time
	UpdatedAt time.Time
	Meta      map[string]any
}

type Rect struct {
	X, Y, Width, Height float64
}

type Point struct {
	X, Y float64
}

type FormField struct {
	Name     string
	Type     FormFieldType
	Value    string
	Options  []string // cho dropdown/radio
	Required bool
	ReadOnly bool
}

type FormFieldType string

const (
	FieldText     FormFieldType = "text"
	FieldCheckbox FormFieldType = "checkbox"
	FieldRadio    FormFieldType = "radio"
	FieldDropdown FormFieldType = "dropdown"
	FieldSignature FormFieldType = "signature"
)

type RenderOptions struct {
	DPI         int    // mặc định 150
	Format      string // "png" | "jpeg" | "svg"
	PageRange   []int  // rỗng = toàn bộ
	Quality     int    // 1-100, áp dụng cho jpeg
}

type ExtractOptions struct {
	PreserveLayout bool
	IncludeTables  bool
	OCRFallback    bool // nếu trang là ảnh scan, fallback sang OCR
}
```

### 6.2 Annotation Layer (lưu độc lập với PDF)

```go
type AnnotationLayer struct {
	ID         LayerID
	DocumentID DocumentID
	Name       string // ví dụ "Bản review của Long"
	Annotations []Annotation
	Visible    bool
	CreatedAt  time.Time
}
```

### 6.3 Configuration Type

```go
type ViewerConfig struct {
	Theme            string // "light" | "dark" | "auto"
	InitialZoom      float64
	EnableAnnotation bool
	EnableForms      bool
	ReadOnly         bool
	Locale           string

	OnAnnotationChange func(a Annotation)
	OnPageChange       func(pageIndex int)
	OnFormFieldChange  func(field FormField)
}
```

---

## 7. Public API Specification

### 7.1 Mở document (Go native)

```go
package main

import "github.com/fluxtoolkit/fluxdocs/core"

func main() {
	doc, err := core.OpenDocument("contract.pdf")
	if err != nil {
		panic(err)
	}
	defer doc.Close()

	// Render trang đầu ra PNG, 150 DPI
	img, err := doc.RenderPage(0, core.RenderOptions{
		DPI:    150,
		Format: "png",
	})
}
```

### 7.2 Document Operations

```go
doc.GetPage(index int) (*Page, error)
doc.GetPageCount() int
doc.RenderPage(index int, opts RenderOptions) ([]byte, error)
doc.RenderAllPages(opts RenderOptions) ([][]byte, error)
doc.Merge(other *Document) (*Document, error)
doc.Split(pageRanges [][2]int) ([]*Document, error)
doc.Rotate(pageIndex int, degrees int) error
doc.Reorder(newOrder []int) error
doc.Save(path string) error
doc.SaveTo(w io.Writer) error
doc.Close() error
```

### 7.3 Annotation Operations

```go
doc.AddAnnotation(a Annotation) (Annotation, error)
doc.UpdateAnnotation(id AnnotationID, patch AnnotationPatch) (Annotation, error)
doc.RemoveAnnotation(id AnnotationID) error
doc.GetAnnotations(pageID PageID) ([]Annotation, error)
doc.FlattenAnnotations() (*Document, error) // bake annotation vào PDF, không sửa được nữa
doc.ExportAnnotationLayer() (*AnnotationLayer, error)
doc.ImportAnnotationLayer(layer AnnotationLayer) error
```

### 7.4 Redaction (Pro)

```go
doc.RedactText(pageIndex int, pattern string) ([]Rect, error)       // theo regex, ví dụ số CMND
doc.RedactArea(pageIndex int, rect Rect) error                       // theo vùng chỉ định
doc.RedactAndFlatten() (*Document, error)                            // xóa vĩnh viễn, không phục hồi được
```

### 7.5 Forms (Pro)

```go
doc.GetFormFields() ([]FormField, error)
doc.SetFormFieldValue(name string, value string) error
doc.FlattenForm() (*Document, error)
doc.SignDocument(opts SignatureOptions) (*Document, error)
```

### 7.6 Extraction

```go
doc.ExtractText(opts ExtractOptions) (string, error)
doc.ExtractTextByPage(pageIndex int, opts ExtractOptions) (string, error)
doc.ExtractTables(pageIndex int) ([]Table, error)
doc.OCRPage(pageIndex int) (string, error)                          // Pro, cần Tesseract build tag
doc.ExtractStructured(schema any) (map[string]any, error)            // Cloud, gọi LLM
doc.AskDocument(question string) (string, error)                     // Cloud, document Q&A
```

### 7.7 Events (qua callback, không có event loop native trong Go)

```go
doc.OnAnnotationChange(func(a Annotation))
doc.OnPageRendered(func(pageIndex int, img []byte))
doc.OnFormFieldChange(func(f FormField))
```

### 7.8 React Wrapper Example (WASM core qua client)

```tsx
import { FluxDocsViewer, useFluxDocs } from '@fluxdocs/react';

function ContractReview() {
  const { ref, addAnnotation, extractText } = useFluxDocs({
    src: '/contracts/agreement.pdf',
    onAnnotationChange: (a) => saveToBackend(a),
  });

  return (
    <div>
      <button onClick={() => addAnnotation({ type: 'highlight', pageId: 'p0', rect })}>
        Highlight
      </button>
      <FluxDocsViewer ref={ref} theme="dark" style={{ height: 800 }} />
    </div>
  );
}
```

### 7.9 HTTP API (Cloud tier, dùng cho integration không cần Go)

```
POST   /v1/documents                  upload PDF, trả về documentId
GET    /v1/documents/:id/pages/:n     render trang dạng PNG/SVG
POST   /v1/documents/:id/annotations  thêm annotation
GET    /v1/documents/:id/annotations  lấy toàn bộ annotation layer
POST   /v1/documents/:id/extract      trích xuất text/table
POST   /v1/documents/:id/ask          AI document Q&A
POST   /v1/documents/:id/redact       redact theo pattern/area
```

---

## 8. UI/UX Design System

### 8.1 Visual Philosophy

FluxDocs là công cụ business chuyên nghiệp, đối tượng dùng thường ở môi trường nhạy cảm (pháp lý, y tế, tài chính). Aesthetic phải truyền tải "đáng tin cậy, chính xác" hơn là "sáng tạo". Chủ động tránh:

- Màu sắc annotation quá sặc sỡ gây mất tập trung khỏi nội dung tài liệu
- Animation rườm rà khi chuyển trang
- Icon trừu tượng khó hiểu cho tool annotation (luôn dùng icon rõ nghĩa: bút highlight, ghi chú, hình chữ nhật)

Ưu tiên:

- Document canvas là trung tâm, toolbar thu nhỏ tối đa khi không cần
- Density cao cho sidebar (danh sách annotation, outline, thumbnail trang)
- Trạng thái rõ ràng: đang ở chế độ xem hay chế độ annotate, có lưu chưa

### 8.2 Design Tokens

```css
:root {
  /* Typography */
  --fd-font-sans:        'Inter', system-ui, sans-serif;
  --fd-font-mono:        'JetBrains Mono', ui-monospace, monospace;
  --fd-font-size-xs:     11px;
  --fd-font-size-sm:     12px;
  --fd-font-size-base:   13px;

  /* Light theme */
  --fd-bg:               #fafafa;
  --fd-bg-canvas:        #e5e7eb;   /* nền quanh trang PDF, tạo độ tương phản */
  --fd-fg:               #18181b;
  --fd-fg-muted:         #71717a;
  --fd-border:           #e5e7eb;

  /* Dark theme */
  --fd-bg-dark:          #0a0a0a;
  --fd-bg-canvas-dark:   #18181b;
  --fd-fg-dark:          #fafafa;

  /* Annotation colors */
  --fd-highlight-yellow: #fde047;
  --fd-highlight-green:  #86efac;
  --fd-highlight-blue:   #93c5fd;
  --fd-highlight-pink:   #f9a8d4;
  --fd-note-bg:          #f59e0b;
  --fd-draw-default:     #ef4444;
  --fd-redact-fill:      #18181b;   /* đen đặc, không trong suốt */

  /* Page chrome */
  --fd-page-shadow:      0 1px 3px rgba(0,0,0,0.12), 0 1px 2px rgba(0,0,0,0.08);
  --fd-page-border:      #d4d4d8;

  /* Animations */
  --fd-transition-fast:     100ms ease-out;
  --fd-transition-default:  150ms ease-out;
}
```

### 8.3 Layout

```
┌──────────────────────────────────────────────────────────────────┐
│  Toolbar                                                         │
│  [Highlight] [Note] [Draw] [Shape] [Redact] [Sign]               │
│  [Zoom -] [100%] [Zoom +]   [Search] [Export] [Share]            │
├───────────┬────────────────────────────────────────┬─────────────┤
│ Thumbnail │                                        │ Annotation  │
│ sidebar   │           Document canvas              │ list        │
│           │                                        │             │
│ ┌───────┐ │   ┌──────────────────────────────┐     │ ┌─────────┐ │
│ │ Pg 1  │ │   │                              │     │ │ Highlight│ │
│ ├───────┤ │   │      (nội dung trang PDF)     │     │ │ "Điều 3" │ │
│ │ Pg 2  │ │   │                              │     │ ├─────────┤ │
│ │ Pg 3  │ │   │  ▓▓▓▓ highlight               │     │ │ Note     │ │
│ └───────┘ │   │                              │     │ │ "Cần xác │ │
│           │   └──────────────────────────────┘     │ │  nhận"   │ │
│           │                                        │ └─────────┘ │
└───────────┴────────────────────────────────────────┴─────────────┘
```

### 8.4 Interaction Patterns

**Annotation tools:**

| Hành động | Kết quả |
|---|---|
| Chọn tool Highlight, kéo qua text | Tạo highlight bám theo dòng text được chọn |
| Chọn tool Note, click vào trang | Tạo note pin, mở popup nhập nội dung |
| Chọn tool Draw, kéo chuột | Vẽ freehand, lưu thành chuỗi Point |
| Chọn tool Redact, kéo vùng | Đánh dấu vùng redact (chưa xóa, chỉ preview tới khi flatten) |
| Double click annotation | Mở popup edit nội dung/màu |
| Right click annotation | Context menu: Delete, Change color, Reply |

**Keyboard:**

| Phím | Hành động |
|---|---|
| Arrow Up/Down | Chuyển trang |
| Cmd/Ctrl + F | Mở search trong document |
| Cmd/Ctrl + +/- | Zoom in/out |
| Cmd/Ctrl + S | Save (flatten annotation nếu cần) |
| Escape | Thoát chế độ annotate, về chế độ xem |
| Delete | Xóa annotation đang chọn |

### 8.5 Accessibility

- WCAG 2.1 AA tối thiểu cho toolbar và sidebar (canvas PDF chính nó kế thừa accessibility từ structure PDF gốc, FluxDocs không thể cải thiện PDF không có tag accessibility)
- Toàn bộ annotation tool điều khiển được bằng keyboard
- Hỗ trợ PDF/UA (Tagged PDF) khi document gốc có, đọc được qua screen reader
- Focus indicator rõ trên mọi annotation và toolbar button
- Tôn trọng `prefers-reduced-motion` cho transition chuyển trang/zoom

---

## 9. Feature Roadmap (3 Waves)

### 9.1 Wave 1 — Free MVP (Tier: Core MIT, Tuần 1–8)

**Mục tiêu:** Ship một PDF viewer + annotation engine Go hoạt động đầy đủ, đủ tốt để thay thế việc ghép pdf.js + pdf-lib, đủ để thu hút GitHub star và npm download ban đầu.

**Tuần 1–2: Foundation**
- Setup Go module + workspace, CI (GitHub Actions) build cả native và WASM target
- PDF parser: object model, xref table, page tree resolver
- Decode content stream cơ bản (text, path đơn giản)
- Render trang ra raster PNG (DPI tùy chỉnh)
- CLI tool `fluxdocs` cơ bản: render, info, extract-text

**Tuần 3–4: Rendering nâng cao + WASM**
- Font decoder (TrueType, Type1 cơ bản — đủ cho 90% PDF thực tế)
- Render path vector phức tạp (Bezier curve, clipping)
- Build WASM target, test chạy trong browser qua Canvas
- Goroutine pool cho render multi-page song song

**Tuần 5: Annotation Engine**
- AnnotationStore (reactive layer, JSON-serializable)
- Highlight, note, draw, shape — đủ 4 loại cơ bản
- Render annotation layer chồng lên canvas (cả native và WASM)
- Export/import annotation layer riêng biệt với PDF gốc

**Tuần 6: Client Wrappers**
- `@fluxdocs/react` load WASM, component `<FluxDocsViewer />`
- `@fluxdocs/vue` tương đương
- Sample app cho mỗi framework

**Tuần 7: Polish & Document Ops**
- Merge / split / rotate / reorder page
- Text extraction theo layout (giữ thứ tự đọc đúng, không chỉ dump theo object order)
- Export annotation đã flatten ra PDF mới
- Mobile responsive cho viewer UI

**Tuần 8: Documentation & Launch Prep**
- Documentation site (Hugo hoặc Vocs)
- 10+ example: Go server-side, React client-side, CLI batch processing
- Landing page với demo "redact PII trực tiếp trong browser, không gửi server"
- README quick start
- Trang so sánh (vs Nutrient, vs ghép pdf.js+pdf-lib)
- Draft Show HN, asset Product Hunt

### 9.2 Wave 2 — Pro Tier (Tuần 11–18)

**Mục tiêu:** Thêm tính năng developer chịu trả $299–799 one-time — đúng nhóm tính năng Nutrient tính phí riêng.

**Tuần 11–12: Forms & Signature**
- Parse AcroForm field (text, checkbox, radio, dropdown)
- Fill form qua API, flatten form thành static content
- Digital signature (PKCS#7), verify signature có sẵn trong PDF
- Signature pad UI component cho client wrapper

**Tuần 13–14: Redaction**
- Redact theo regex pattern (số CMND, thẻ tín dụng, email)
- Redact theo vùng chỉ định bằng tay
- Flatten redaction — đảm bảo nội dung bị xóa vĩnh viễn ở object stream, không chỉ vẽ đè (đây là lỗi bảo mật phổ biến ở nhiều tool redact kém)
- Test suite xác minh nội dung đã redact không thể recover qua text extraction hoặc copy-paste

**Tuần 15–16: OCR & Compare**
- Tích hợp Tesseract qua CGO (build tag `ocr`)
- OCR fallback tự động khi `ExtractText` trả về rỗng (trang là ảnh scan)
- Document compare: diff hai version PDF, highlight phần thay đổi
- Watermark (text/image, áp lên toàn bộ hoặc trang chỉ định)

**Tuần 17: Advanced Export & Polish**
- Export PDF/A (chuẩn lưu trữ dài hạn)
- Batch processing CLI (xử lý hàng loạt file theo glob pattern)
- Performance benchmark suite, publish số liệu so với Nutrient/MuPDF

**Tuần 18: Pro Launch**
- License key validation
- Stripe Checkout integration
- Pro documentation, migration guide từ pdf.js/pdf-lib
- Public Pro launch

### 9.3 Wave 3 — Cloud + AI Tier (Tháng 6+)

**Mục tiêu:** Recurring revenue qua AI document processing — đúng category Nutrient bán riêng (XtractFlow) với giá cao.

**Tháng 6–7: Cloud Foundation**
- Backend API (Go + Chi/Echo + Postgres)
- Upload document, lưu vào R2, queue xử lý qua River
- Auth + organization model
- Stripe subscription theo volume xử lý

**Tháng 8–9: AI Extraction**
- `ExtractStructured()` — đưa schema (ví dụ "tên, ngày, số tiền hợp đồng"), LLM trả về structured JSON
- `AskDocument()` — document Q&A qua LLM, có citation về trang/vị trí trong PDF
- Auto-redact PII bằng AI detection (không cần viết regex tay)
- Table extraction nâng cao bằng vision model cho bảng phức tạp

**Tháng 10–11: Collaboration**
- Multi-user annotation sync (CRDT-lite, không cần full Yjs vì annotation ít conflict hơn text editing)
- Comment thread theo từng annotation
- Share link với permission (view/comment/edit)
- Version history document

**Tháng 12: Integrations**
- Webhook (document processed, annotation added)
- Zapier connector
- Tích hợp DocuSign-style flow (request signature qua email)
- Export tới Google Drive/Dropbox sau khi xử lý

---

## 10. API Naming Conventions

### 10.1 Method Naming (Go idiomatic)

PascalCase cho exported method, verb đứng trước, tuân theo Go convention chuẩn (không dùng "Get" prefix khi trả về field đơn giản, theo Effective Go).

**Nên dùng:**
```go
doc.RenderPage(0, opts)
doc.AddAnnotation(a)
doc.ExtractText(opts)
doc.RedactArea(pageIndex, rect)
doc.Merge(other)
```

**Tránh:**
```go
doc.render_page(0, opts)        // snake_case không phải Go convention
doc.GetRenderedPageAsImage(0)   // dài dòng, "Get" dư thừa
doc.DoOperation("render", 0)    // generic action, mất type safety
```

### 10.2 Error Handling

Theo chuẩn Go: trả `error` là giá trị thứ hai, không dùng panic cho lỗi runtime thông thường (chỉ panic cho lỗi không thể phục hồi, ví dụ bug logic nội bộ).

```go
img, err := doc.RenderPage(0, opts)
if err != nil {
    // xử lý lỗi cụ thể, ví dụ kiểm tra errors.Is(err, fluxdocs.ErrPageNotFound)
}
```

Định nghĩa sentinel error cho case phổ biến:

```go
var (
	ErrPageNotFound      = errors.New("fluxdocs: page not found")
	ErrEncryptedDocument = errors.New("fluxdocs: document is encrypted")
	ErrInvalidPDF        = errors.New("fluxdocs: invalid PDF structure")
)
```

### 10.3 CSS Class Naming (BEM, cho client UI)

Prefix `fd-` để tránh xung đột với host application.

```css
.fd-viewer { }
.fd-viewer__canvas { }
.fd-viewer__toolbar { }
.fd-annotation { }
.fd-annotation--highlight { }
.fd-annotation--note { }
.fd-annotation--selected { }
.fd-page { }
.fd-page__thumbnail { }
```

CSS custom property prefix: `--fd-*`

### 10.4 Package Naming (Go)

Theo convention Go: tên package ngắn, lowercase, không underscore, mô tả đúng chức năng.

```
fluxdocs/core           # engine chính
fluxdocs/render         # render logic (raster + svg)
fluxdocs/annotation      # annotation engine
fluxdocs/extract        # text/table extraction
fluxdocs/ocr            # OCR adapter (build tag riêng)
fluxdocs/sign           # digital signature
fluxdocs/cloud          # HTTP client gọi Cloud API
```

### 10.5 NPM Package Names (cho client wrapper)

| Package | Mô tả |
|---|---|
| `@fluxdocs/core` | WASM build + JS loader |
| `@fluxdocs/react` | React wrapper (Wave 1) |
| `@fluxdocs/vue` | Vue wrapper (Wave 1) |
| `@fluxdocs/web-components` | Custom element thuần (Wave 2) |
| `@fluxdocs/cloud-sdk` | Cloud API client (Wave 3) |

---

## 11. Code Organization

### 11.1 Monorepo Structure

```
fluxdocs/
├── core/                            # module Go chính
│   ├── document.go                  # Document type + OpenDocument()
│   ├── parse/
│   │   ├── object.go                # object model PDF
│   │   ├── xref.go                   # cross-reference table
│   │   ├── pagetree.go               # resolve page tree
│   │   ├── contentstream.go          # tokenize content stream
│   │   └── font.go                   # font decoder
│   ├── render/
│   │   ├── raster.go                 # render PNG/JPEG
│   │   ├── svg.go                    # render SVG
│   │   ├── pool.go                   # goroutine pool multi-page
│   │   └── canvas_wasm.go            # binding Canvas API (build tag wasm)
│   ├── annotation/
│   │   ├── store.go                  # AnnotationStore
│   │   ├── highlight.go
│   │   ├── note.go
│   │   ├── draw.go
│   │   └── layer.go                  # serialize/deserialize layer
│   ├── docops/
│   │   ├── merge.go
│   │   ├── split.go
│   │   ├── rotate.go
│   │   ├── redact.go
│   │   └── watermark.go
│   ├── form/
│   │   ├── acroform.go
│   │   └── fill.go
│   ├── sign/
│   │   └── pkcs7.go
│   ├── extract/
│   │   ├── text.go
│   │   ├── table.go
│   │   └── layout.go
│   ├── ocr/
│   │   └── tesseract.go              # build tag: ocr
│   ├── errors.go
│   ├── types.go
│   └── fluxdocs.go                   # public API entry
│
├── cmd/
│   └── fluxdocs/
│       └── main.go                   # CLI tool
│
├── cloud/                            # backend Go cho Cloud tier
│   ├── api/
│   │   ├── handlers/
│   │   ├── middleware/
│   │   └── router.go
│   ├── queue/
│   │   └── worker.go
│   ├── db/
│   │   ├── schema.sql
│   │   └── queries.sql               # cho sqlc generate
│   └── main.go
│
├── wasm/
│   └── main.go                       # entry point build WASM, export func qua syscall/js
│
├── packages/                         # phần JS/TS wrapper
│   ├── core/                         # @fluxdocs/core — load + wrap WASM
│   ├── react/                        # @fluxdocs/react
│   ├── vue/                          # @fluxdocs/vue
│   └── cloud-sdk/                    # @fluxdocs/cloud-sdk
│
├── examples/
│   ├── go-server-demo/
│   ├── react-vite-demo/
│   ├── vue-demo/
│   ├── cli-batch-redact-demo/
│   └── ai-extraction-demo/
│
├── apps/
│   ├── docs/                         # documentation site
│   └── landing/                      # landing page
│
├── go.work                           # Go workspace, multi-module
├── go.mod
├── pnpm-workspace.yaml               # cho phần packages/ JS
├── README.md
├── LICENSE                           # MIT (core)
└── CONTRIBUTING.md
```

---

## 12. Database Schema (Cloud Tier)

PostgreSQL schema cho bản Cloud hosted.

```sql
-- Organizations
CREATE TABLE organizations (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name            VARCHAR(200) NOT NULL,
  slug            VARCHAR(100) UNIQUE NOT NULL,
  plan            VARCHAR(50) NOT NULL DEFAULT 'free',
  stripe_cust_id  VARCHAR(100),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Users
CREATE TABLE users (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email           VARCHAR(320) UNIQUE NOT NULL,
  name            VARCHAR(200),
  avatar_url      TEXT,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE memberships (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  role        VARCHAR(50) NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(user_id, org_id)
);

-- Documents
CREATE TABLE documents (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  title           VARCHAR(500) NOT NULL,
  page_count      INT NOT NULL,
  storage_key     TEXT NOT NULL,          -- key trong R2
  file_size_bytes BIGINT,
  sha256          VARCHAR(64),             -- dedupe + integrity check
  status          VARCHAR(50) DEFAULT 'ready', -- processing/ready/failed
  uploaded_by     UUID REFERENCES users(id),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_documents_org ON documents(org_id);

-- Annotation layers
CREATE TABLE annotation_layers (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  document_id   UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  name          VARCHAR(200) NOT NULL,
  created_by    UUID REFERENCES users(id),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Annotations
CREATE TABLE annotations (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  layer_id    UUID NOT NULL REFERENCES annotation_layers(id) ON DELETE CASCADE,
  page_index  INT NOT NULL,
  type        VARCHAR(20) NOT NULL,        -- highlight/note/draw/shape/stamp/redact
  rect_x      NUMERIC(10,2),
  rect_y      NUMERIC(10,2),
  rect_w      NUMERIC(10,2),
  rect_h      NUMERIC(10,2),
  color       VARCHAR(20),
  opacity     NUMERIC(3,2) DEFAULT 1.0,
  content     TEXT,
  points      JSONB,                       -- cho draw freehand
  author_id   UUID REFERENCES users(id),
  meta        JSONB,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_annotations_layer ON annotations(layer_id);
CREATE INDEX idx_annotations_page ON annotations(layer_id, page_index);

-- Comments (theo annotation, cho collaboration Wave 3)
CREATE TABLE annotation_comments (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  annotation_id UUID NOT NULL REFERENCES annotations(id) ON DELETE CASCADE,
  user_id       UUID NOT NULL REFERENCES users(id),
  content       TEXT NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Processing jobs (OCR, extraction, AI)
CREATE TABLE processing_jobs (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  document_id   UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  job_type      VARCHAR(50) NOT NULL,      -- ocr/extract/ai_extract/ask
  status        VARCHAR(50) DEFAULT 'queued', -- queued/running/done/failed
  input         JSONB,                      -- ví dụ schema cho ai_extract, câu hỏi cho ask
  result        JSONB,
  error_message TEXT,
  started_at    TIMESTAMPTZ,
  completed_at  TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_jobs_document ON processing_jobs(document_id);
CREATE INDEX idx_jobs_status ON processing_jobs(status, created_at);

-- Share links
CREATE TABLE share_links (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  document_id     UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  token           VARCHAR(64) UNIQUE NOT NULL,
  password_hash   VARCHAR(255),
  permission      VARCHAR(20) DEFAULT 'view', -- view/comment/edit
  expires_at      TIMESTAMPTZ,
  view_count      INT DEFAULT 0,
  created_by      UUID REFERENCES users(id),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- API keys
CREATE TABLE api_keys (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name        VARCHAR(200) NOT NULL,
  key_hash    VARCHAR(255) NOT NULL,
  prefix      VARCHAR(10) NOT NULL,
  scopes      VARCHAR(100)[],
  last_used   TIMESTAMPTZ,
  created_by  UUID REFERENCES users(id),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  revoked_at  TIMESTAMPTZ
);
```

---

## 13. Algorithms Reference

### 13.1 PDF Object Resolution

PDF dùng cross-reference table (xref) để map object number → vị trí byte trong file. Việc resolve đúng object — đặc biệt với incremental update (PDF bị edit nhiều lần, có nhiều xref table) — là nền tảng để render đúng.

**Pseudocode:**

```
function resolveObject(file, objectNumber, generation):
    xrefTable = parseXrefChain(file)  // theo chuỗi /Prev nếu có incremental update
    entry = xrefTable.lookup(objectNumber, generation)

    if entry.type == 'free':
        return nil  // object đã bị xóa

    if entry.type == 'in-use':
        return parseObjectAt(file, entry.offset)

    if entry.type == 'compressed':
        // object nằm trong Object Stream (PDF 1.5+)
        objStream = resolveObject(file, entry.streamObjectNumber, 0)
        return objStream.extractObject(entry.indexInStream)
```

**Edge case cần xử lý:**
- File bị corrupt/truncated — fallback brute-force scan toàn file tìm `obj`/`endobj` marker
- Nhiều xref table do incremental update — phải theo đúng thứ tự `/Prev` để lấy version mới nhất
- Object stream lồng nhau (compressed object trong compressed object — hiếm nhưng spec cho phép)

### 13.2 Content Stream Rendering Pipeline

Content stream PDF là chuỗi operator (giống stack-based language nhỏ) mô tả cách vẽ trang — text, path, image, clipping.

```
function renderPage(page, canvas):
    graphicsState = GraphicsState.default()
    stateStack = []
    contentStream = decodeStream(page.Contents)
    tokens = tokenize(contentStream)

    for token in tokens:
        switch token.operator:
            case 'q':  stateStack.push(graphicsState.clone())
            case 'Q':  graphicsState = stateStack.pop()
            case 'cm': graphicsState.transform = graphicsState.transform.multiply(token.matrix)
            case 'Tf': graphicsState.font = resolveFont(token.fontName, token.size)
            case 'Tj': drawText(canvas, token.text, graphicsState)
            case 'm', 'l', 'c': graphicsState.path.addSegment(token)
            case 'f', 'S':      canvas.paintPath(graphicsState.path, graphicsState)
            case 'Do':          drawXObject(canvas, token.name, graphicsState)  // image hoặc form
            // ... ~40 operator khác theo spec PDF 32000-1

    return canvas
```

**Lưu ý thiết kế Go:** Vì content stream là tuyến tính và stateful (graphics state thay đổi theo thứ tự operator), việc render từng trang không parallel hóa được nội bộ — nhưng **giữa các trang** thì hoàn toàn độc lập, đây là chỗ goroutine pool phát huy tác dụng (xem 13.3).

### 13.3 Parallel Multi-Page Rendering (lợi thế Go)

```go
func (d *Document) RenderAllPages(opts RenderOptions) ([][]byte, error) {
	results := make([][]byte, d.PageCount)
	errs := make([]error, d.PageCount)

	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU()) // giới hạn concurrency theo số CPU

	for i := 0; i < d.PageCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			img, err := d.RenderPage(idx, opts)
			results[idx] = img
			errs[idx] = err
		}(i)
	}

	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}
```

Đây là chỗ Go có lợi thế tự nhiên so với Node.js (cần Worker Threads phức tạp hơn) hoặc Python (GIL hạn chế CPU-bound parallelism thật).

### 13.4 Redaction — Xóa Nội Dung Vĩnh Viễn (không chỉ vẽ đè)

Lỗi bảo mật phổ biến nhất ở tool redact kém: chỉ vẽ hình chữ nhật đen đè lên nội dung, nhưng text gốc vẫn còn trong content stream và có thể extract/copy được. FluxDocs xử lý đúng bằng cách sửa object stream thật.

```
function redactAndFlatten(page, redactRects):
    contentStream = decodeStream(page.Contents)
    tokens = tokenize(contentStream)
    newTokens = []

    for token in tokens:
        if token.operator == 'Tj' or token.operator == 'TJ':
            textBounds = computeTextBounds(token, currentGraphicsState)
            if intersectsAny(textBounds, redactRects):
                continue  // loại bỏ hoàn toàn operator vẽ text này, không giữ lại
        newTokens.append(token)

    page.Contents = encodeStream(reconstructStream(newTokens))

    // Vẽ vùng đen che lại (chỉ để hiển thị, không phải cơ chế bảo mật chính)
    for rect in redactRects:
        page.Contents.append(drawFilledRect(rect, color='black'))

    // Xóa luôn metadata/annotation có thể chứa nội dung tương tự
    removeMatchingAnnotations(page, redactRects)

    return page
```

**Test bắt buộc:** sau redact, chạy `ExtractText()` lại trên vùng đã redact — phải trả về rỗng hoặc không khớp nội dung gốc.

---

## 14. Pricing & Monetization

### 14.1 Tier Structure

| Tier | Giá | Đối tượng |
|---|---|---|
| **Core (MIT)** | $0 | Dự án OSS, evaluation, hobby, học thuật |
| **Pro Self-host** | $499 one-time | Indie dev, agency (per developer license) |
| **Pro Team** | $1,499 one-time | Team dev nhỏ (tới 10 developer) |
| **Cloud Starter** | $49/tháng | 500 trang xử lý/tháng (OCR/AI extract) |
| **Cloud Team** | $199/tháng | 5,000 trang/tháng |
| **Cloud Business** | $599/tháng | 25,000 trang/tháng, priority support |
| **Enterprise Self-host** | $8k–40k/năm | On-prem, SLA, tùy biến — định giá thấp hơn Nutrient 40-60% |

### 14.2 Feature Matrix

| Tính năng | Core | Pro | Cloud | Ent |
|---|---|---|---|---|
| Render/view PDF | ✓ | ✓ | ✓ | ✓ |
| Annotation (highlight/note/draw/shape) | ✓ | ✓ | ✓ | ✓ |
| Merge/split/rotate | ✓ | ✓ | ✓ | ✓ |
| Text extraction | ✓ | ✓ | ✓ | ✓ |
| WASM client-side build | ✓ | ✓ | ✓ | ✓ |
| React/Vue wrapper | ✓ | ✓ | ✓ | ✓ |
| Form fill | – | ✓ | ✓ | ✓ |
| Digital signature | – | ✓ | ✓ | ✓ |
| Redaction (an toàn, flatten thật) | – | ✓ | ✓ | ✓ |
| OCR (Tesseract) | – | ✓ | ✓ | ✓ |
| Document compare | – | ✓ | ✓ | ✓ |
| Watermark | – | ✓ | ✓ | ✓ |
| Export PDF/A | – | ✓ | ✓ | ✓ |
| AI structured extraction | – | – | ✓ | ✓ |
| AI document Q&A | – | – | ✓ | ✓ |
| Auto-redact PII bằng AI | – | – | ✓ | ✓ |
| Multi-user collaboration | – | – | ✓ | ✓ |
| Webhook/Zapier | – | – | ✓ | ✓ |
| SSO (SAML, OIDC) | – | – | – | ✓ |
| On-premise deployment | – | – | – | ✓ |
| DPA, SOC2, HIPAA BAA | – | – | – | ✓ |
| SLA 99.9% uptime | – | – | – | ✓ |

### 14.3 Vì Sao Pricing Có Thể Cao Hơn FluxGantt

Khác với thư viện Gantt (đối thủ dhtmlx chỉ $599-1,599/năm), category PDF SDK đã có benchmark giá rất cao từ Nutrient ($25k-220k+/năm). Điều này cho phép FluxDocs định giá Pro/Enterprise cao hơn nhiều so với FluxGantt mà vẫn là "rẻ hơn đối thủ 40-90%" — biên lợi nhuận trên mỗi khách hàng cao hơn đáng kể.

### 14.4 Vì Sao Cloud Tính Theo Volume Trang

Khác với FluxGantt Cloud (tính theo seat/user), FluxDocs Cloud tính theo **số trang xử lý/tháng** vì:

- Chi phí thật (OCR, AI extraction qua LLM) tỷ lệ trực tiếp với số trang, không phải số người dùng
- Khách hàng dễ ước tính chi phí dựa trên volume tài liệu thực tế của họ
- Tránh tình trạng 1 user xử lý hàng chục nghìn trang nhưng chỉ trả tiền "1 seat"

---

## 15. Distribution & Launch Strategy

### 15.1 Pre-Launch (Tuần 7–8)

- Landing page tại fluxdocs.dev với demo tương tác: "Upload PDF, redact PII ngay trong browser — không gửi server"
- Benchmark page: so sánh tốc độ render/MB binary với pdf.js, MuPDF, Nutrient (nếu có thể test)
- 3 demo GIF: annotation trực tiếp / redact an toàn / AI document Q&A
- GitHub repo public, README có badge "100% Go core, MIT license"

### 15.2 Launch Day (Tuần 8)

- **Show HN:** *"Show HN: FluxDocs — MIT-licensed PDF SDK in Go, with WASM build for client-side processing"* — góc nhìn "core Go thật" đặc biệt hấp dẫn với audience HN quan tâm performance/Go
- **Product Hunt:** demo trực quan annotation + redact
- **Reddit:**
  - r/golang (rất quan trọng — đây là audience chính cho góc "Go PDF SDK")
  - r/webdev
  - r/programming
  - r/privacy (góc "xử lý PDF nhạy cảm hoàn toàn client-side")
- **Dev.to / Hashnode:** *"Why we wrote a PDF engine in Go (and compiled it to WASM)"* — bài kỹ thuật sâu về parser, content stream, lý do chọn Go
- **Email outreach** tới startup làm contract management, e-signature, legal tech: "MIT alternative tới Nutrient, core Go, self-host được"

### 15.3 Post-Launch (Ongoing)

**SEO content:**
- "FluxDocs vs Nutrient (PSPDFKit)" — nhắm trực tiếp pain point giá
- "FluxDocs vs pdf.js + pdf-lib" — nhắm người đang ghép nhiều lib
- "How to redact PDF safely in Go" — tutorial kỹ thuật, rank tốt vì ít content chất lượng về chủ đề này
- "PDF rendering in WASM: lessons from building FluxDocs" — content kỹ thuật sâu, thu hút Go/Rust audience

**Conference talks:** GopherCon, FOSDEM (track Go hoặc track document processing)

**Open source goodwill:** Đóng góp ngược cho pdfcpu/MuPDF community nếu phát hiện bug khi build, tăng uy tín kỹ thuật.

---

## 16. 18-Week Execution Plan

| Tuần | Phase | Deliverable | Metric chính |
|---|---|---|---|
| 1 | Build | Go module setup, parser cơ bản, xref resolver | Repo public, CI green cả native + WASM |
| 2 | Build | Render raster PNG, CLI tool cơ bản | Render đúng 95% test PDF mẫu |
| 3 | Build | Font decoder, render path vector, WASM build | WASM chạy được trong browser demo |
| 4 | Build | Goroutine pool multi-page, benchmark performance | Benchmark công bố được (ms/page) |
| 5 | Build | Annotation engine (4 loại cơ bản) | Annotation render đúng trên cả native/WASM |
| 6 | Build | React + Vue wrapper, sample app | npm publish alpha |
| 7 | Polish | Document ops (merge/split/rotate), text extraction | Docs site live |
| 8 | **LAUNCH** | Show HN + Product Hunt + Reddit r/golang | 500+ GH stars, 1k+ npm/go get download |
| 9 | Listen | Bug fix, review PR, engagement community | Triage 80% issue |
| 10 | Listen | Iterate theo feedback | DX polish, mở rộng example |
| 11 | Pre-order | Email blast Pro early bird $349 | 30–50 pre-order |
| 12 | Build Pro | Form fill + AcroForm parser | Fill đúng 20 file form mẫu |
| 13 | Build Pro | Digital signature (PKCS#7) | Sign + verify thành công |
| 14 | Build Pro | Redaction engine an toàn (flatten thật) | Test extract-after-redact pass 100% |
| 15 | Build Pro | OCR integration (Tesseract CGO) | OCR accuracy benchmark công bố |
| 16 | Build Pro | Document compare, watermark, PDF/A export | Export pass validator chuẩn PDF/A |
| 17 | Polish | Pro docs, license key system | Hệ thống license hoạt động |
| 18 | **LAUNCH Pro** | Pro tier live | 50+ Pro license = $15k+ revenue |

---

## 17. Validation Milestones

### 17.1 Hard Gates (Go/No-Go Decisions)

**Sau Tuần 8 (Free MVP Launch):**

| Metric | Target | Nếu dưới target |
|---|---|---|
| GitHub stars (30 ngày) | 500+ | Audit lại distribution, đặc biệt r/golang |
| go get + npm download | 1,000+ | DX cần cải thiện, kiểm tra docs onboard |
| Email waitlist signup | 200+ | Bỏ qua Pro launch |
| Benchmark được share lại bởi bên thứ 3 | 5+ lần | Performance claim chưa đủ thuyết phục |

**Action matrix:** giữ nguyên cấu trúc như FluxGantt — 4/4 pass thì tiếp tục Wave 2; thấp hơn thì giảm scope hoặc trì hoãn theo bước tương tự.

**Sau Tuần 18 (Pro Tier Launch):**

| Metric | Target | Nếu dưới target |
|---|---|---|
| Pro license bán được | 50+ | Reposition, nhấn mạnh hơn benchmark giá so với Nutrient |
| Redaction test pass rate (an toàn) | 100% | Không launch Pro nếu chưa đạt — đây là tính năng có rủi ro pháp lý nếu sai |
| Tỷ lệ refund | <5% | Audit chất lượng rendering edge case |

**Sau Tháng 6 (Quyết định Cloud Tier):**

Tín hiệu để tiến hành Cloud:
- 100+ Pro customer
- 15+ inquiry về AI extraction/OCR hosted (cao hơn threshold FluxGantt vì đây là tính năng có giá trị rõ ràng hơn với category PDF)
- Ít nhất 2 inquiry Enterprise self-host

---

## 18. Risk Assessment & Mitigation

### 18.1 Technical Risks

**Risk:** PDF parser tự viết không đủ robust với file PDF "lỗi" (rất phổ biến trong thực tế — nhiều PDF generator vi phạm spec)
**Mitigation:** Xây test corpus lớn từ PDF thực tế (không chỉ PDF "sạch"). Áp dụng chiến lược "lenient parsing" giống pdf.js — cố gắng render được nhiều nhất có thể dù file không hoàn toàn đúng spec, kèm fallback brute-force scan.

**Risk:** Font rendering không chính xác (đặc biệt font nhúng, CJK, ligature phức tạp)
**Mitigation:** Ưu tiên hỗ trợ tốt TrueType/OpenType (phổ biến nhất). Với case khó, cho phép build tag dùng MuPDF qua CGO như fallback "high-fidelity mode" — chấp nhận trade-off license cho ai cần độ chính xác tối đa.

**Risk:** WASM bundle size lớn ảnh hưởng tốc độ load trang web
**Mitigation:** Tối ưu build (TinyGo cho phần không cần full Go runtime nếu khả thi), lazy-load WASM chỉ khi viewer thực sự mount, benchmark bundle size là KPI theo dõi liên tục.

**Risk:** Redaction có lỗi an toàn (nội dung vẫn extract được sau khi redact)
**Mitigation:** Test suite bắt buộc chạy `ExtractText` sau mọi redact test case trước khi merge code. Đây là risk nghiêm trọng nhất về reputation — một lần lỗi redact bị phát hiện công khai (như nhiều case thực tế đã xảy ra với chính phủ/tổ chức) sẽ phá hủy uy tín sản phẩm.

### 18.2 Market Risks

**Risk:** Nutrient/Apryse hạ giá hoặc ra gói entry-level rẻ hơn để chặn cạnh tranh
**Mitigation:** Giữ lợi thế MIT + Go + WASM — đây là khác biệt kiến trúc, không chỉ giá, khó copy nhanh.

**Risk:** Google Document AI / AWS Textract đè bẹp phần AI extraction bằng economies of scale
**Mitigation:** Định vị FluxDocs không cạnh tranh trực tiếp về AI extraction thuần, mà bán trải nghiệm tích hợp (viewer + annotation + extraction trong 1 SDK, không cần ghép nhiều dịch vụ).

**Risk:** Đối thủ OSS khác (ví dụ fork pdfcpu thêm UI) xuất hiện
**Mitigation:** Tốc độ ship Wave 2/3, đặc biệt AI features mà OSS thuần khó duy trì do chi phí LLM API.

### 18.3 Execution Risks

**Risk:** Viết PDF parser từ đầu tốn thời gian hơn ước tính nhiều (đây là rủi ro lớn nhất của roadmap — PDF spec rất phức tạp, dày hơn 750 trang)
**Mitigation:** Scope Wave 1 chỉ cần hỗ trợ tốt subset phổ biến nhất (90% PDF thực tế sinh ra từ ~10 tool: Word, LaTeX, Chrome print-to-PDF, Adobe). Không cần hỗ trợ 100% spec ngày đầu — tăng dần coverage theo feedback thực tế, tương tự cách pdf.js đã làm.

**Risk:** Solo developer burnout — core PDF parser đòi hỏi độ tập trung kỹ thuật cao hơn FluxGantt
**Mitigation:** Cân nhắc dùng MuPDF qua CGO cho phần render khó nhất ngay từ Wave 1 nếu tốc độ Wave 1 chậm hơn dự kiến, rồi thay dần bằng pure-Go sau — tránh rủi ro trễ launch vì cố pure-Go 100% ngay từ đầu.

### 18.4 Legal Risks

**Risk:** Vô tình implement thuật toán/kỹ thuật có patent (ít khả năng với PDF vì spec đã chuẩn hóa lâu, nhưng OCR/AI có thể có vùng xám)
**Mitigation:** Clean-room implementation dựa trên spec công khai ISO 32000, không tham khảo source code của Nutrient/Apryse.

**Risk:** Dùng MuPDF (AGPL) như fallback CGO có thể buộc toàn bộ binary phải AGPL nếu không tách đúng
**Mitigation:** Tách phần dùng MuPDF thành build tag riêng, document rõ ràng trong README rằng build với tag `mupdf` chịu license AGPL, build mặc định (pure Go) giữ MIT. Tư vấn luật sở hữu trí tuệ trước khi ship tính năng này.

**Risk:** Redaction lỗi gây hậu quả pháp lý cho khách hàng dùng FluxDocs (lộ thông tin nhạy cảm)
**Mitigation:** Disclaimer rõ trong docs Pro tier, kèm test suite công khai chứng minh độ an toàn. Cân nhắc audit bảo mật độc lập trước khi quảng bá tính năng redaction mạnh.

---

## 19. Appendix A: Sample Document JSON Schema

```json
{
  "fluxdocs": {
    "version": "1.0.0",
    "exported_at": "2026-06-20T10:00:00Z"
  },
  "document": {
    "id": "doc-01ARZ3NDEKTSV4RRFFQ69G5FAV",
    "title": "Hợp đồng dịch vụ - Công ty ABC",
    "pageCount": 12,
    "metadata": {
      "author": "Legal Team",
      "producer": "Microsoft Word",
      "keywords": ["contract", "service-agreement"]
    },
    "encrypted": false,
    "createdAt": "2026-06-01T08:00:00Z",
    "updatedAt": "2026-06-15T14:30:00Z"
  },
  "annotationLayer": {
    "id": "layer-01ARZ3NDEKTSV4RRFFQ69G5FAW",
    "name": "Review của phòng pháp lý",
    "annotations": [
      {
        "id": "ann-01ARZ3NDEKTSV4RRFFQ69G5FAX",
        "pageId": "page-3",
        "type": "highlight",
        "rect": { "x": 72, "y": 480, "width": 320, "height": 14 },
        "color": "#fde047",
        "opacity": 0.4,
        "content": "Điều khoản cần xác nhận lại với khách hàng",
        "authorId": "user-long",
        "createdAt": "2026-06-10T09:15:00Z"
      },
      {
        "id": "ann-01ARZ3NDEKTSV4RRFFQ69G5FAY",
        "pageId": "page-5",
        "type": "redact",
        "rect": { "x": 120, "y": 200, "width": 180, "height": 20 },
        "content": "Số tài khoản ngân hàng",
        "authorId": "user-long",
        "createdAt": "2026-06-10T09:20:00Z"
      }
    ]
  }
}
```

**Ví dụ processing job (Cloud tier):**

```json
{
  "id": "job-01ARZ3NDEKTSV4RRFFQ69G5FAZ",
  "documentId": "doc-01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "jobType": "ai_extract",
  "status": "done",
  "input": {
    "schema": {
      "contractParty": "string",
      "effectiveDate": "date",
      "totalValue": "number",
      "currency": "string"
    }
  },
  "result": {
    "contractParty": "Công ty TNHH ABC",
    "effectiveDate": "2026-07-01",
    "totalValue": 450000000,
    "currency": "VND"
  },
  "startedAt": "2026-06-15T10:00:00Z",
  "completedAt": "2026-06-15T10:00:08Z"
}
```

---

## 20. Appendix B: PDF Rendering Pipeline Pseudocode

Outline đầy đủ cho reference implementation của pipeline render, từ mở file tới ra raster image.

```
function openAndRenderPage(filePath string, pageIndex int, opts RenderOptions) []byte {

    // Step 1: Đọc file, parse header + tìm xref table
    file = readFile(filePath)
    if not file.startsWith("%PDF-"):
        throw InvalidPDFError

    xrefOffset = findStartXref(file)  // đọc từ cuối file, tìm "startxref"
    xrefTable = parseXrefChain(file, xrefOffset)

    // Step 2: Resolve trailer, tìm root object (Catalog)
    trailer = parseTrailer(file, xrefTable)
    catalog = resolveObject(file, xrefTable, trailer.Root)

    // Step 3: Resolve page tree, lấy đúng trang cần
    pageTree = resolveObject(file, xrefTable, catalog.Pages)
    page = walkPageTree(pageTree, pageIndex)  // page tree là cấu trúc cây, có thể lồng nhiều cấp

    // Step 4: Decode content stream của trang (có thể nén Flate/LZW)
    rawContent = resolveObject(file, xrefTable, page.Contents)
    contentStream = decodeStream(rawContent)  // giải nén theo /Filter

    // Step 5: Setup canvas theo kích thước trang + DPI yêu cầu
    width = page.MediaBox.width * (opts.DPI / 72.0)
    height = page.MediaBox.height * (opts.DPI / 72.0)
    canvas = newCanvas(width, height)

    // Step 6: Resolve resource dictionary (font, image, color space dùng trong trang)
    resources = resolveObject(file, xrefTable, page.Resources)

    // Step 7: Tokenize và thực thi content stream operator
    tokens = tokenize(contentStream)
    graphicsState = GraphicsState.default(scale = opts.DPI / 72.0)
    executeOperators(tokens, graphicsState, resources, canvas)

    // Step 8: Encode canvas ra format yêu cầu
    switch opts.Format:
        case "png":  return encodePNG(canvas)
        case "jpeg": return encodeJPEG(canvas, opts.Quality)
        case "svg":  return encodeSVG(canvas)  // dùng pipeline vector riêng, không rasterize
}

function walkPageTree(node, targetIndex, currentIndex = {value: 0}) Page {
    if node.Type == "Page":
        if currentIndex.value == targetIndex:
            return node
        currentIndex.value += 1
        return null

    // node.Type == "Pages" (intermediate node), có Kids là array con
    for child in node.Kids:
        resolvedChild = resolveObject(child)
        result = walkPageTree(resolvedChild, targetIndex, currentIndex)
        if result != null:
            return result

    return null
}
```

**Lưu ý hiệu năng:** `walkPageTree` chạy O(n) theo số trang nếu duyệt tuần tự mỗi lần gọi `RenderPage`. Với document nhiều trang, FluxDocs cache flat page array sau lần resolve đầu tiên (`Document.pages []Page` đã resolve sẵn khi `OpenDocument()`), tránh duyệt lại cây mỗi lần render.

---

## 21. Appendix C: Competitor Comparison Matrix

| Tính năng | FluxDocs | Nutrient (PSPDFKit) | Apryse | ComPDF | pdf.js + pdf-lib |
|---|---|---|---|---|---|
| License | MIT | Comm. | Comm. | Comm. (free tier hạn chế) | Apache/MIT |
| Giá entry-level | $0 (Core) / $499 (Pro) | $25k+/năm | Tương đương Nutrient | Rẻ hơn Nutrient, vẫn đắt hơn FluxDocs | $0 |
| Core language | Go | C++ | C++ | C++ | JavaScript |
| Server-side processing | ✓ | ✓ | ✓ | ✓ | ✗ (pdf-lib hạn chế) |
| WASM client-side build chính thức | ✓ | ~ (có nhưng nặng) | ~ | ✗ | ✓ (pdf.js vốn là JS) |
| Annotation engine đầy đủ | ✓ | ✓ | ✓ | ✓ | ✗ |
| Form fill | ✓** | ✓ | ✓ | ✓ | ~ (pdf-lib cơ bản) |
| Digital signature | ✓** | ✓ | ✓ | ✓ | ✗ |
| Redaction an toàn (flatten thật) | ✓** | ✓ | ✓ | ~ | ✗ |
| OCR tích hợp | ✓** | ✓ | ✓ | ✓ | ✗ |
| AI document Q&A | ✓*** | ✓ (add-on riêng, đắt) | ~ | ✗ | ✗ |
| AI structured extraction | ✓*** | ✓ (XtractFlow, đắt) | ~ | ✗ | ✗ |
| Pricing minh bạch (không sales-gated) | ✓ | ✗ | ✗ | ~ | ✓ |
| Self-host được core miễn phí | ✓ | ✗ | ✗ | ~ | ✓ |
| Goroutine-native parallel render | ✓ | N/A (C++ threads) | N/A | N/A | ✗ (JS single-thread) |
| Maintained tích cực | ✓ | ✓ | ✓ | ✓ | ✓ (pdf.js rất tốt) |

**Chú thích:**
`✓` = Có · `✓**` = Có, tầng Pro · `✓***` = Có, tầng Cloud · `✗` = Không · `~` = Một phần / hạn chế

---

## Kết

Đây là bản spec living document. So với FluxGantt, FluxDocs có ceiling giá cao hơn rõ rệt nhờ benchmark thị trường đã được Nutrient/Apryse xác lập, và Go đóng vai trò kiến trúc cốt lõi thật — không phải lựa chọn ngôn ngữ mang tính hình thức. Rủi ro lớn nhất nằm ở độ phức tạp của việc tự viết PDF parser; cần theo dõi sát tiến độ Wave 1 và sẵn sàng fallback CGO/MuPDF nếu cần để không trễ launch.

**Liên hệ:**

| | |
|---|---|
| GitHub | github.com/fluxtoolkit/fluxdocs |
| Email | hello@fluxdocs.dev |
| Twitter | @fluxdocs |
