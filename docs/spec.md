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

FluxDocs is an SDK for processing and annotating PDFs, with a core engine written
in **Go** and an MIT license, targeting the gap between two extremes: ultra-expensive
enterprise SDKs (Nutrient/PSPDFKit — self-hosted entry-level licenses at
$25,000–40,000/year, averaging $76,000/year, up to $220,000+ for large
deployments) and scattered, feature-thin open-source options (pdf.js renders
only, pdf-lib does basic editing only, none of them have an integrated
annotation engine + server processing + AI extraction).

The product belongs to the Flux family (alongside FluxFiles and FluxGantt),
inheriting its entire design system, brand voice, and the proven three-tier
monetization model.

**Why Go, and what role Go plays:**

PDF processing is a byte-level, CPU-bound problem that doesn't need complex
asynchronous I/O — squarely in Go's wheelhouse. FluxDocs uses a **"real Go core"**
architecture: all the heavy lifting (parse PDF structure, render pages to
raster/SVG, compose the annotation layer, merge/split, redact, trigger OCR,
digital signing) runs in a Go server or a Go CLI binary. The client-side viewer
UI is only a thin layer (Canvas/WASM) displaying what Go returns — Go is not a
"supporting actor" as in many projects that bolt Go onto UI-heavy use cases.

Compiling to WASM lets the same Go core run on both the server (self-hosted
Docker) and the browser (client-side rendering, no need to send sensitive PDFs to
a server) — this is the key technical lever to compete with Nutrient (which uses
a C++ core and has no lightweight WASM option).

**Three monetization tiers:**

- **Core (MIT, free):** render PDFs, basic annotation (highlight, note, draw),
  text extraction, merge/split
- **Pro (one-time):** form fill, digital signature, redaction, OCR, document
  compare, watermark removal
- **Cloud (subscription):** AI document Q&A, structured data auto-extraction,
  hosted processing API, collaboration

The primary audience is **developers** embedding PDF viewing/annotation into
their products — contract-management SaaS, legal tooling, internal document
management systems, self-built e-signature platforms.

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

FluxDocs is an SDK for processing and annotating PDFs, with a Go core and an MIT
license, targeting the gap between expensive enterprise SDKs (Nutrient/PSPDFKit,
ComPDF, Apryse) and scattered, feature-thin open-source libraries (pdf.js,
pdf-lib, pdfcpu used in isolation).

The product belongs to the Flux family, alongside FluxFiles and FluxGantt. A
shared brand lowers marketing cost and inherits the developer-experience
reputation built by the earlier products.

Three monetization tiers:

- **Core (MIT, free):** render, basic annotation, text extraction, merge/split
- **Pro (one-time):** form fill, e-signature, redaction, OCR, document compare
- **Cloud (subscription):** AI document Q&A, structured data extraction, hosted
  API, collaboration

The core technical differentiator: a real Go core (not a thin binding over CGO),
compilable to WASM to run fully client-side — solving the privacy problem
(sensitive PDFs like contracts, medical records, finance need not be sent to a
server) that cloud-first SDKs struggle with.

---

## 2. Market Analysis

### 2.1 Competitor Landscape

**Commercial / Closed Source:**

| Product | License | Pricing | Stack | Strengths | Weaknesses |
|---|---|---|---|---|---|
| **Nutrient (PSPDFKit)** | Commercial | $25k–40k/yr entry-level self-host; avg $76k/yr; up to $220k+ enterprise | C++ core, many-language bindings | Most feature-complete on the market, good support, covers Web/mobile/desktop/server | Very expensive, sales-gated pricing (must contact sales for a quote), overkill for small teams |
| **Apryse (formerly PDFTron)** | Commercial | Comparable to Nutrient, sales-gated | C++ core | Mature, high rendering accuracy | Same pricing problem, steep learning curve |
| **ComPDF** | Commercial (limited free tier) | Cheaper than Nutrient but still per-component | C++ core | More competitive pricing than Nutrient | Smaller ecosystem, fewer enterprise case studies |

**Open Source:**

| Product | License | Stack | Status | Weaknesses |
|---|---|---|---|---|
| **pdf.js** | Apache 2.0 | JavaScript | Well maintained (Mozilla) | Render/view only, no full annotation engine, no server-side processing |
| **pdf-lib** | MIT | JavaScript | Maintained | Basic PDF create/edit, no render UI, no OCR, no AI |
| **pdfcpu** | Apache 2.0 | Go | Maintained, but CLI-focused | Strong at merge/split/watermark/encrypt but has no rendering engine or annotation UI, not aimed at embedding into a product |
| **MuPDF** | AGPL / Commercial dual | C | Very mature, high performance | The AGPL license is heavily restrictive for closed-source; you must buy a commercial license for commercial use — exactly the gap FluxDocs fills |

### 2.2 Market Gap

A clear gap exists for an MIT-licensed SDK that provides:

- A real Go core — not JS-only (lacking server processing) nor closed C++
  (lacking customizability and code audit)
- A complete annotation engine: highlight, note, draw, shape, stamp, measurement
  — not just rendering
- Server-side processing: merge, split, redact, watermark, OCR, form-fill,
  e-signature in a single SDK
- An official WASM build — running 100% client-side when privacy demands it (no
  file sent to a server)
- AI-powered from day one: document Q&A, structured extraction, pattern-based
  auto-redaction (PII, card numbers, etc.) — features Nutrient sells separately
  via the XtractFlow add-on
- Transparent, non-sales-gated pricing — self-serve license keys like the Stripe
  Checkout model

### 2.3 Customer Profile

**Primary Customer:**
- Solo/small-team developers building document-processing SaaS: contract
  management, self-built e-signature, legal/medical/finance records systems
- Pain: Nutrient is too expensive for early-stage ($25k+/yr just to start),
  pdf.js/pdf-lib aren't feature-complete and require stitching 3-4 different libs
- Spend: $299–799 one-time per developer for a Pro license

**Secondary Customer:**
- Agencies building document-management tools for clients (legal, real estate,
  insurance)
- Pain: client projects aren't large enough to buy a Nutrient site license;
  stitching multiple OSS libs takes 2-3 months and still lacks an annotation UI
- Spend: $999–1,999 team license, one-time

**Tertiary Customer (Cloud tier):**
- Small teams needing to batch-process PDFs (extract data, redact PII, OCR)
  without self-hosting infra
- Pain: building an OCR + AI extraction pipeline is time-consuming; existing
  cloud APIs (Google Document AI, AWS Textract) are expensive per page and hard
  to customize the UI for
- Spend: $49–199/month by processing volume

### 2.4 Total Addressable Market (TAM) Estimate

**Estimated from Nutrient/Apryse pricing signals:**

- Nutrient has a publicly visible customer base spanning Fortune 500 to startups,
  averaging $76k/yr for signed customers
- The global document SDK/processing market (including OCR, embedded
  e-signature, viewer SDKs): estimated at hundreds of millions USD/year,
  fragmented across many vendors

**Feasible share for FluxDocs:**

- Year 1: 30–60 Pro licenses × $499 average = $15–30k
- Year 2: 150–300 Pro + 30 Cloud subs × $99 average = $90–180k ARR
- Year 3: 500+ Pro + 150 Cloud + 5-10 Enterprise self-host deals ($5-15k/deal) =
  $300–600k ARR

Compared with the Gantt chart product, the price ceiling is clearly higher
thanks to the established market benchmark (Nutrient proves enterprise customers
will pay $25k-200k+/year for exactly this product category).

---

## 3. Product Positioning & Branding

### 3.1 Brand Identity

| | |
|---|---|
| **Product Name** | FluxDocs |
| **Brand Family** | Flux (modern web tooling) |
| **Family Members** | FluxFiles (file manager, shipping)<br>FluxGantt (Gantt chart)<br>FluxDocs (PDF viewer/annotator SDK, this product)<br>FluxBoard (Kanban, future)<br>FluxFlow (workflow editor, future) |

### 3.2 Tagline & Positioning

> **Tagline:** "View. Annotate. Extract. All in Go, all in MIT."

> **Positioning:** "The first MIT-licensed PDF SDK with a real Go core and AI
> document extraction — running on both server and client via WASM."

> **Elevator Pitch:** "Every document product needs a PDF viewer and annotation,
> and every developer building one faces the same choice: pay $25,000+/year for
> Nutrient, stitch together 4 incomplete JS libraries, or touch MuPDF and get
> stuck with the AGPL license. FluxDocs is the missing option — a fast Go core,
> MIT-licensed, compiled to WASM to process fully client-side when privacy is
> needed, with AI extraction built in."

### 3.3 Brand Voice

| | |
|---|---|
| **Tone** | Direct, technical, confident but not arrogant |
| **Reference** | The docs voice of Caddy (a Go project famous for clean docs), Tiptap, Drizzle ORM |
| **Avoid** | Hollow marketing: "revolutionary", "enterprise-grade" repeated meaninglessly |
| **Prefer** | Concrete performance benchmarks (ms/page render, MB/s throughput), direct price comparison with Nutrient |

### 3.4 Visual Identity

| | |
|---|---|
| **Primary Color** | Indigo `#6366f1` — inherited from FluxGantt, consistent brand family |
| **Annotation Color** | Amber `#f59e0b` — default for highlight/note, distinct from the system indigo |
| **Background** | Near-black `#0a0a0a` (dark mode), off-white `#fafafa` (light mode) |
| **Typography** | Inter (UI), JetBrains Mono (code samples) |
| **Logo Concept** | A stylized sheet of paper with rounded corners and an annotation line running across like a highlight |

### 3.5 Domain & Online Presence

| | |
|---|---|
| **Primary domain** | fluxdocs.dev |
| **Secondary** | fluxdocs.com (redirects to .dev) |
| **NPM scope** | `@fluxdocs` (for JS/WASM wrappers) |
| **Go module** | `github.com/fluxtoolkit/fluxdocs` |
| **GitHub** | github.com/fluxtoolkit/fluxdocs |
| **Twitter/X** | @fluxdocs |
| **Discord** | Flux Toolkit community server (shared with FluxFiles, FluxGantt) |

---

## 4. Technology Stack

### 4.1 Core Engine

| Layer | Choice |
|---|---|
| **Language** | Go 1.23+ |
| **Module format** | Standard Go module; plus a WASM build target (`GOOS=js GOARCH=wasm`) |
| **Architecture** | Headless core (parse + compute + render-to-buffer) decoupled from any UI |
| **PDF parsing** | A hand-written parser following the PDF 32000-1:2008 spec for the core (object model, xref, stream); may call CGO into MuPDF for high-quality rendering in an optional "high-fidelity" mode (separate build tag, not required) |
| **Rendering** | Raster (PNG/JPEG buffer) for server-side thumbnail/preview; SVG for vector-accurate client-side; the WASM build uses the browser Canvas API to paint |
| **Concurrency** | A goroutine pool processing multi-page rendering in parallel — a natural Go advantage over single-threaded JS (except Workers) |
| **OCR** | Tesseract binding via CGO (Pro tier); may be swapped for a cloud OCR API (Cloud tier) |
| **AI integration** | Call an LLM API (Claude, via a standard Go HTTP client) for document Q&A and structured extraction — no other-language SDK needed |
| **Testing** | `go test` + table-driven tests for the parser; golden-file tests for rendering (compare PNG output with a reference) |
| **Monorepo** | Go workspace (`go.work`) for multi-module, combined with a pnpm workspace for the JS wrappers/demos |

### 4.2 Client Wrappers

**Wave 1:**
- `@fluxdocs/react` — React 18+, a `<FluxDocsViewer />` component that loads the
  WASM core
- `@fluxdocs/vue` — Vue 3+, an equivalent Composition API

**Wave 2:**
- `@fluxdocs/web-components` — pure custom elements, usable in any framework or
  vanilla HTML
- A native Go package used directly in a Go backend (no wrapper needed — an
  advantage over FluxGantt since the core is already Go)

**Community-driven:**
- `@fluxdocs/svelte`
- A Python binding via cgo-export (for data/ML teams batch-processing PDFs)

### 4.3 Cloud Backend (Wave 3)

| | |
|---|---|
| **Runtime** | A Go binary running directly (no separate Node.js runtime for the backend) |
| **Framework** | Chi (a lightweight idiomatic Go HTTP router) or Echo |
| **Database** | PostgreSQL 16 |
| **ORM** | sqlc (generate type-safe Go code from SQL, keeping Go's "little magic" philosophy) or Drizzle if an auxiliary Node service is needed |
| **Object storage** | Cloudflare R2 (stores the original PDF + the annotation-layer JSON) |
| **Batch queue** | Go channels + an internal worker pool for small jobs; River (a Postgres-backed, Go-native queue) for large jobs needing persistence |
| **Auth** | Better-Auth or hand-written in Go (JWT + OAuth, few dependencies) |
| **CDN** | Cloudflare (free tier) |
| **Hosting** | Fly.io — especially good for Go thanks to light binaries and fast cold starts |
| **Email** | Resend (transactional) |
| **Payments** | Stripe (Pro one-time + Cloud subscription) |
| **Analytics** | Plausible (privacy-first) |

### 4.4 Documentation Site

| | |
|---|---|
| **Framework** | Vocs (inherited from FluxGantt) or Hugo (a Go-native static site, fitting the "all Go" branding) |
| **Hosting** | Cloudflare Pages |
| **Code examples** | Go Playground embeds for server-side snippets; StackBlitz for client wrappers |

---

## 5. System Architecture

### 5.1 Layered Architecture

```
┌───────────────────────────────────────────────────────────┐
│  User Application Layer                                   │
│  (React, Vue, vanilla JS, or a Go backend directly)       │
└───────────────────────────────────────────────────────────┘
                              │
                ┌─────────────┴─────────────┐
                ▼                           ▼
┌───────────────────────────┐   ┌───────────────────────────┐
│  Client Wrapper Layer     │   │  Go Native Usage          │
│  @fluxdocs/react/vue      │   │  import "fluxdocs/core"   │
│  - Load WASM core         │   │  - Used directly in a Go  │
│  - Canvas paint binding   │   │    backend, no wrapper or │
│  - Event binding for      │   │    network hop needed     │
│    annotation tools       │   │                           │
└───────────────────────────┘   └───────────────────────────┘
                │                           │
                └─────────────┬─────────────┘
                              ▼
┌───────────────────────────────────────────────────────────┐
│  Core Engine (fluxdocs/core) — written 100% in Go         │
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
│  │  - Goroutine pool for multi-page parallel render        ││
│  └───────────────────────────────────────────────────────┘│
│  ┌───────────────────────────────────────────────────────┐│
│  │  Annotation Layer                                     ││
│  │  - AnnotationStore (reactive, like FluxGantt's store)  ││
│  │  - Highlight, note, draw, shape, stamp                 ││
│  │  - Serialize annotations independent of the source PDF ││
│  │    (JSON layer)                                        ││
│  └───────────────────────────────────────────────────────┘│
│  ┌───────────────────────────────────────────────────────┐│
│  │  Document Ops Layer                                   ││
│  │  - Merge / split / rotate / reorder pages               ││
│  │  - Redact (permanently remove content, not just cover)  ││
│  │  - Watermark                                            ││
│  │  - Form fill (AcroForm + basic XFA)                     ││
│  │  - Digital signature (PKCS#7)                           ││
│  └───────────────────────────────────────────────────────┘│
│  ┌───────────────────────────────────────────────────────┐│
│  │  Extraction Layer                                     ││
│  │  - Layout-aware text extraction (reading order)         ││
│  │  - Table detection                                     ││
│  │  - OCR adapter (Tesseract CGO or cloud)                 ││
│  │  - AI structured extraction (Cloud, calls an LLM)       ││
│  └───────────────────────────────────────────────────────┘│
│  ┌───────────────────────────────────────────────────────┐│
│  │  Sync Layer (Cloud only)                               ││
│  │  - Multi-user annotation sync (CRDT-lite)               ││
│  │  - Comment thread per annotation                        ││
│  │  - Document version history                             ││
│  └───────────────────────────────────────────────────────┘│
└───────────────────────────────────────────────────────────┘
```

### 5.2 Design Principles

1. **A real Go core, not fake Go** — Unlike many projects that bolt Go onto a
   UI-heavy use case (where Go only does relay/orchestration), here the heaviest
   work — parse + render PDF — runs entirely in Go. This is the core technical
   differentiator versus "writing Go for show".

2. **One core, two build targets** — The same Go codebase compiles to a native
   binary (server, CLI) and to WASM (browser). No need to maintain two separate
   implementations as many other SDKs do (JS for client, another language for
   server).

3. **Annotations separate from the original PDF** — Annotations are stored as a
   separate JSON layer, applied onto the PDF at render time or on "flatten"
   export. This lets multiple users annotate without modifying the source file,
   eases undo, and eases syncing.

4. **Goroutine-native for batch processing** — Rendering many pages, OCR-ing many
   files, extracting many documents — all use a Go worker pool, no external
   thread pool or complex async runtime.

5. **Privacy-first via WASM** — For sensitive documents (contracts, medical
   records), the whole pipeline can run in the browser, sending not a single byte
   to a server. This is a use case cloud-first SDKs (Nutrient Cloud) don't serve
   well.

6. **Type safety via Go structs, no separate TypeScript needed** — Because the
   core is Go, server-side consumers (other Go backends) use Go types directly,
   without a JSON ↔ type transform layer as when the core is in another language.

7. **Clear license, separating core/CGO** — The pure-Go core (parse, basic
   render, annotation, document ops) stays 100% MIT. Optional CGO-using parts
   (Tesseract OCR, high-fidelity render via MuPDF) are packaged behind their own
   build tags so users decide whether to accept that dependent license.

---

## 6. Core Type System

### 6.1 Core Types (Go)

```go
// Package core is the main engine; directory core/, import path
// github.com/fluxtoolkit/fluxdocs/core, used as core.OpenDocument (§7.1).
package core

import "time"

// DocumentID, PageID, AnnotationID use controlled type aliases to avoid mixing
// up ID kinds (the equivalent of branded types in TS)
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
	Color     string  // hex, e.g. "#f59e0b"
	Opacity   float64 // 0..1
	Content   string  // text if it is a note/stamp
	Points    []Point // used for draw (freehand)
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
	Options  []string // for dropdown/radio
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
	DPI         int    // default 150
	Format      string // "png" | "jpeg" | "svg"
	PageRange   []int  // empty = all pages
	Quality     int    // 1-100, applies to jpeg
}

type ExtractOptions struct {
	PreserveLayout bool
	IncludeTables  bool
	OCRFallback    bool // if the page is a scanned image, fall back to OCR
}
```

### 6.2 Annotation Layer (stored independently of the PDF)

```go
type AnnotationLayer struct {
	ID         LayerID
	DocumentID DocumentID
	Name       string // e.g. "Long's review"
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

### 7.1 Opening a document (Go native)

```go
package main

import "github.com/fluxtoolkit/fluxdocs/core"

func main() {
	doc, err := core.OpenDocument("contract.pdf")
	if err != nil {
		panic(err)
	}
	defer doc.Close()

	// Render the first page to PNG at 150 DPI
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
doc.FlattenAnnotations() (*Document, error) // bake annotations into the PDF, no longer editable
doc.ExportAnnotationLayer() (*AnnotationLayer, error)
doc.ImportAnnotationLayer(layer AnnotationLayer) error
```

### 7.4 Redaction (Pro)

```go
doc.RedactText(pageIndex int, pattern string) ([]Rect, error)       // by regex, e.g. an ID number
doc.RedactArea(pageIndex int, rect Rect) error                       // by a specified region
doc.RedactAndFlatten() (*Document, error)                            // permanently removed, not recoverable
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
doc.OCRPage(pageIndex int) (string, error)                          // Pro, needs the Tesseract build tag
doc.ExtractStructured(schema any) (map[string]any, error)            // Cloud, calls an LLM
doc.AskDocument(question string) (string, error)                     // Cloud, document Q&A
```

### 7.7 Events (via callbacks, since Go has no native event loop)

```go
doc.OnAnnotationChange(func(a Annotation))
doc.OnPageRendered(func(pageIndex int, img []byte))
doc.OnFormFieldChange(func(f FormField))
```

### 7.8 React Wrapper Example (WASM core via the client)

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

### 7.9 HTTP API (Cloud tier, for non-Go integrations)

```
POST   /v1/documents                  upload a PDF, returns documentId
GET    /v1/documents/:id/pages/:n     render a page as PNG/SVG
POST   /v1/documents/:id/annotations  add an annotation
GET    /v1/documents/:id/annotations  get the full annotation layer
POST   /v1/documents/:id/extract      extract text/tables
POST   /v1/documents/:id/ask          AI document Q&A
POST   /v1/documents/:id/redact       redact by pattern/area
```

---

## 8. UI/UX Design System

### 8.1 Visual Philosophy

FluxDocs is a professional business tool whose users typically work in sensitive
environments (legal, medical, finance). The aesthetic must convey "trustworthy,
precise" rather than "creative". Actively avoid:

- Overly garish annotation colors that distract from the document content
- Fussy page-transition animations
- Abstract, hard-to-read icons for annotation tools (always use clear icons:
  highlighter pen, note, rectangle)

Prefer:

- The document canvas as the center; minimize the toolbar when not needed
- High density for the sidebar (annotation list, outline, page thumbnails)
- Clear state: viewing vs annotating mode, saved or not

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
  --fd-bg-canvas:        #e5e7eb;   /* the area around the PDF page, for contrast */
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
  --fd-redact-fill:      #18181b;   /* solid black, non-transparent */

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
│ ├───────┤ │   │      (PDF page content)       │     │ │ "Clause3"│ │
│ │ Pg 2  │ │   │                              │     │ ├─────────┤ │
│ │ Pg 3  │ │   │  ▓▓▓▓ highlight               │     │ │ Note     │ │
│ └───────┘ │   │                              │     │ │ "Needs   │ │
│           │   └──────────────────────────────┘     │ │  review" │ │
│           │                                        │ └─────────┘ │
└───────────┴────────────────────────────────────────┴─────────────┘
```

### 8.4 Interaction Patterns

**Annotation tools:**

| Action | Result |
|---|---|
| Pick the Highlight tool, drag over text | Create a highlight following the selected text lines |
| Pick the Note tool, click on the page | Create a note pin, open a popup to enter content |
| Pick the Draw tool, drag the mouse | Draw freehand, stored as a sequence of Points |
| Pick the Redact tool, drag a region | Mark a redact region (not yet removed, preview only until flatten) |
| Double-click an annotation | Open the edit popup for content/color |
| Right-click an annotation | Context menu: Delete, Change color, Reply |

**Keyboard:**

| Key | Action |
|---|---|
| Arrow Up/Down | Change page |
| Cmd/Ctrl + F | Open search within the document |
| Cmd/Ctrl + +/- | Zoom in/out |
| Cmd/Ctrl + S | Save (flatten annotations if needed) |
| Escape | Exit annotate mode, back to view mode |
| Delete | Delete the selected annotation |

### 8.5 Accessibility

- WCAG 2.1 AA minimum for the toolbar and sidebar (the PDF canvas itself inherits
  accessibility from the original PDF structure; FluxDocs cannot improve a PDF
  with no accessibility tags)
- All annotation tools controllable by keyboard
- Support PDF/UA (Tagged PDF) when the source document has it, readable via a
  screen reader
- Clear focus indicators on every annotation and toolbar button
- Respect `prefers-reduced-motion` for page-change/zoom transitions

---

## 9. Feature Roadmap (3 Waves)

### 9.1 Wave 1 — Free MVP (Tier: Core MIT, Weeks 1–8)

**Goal:** Ship a fully working Go PDF viewer + annotation engine, good enough to
replace stitching pdf.js + pdf-lib, good enough to attract initial GitHub stars
and npm downloads.

**Weeks 1–2: Foundation**
- Set up the Go module + workspace, CI (GitHub Actions) building both native and
  WASM targets
- PDF parser: object model, xref table, page tree resolver
- Decode basic content streams (text, simple paths)
- Render a page to raster PNG (configurable DPI)
- Basic `fluxdocs` CLI tool: render, info, extract-text

**Weeks 3–4: Advanced rendering + WASM**
- Font decoder (TrueType, basic Type1 — enough for 90% of real PDFs)
- Render complex vector paths (Bezier curves, clipping)
- Build the WASM target, test it running in a browser via Canvas
- Goroutine pool for parallel multi-page rendering

**Week 5: Annotation Engine**
- AnnotationStore (a reactive, JSON-serializable layer)
- Highlight, note, draw, shape — the 4 basic types
- Render the annotation layer over the canvas (both native and WASM)
- Export/import the annotation layer separately from the source PDF

**Week 6: Client Wrappers**
- `@fluxdocs/react` loads WASM, a `<FluxDocsViewer />` component
- `@fluxdocs/vue` equivalent
- A sample app per framework

**Week 7: Polish & Document Ops**
- Merge / split / rotate / reorder pages
- Layout-aware text extraction (correct reading order, not just object-order dump)
- Export flattened annotations to a new PDF
- Mobile-responsive viewer UI

**Week 8: Documentation & Launch Prep**
- Documentation site (Hugo or Vocs)
- 10+ examples: Go server-side, React client-side, CLI batch processing
- Landing page with a "redact PII directly in the browser, nothing sent to a
  server" demo
- README quick start
- Comparison pages (vs Nutrient, vs stitching pdf.js+pdf-lib)
- Draft Show HN, Product Hunt assets

### 9.2 Wave 2 — Pro Tier (Weeks 11–18)

**Goal:** Add features developers will pay $299–799 one-time for — exactly the
feature set Nutrient charges separately for.

**Weeks 11–12: Forms & Signature**
- Parse AcroForm fields (text, checkbox, radio, dropdown)
- Fill forms via the API, flatten the form into static content
- Digital signature (PKCS#7), verify an existing signature in a PDF
- A signature-pad UI component for the client wrapper

**Weeks 13–14: Redaction**
- Redact by regex pattern (ID numbers, credit cards, emails)
- Redact by a manually specified region
- Flatten redaction — ensure content is permanently removed from the object
  stream, not just painted over (a common security flaw in poor redaction tools)
- A test suite verifying redacted content cannot be recovered via text extraction
  or copy-paste

**Weeks 15–16: OCR & Compare**
- Integrate Tesseract via CGO (build tag `ocr`)
- OCR fallback automatically when `ExtractText` returns empty (the page is a
  scanned image)
- Document compare: diff two PDF versions, highlight the changes
- Watermark (text/image, over all or specified pages)

**Week 17: Advanced Export & Polish**
- Export PDF/A (the long-term archival standard)
- Batch-processing CLI (process many files by glob pattern)
- A performance benchmark suite, publish numbers vs Nutrient/MuPDF

**Week 18: Pro Launch**
- License key validation
- Stripe Checkout integration
- Pro documentation, migration guide from pdf.js/pdf-lib
- Public Pro launch

### 9.3 Wave 3 — Cloud + AI Tier (Month 6+)

**Goal:** Recurring revenue via AI document processing — exactly the category
Nutrient sells separately (XtractFlow) at a high price.

**Months 6–7: Cloud Foundation**
- Backend API (Go + Chi/Echo + Postgres)
- Upload a document, store in R2, queue processing via River
- Auth + organization model
- Stripe subscription by processing volume

**Months 8–9: AI Extraction**
- `ExtractStructured()` — give a schema (e.g. "name, date, contract value"), the
  LLM returns structured JSON
- `AskDocument()` — document Q&A via an LLM, with citations to page/position in
  the PDF
- Auto-redact PII via AI detection (no hand-written regex)
- Advanced table extraction via a vision model for complex tables

**Months 10–11: Collaboration**
- Multi-user annotation sync (CRDT-lite, no need for full Yjs since annotations
  conflict less than text editing)
- Comment thread per annotation
- Share links with permissions (view/comment/edit)
- Document version history

**Month 12: Integrations**
- Webhooks (document processed, annotation added)
- Zapier connector
- DocuSign-style flow integration (request a signature via email)
- Export to Google Drive/Dropbox after processing

---

## 10. API Naming Conventions

### 10.1 Method Naming (Go idiomatic)

PascalCase for exported methods, verb first, following standard Go convention
(no "Get" prefix when returning a simple field, per Effective Go).

**Use:**
```go
doc.RenderPage(0, opts)
doc.AddAnnotation(a)
doc.ExtractText(opts)
doc.RedactArea(pageIndex, rect)
doc.Merge(other)
```

**Avoid:**
```go
doc.render_page(0, opts)        // snake_case is not Go convention
doc.GetRenderedPageAsImage(0)   // verbose, redundant "Get"
doc.DoOperation("render", 0)    // a generic action, loses type safety
```

### 10.2 Error Handling

Per Go standard: return `error` as the second value, no panic for ordinary
runtime errors (only panic for unrecoverable errors, e.g. an internal logic bug).

```go
img, err := doc.RenderPage(0, opts)
if err != nil {
    // handle the specific error, e.g. check errors.Is(err, core.ErrPageNotFound)
}
```

Define sentinel errors for common cases:

```go
var (
	ErrPageNotFound      = errors.New("fluxdocs: page not found")
	ErrEncryptedDocument = errors.New("fluxdocs: document is encrypted")
	ErrInvalidPDF        = errors.New("fluxdocs: invalid PDF structure")
)
```

### 10.3 CSS Class Naming (BEM, for the client UI)

Prefix `fd-` to avoid collisions with the host application.

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

Per Go convention: short, lowercase package names, no underscores, describing the
function accurately.

```
fluxdocs/core           # the main engine
fluxdocs/render         # render logic (raster + svg)
fluxdocs/annotation      # the annotation engine
fluxdocs/extract        # text/table extraction
fluxdocs/ocr            # OCR adapter (separate build tag)
fluxdocs/sign           # digital signature
fluxdocs/cloud          # HTTP client calling the Cloud API
```

### 10.5 NPM Package Names (for client wrappers)

| Package | Description |
|---|---|
| `@fluxdocs/core` | WASM build + JS loader |
| `@fluxdocs/react` | React wrapper (Wave 1) |
| `@fluxdocs/vue` | Vue wrapper (Wave 1) |
| `@fluxdocs/web-components` | Pure custom elements (Wave 2) |
| `@fluxdocs/cloud-sdk` | Cloud API client (Wave 3) |

---

## 11. Code Organization

### 11.1 Monorepo Structure

```
fluxdocs/
├── core/                            # the main Go module
│   ├── document.go                  # Document type + OpenDocument()
│   ├── parse/
│   │   ├── object.go                # PDF object model
│   │   ├── xref.go                   # cross-reference table
│   │   ├── pagetree.go               # resolve the page tree
│   │   ├── contentstream.go          # tokenize the content stream
│   │   └── font.go                   # font decoder
│   ├── render/
│   │   ├── raster.go                 # render PNG/JPEG
│   │   ├── svg.go                    # render SVG
│   │   ├── pool.go                   # multi-page goroutine pool
│   │   └── canvas_wasm.go            # Canvas API binding (build tag wasm)
│   ├── annotation/
│   │   ├── store.go                  # AnnotationStore
│   │   ├── highlight.go
│   │   ├── note.go
│   │   ├── draw.go
│   │   └── layer.go                  # serialize/deserialize the layer
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
├── cloud/                            # Go backend for the Cloud tier
│   ├── api/
│   │   ├── handlers/
│   │   ├── middleware/
│   │   └── router.go
│   ├── queue/
│   │   └── worker.go
│   ├── db/
│   │   ├── schema.sql
│   │   └── queries.sql               # for sqlc generate
│   └── main.go
│
├── wasm/
│   └── main.go                       # WASM build entry, exporting funcs via syscall/js
│
├── packages/                         # the JS/TS wrappers
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
├── pnpm-workspace.yaml               # for the packages/ JS side
├── README.md
├── LICENSE                           # MIT (core)
└── CONTRIBUTING.md
```

---

## 12. Database Schema (Cloud Tier)

PostgreSQL schema for the hosted Cloud edition.

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
  storage_key     TEXT NOT NULL,          -- key in R2
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
  points      JSONB,                       -- for freehand draw
  author_id   UUID REFERENCES users(id),
  meta        JSONB,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_annotations_layer ON annotations(layer_id);
CREATE INDEX idx_annotations_page ON annotations(layer_id, page_index);

-- Comments (per annotation, for Wave 3 collaboration)
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
  input         JSONB,                      -- e.g. schema for ai_extract, question for ask
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

PDF uses a cross-reference table (xref) to map object number → byte position in
the file. Resolving objects correctly — especially with incremental updates (a
PDF edited many times, having multiple xref tables) — is the foundation for
correct rendering.

**Pseudocode:**

```
function resolveObject(file, objectNumber, generation):
    xrefTable = parseXrefChain(file)  // follow the /Prev chain on incremental updates
    entry = xrefTable.lookup(objectNumber, generation)

    if entry.type == 'free':
        return nil  // object has been deleted

    if entry.type == 'in-use':
        return parseObjectAt(file, entry.offset)

    if entry.type == 'compressed':
        // object lives inside an Object Stream (PDF 1.5+)
        objStream = resolveObject(file, entry.streamObjectNumber, 0)
        return objStream.extractObject(entry.indexInStream)
```

**Edge cases to handle:**
- Corrupt/truncated file — fall back to a brute-force scan of the whole file for
  `obj`/`endobj` markers
- Multiple xref tables from incremental updates — must follow the `/Prev` order
  correctly to get the newest version
- Nested object streams (a compressed object inside a compressed object — rare,
  but allowed by the spec)

### 13.2 Content Stream Rendering Pipeline

A PDF content stream is a sequence of operators (like a small stack-based
language) describing how to draw a page — text, paths, images, clipping.

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
            case 'Do':          drawXObject(canvas, token.name, graphicsState)  // image or form
            // ... ~40 other operators per the PDF 32000-1 spec

    return canvas
```

**Go design note:** Because the content stream is linear and stateful (graphics
state changes with operator order), rendering a single page cannot be
parallelized internally — but **across pages** it is fully independent, which is
where the goroutine pool shines (see 13.3).

### 13.3 Parallel Multi-Page Rendering (a Go advantage)

```go
func (d *Document) RenderAllPages(opts RenderOptions) ([][]byte, error) {
	results := make([][]byte, d.PageCount)
	errs := make([]error, d.PageCount)

	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU()) // bound concurrency by CPU count

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

This is where Go has a natural advantage over Node.js (which needs the more
complex Worker Threads) or Python (whose GIL limits true CPU-bound parallelism).

### 13.4 Redaction — Permanently Removing Content (not just painting over)

The most common security flaw in poor redaction tools: only drawing a black
rectangle over the content, while the original text remains in the content stream
and can be extracted/copied. FluxDocs does it correctly by modifying the actual
object stream.

```
function redactAndFlatten(page, redactRects):
    contentStream = decodeStream(page.Contents)
    tokens = tokenize(contentStream)
    newTokens = []

    for token in tokens:
        if token.operator == 'Tj' or token.operator == 'TJ':
            textBounds = computeTextBounds(token, currentGraphicsState)
            if intersectsAny(textBounds, redactRects):
                continue  // drop this text-drawing operator entirely, keep nothing
        newTokens.append(token)

    page.Contents = encodeStream(reconstructStream(newTokens))

    // Draw a black cover (display only, NOT the primary security mechanism)
    for rect in redactRects:
        page.Contents.append(drawFilledRect(rect, color='black'))

    // Also remove metadata/annotations that may hold the same content
    removeMatchingAnnotations(page, redactRects)

    return page
```

**Mandatory test:** after redaction, re-run `ExtractText()` over the redacted
region — it must return empty or not match the original content.

---

## 14. Pricing & Monetization

### 14.1 Tier Structure

| Tier | Price | Audience |
|---|---|---|
| **Core (MIT)** | $0 | OSS projects, evaluation, hobby, academic |
| **Pro Self-host** | $499 one-time | Indie devs, agencies (per-developer license) |
| **Pro Team** | $1,499 one-time | Small dev teams (up to 10 developers) |
| **Cloud Starter** | $49/month | 500 processed pages/month (OCR/AI extract) |
| **Cloud Team** | $199/month | 5,000 pages/month |
| **Cloud Business** | $599/month | 25,000 pages/month, priority support |
| **Enterprise Self-host** | $8k–40k/year | On-prem, SLA, customization — priced 40-60% below Nutrient |

### 14.2 Feature Matrix

| Feature | Core | Pro | Cloud | Ent |
|---|---|---|---|---|
| Render/view PDF | ✓ | ✓ | ✓ | ✓ |
| Annotation (highlight/note/draw/shape) | ✓ | ✓ | ✓ | ✓ |
| Merge/split/rotate | ✓ | ✓ | ✓ | ✓ |
| Text extraction | ✓ | ✓ | ✓ | ✓ |
| WASM client-side build | ✓ | ✓ | ✓ | ✓ |
| React/Vue wrapper | ✓ | ✓ | ✓ | ✓ |
| Form fill | – | ✓ | ✓ | ✓ |
| Digital signature | – | ✓ | ✓ | ✓ |
| Redaction (safe, true flatten) | – | ✓ | ✓ | ✓ |
| OCR (Tesseract) | – | ✓ | ✓ | ✓ |
| Document compare | – | ✓ | ✓ | ✓ |
| Watermark | – | ✓ | ✓ | ✓ |
| Export PDF/A | – | ✓ | ✓ | ✓ |
| AI structured extraction | – | – | ✓ | ✓ |
| AI document Q&A | – | – | ✓ | ✓ |
| Auto-redact PII via AI | – | – | ✓ | ✓ |
| Multi-user collaboration | – | – | ✓ | ✓ |
| Webhook/Zapier | – | – | ✓ | ✓ |
| SSO (SAML, OIDC) | – | – | – | ✓ |
| On-premise deployment | – | – | – | ✓ |
| DPA, SOC2, HIPAA BAA | – | – | – | ✓ |
| SLA 99.9% uptime | – | – | – | ✓ |

### 14.3 Why Pricing Can Be Higher Than FluxGantt

Unlike a Gantt library (where the competitor dhtmlx is only $599-1,599/year), the
PDF SDK category has a very high price benchmark from Nutrient ($25k-220k+/year).
This lets FluxDocs price Pro/Enterprise much higher than FluxGantt while still
being "40-90% cheaper than the competition" — a significantly higher margin per
customer.

### 14.4 Why Cloud Is Priced by Page Volume

Unlike FluxGantt Cloud (priced per seat/user), FluxDocs Cloud is priced by
**pages processed per month** because:

- Real cost (OCR, AI extraction via LLM) scales directly with page count, not
  user count
- Customers can easily estimate cost from their actual document volume
- Avoids the case of one user processing tens of thousands of pages but paying
  for just "1 seat"

---

## 15. Distribution & Launch Strategy

### 15.1 Pre-Launch (Weeks 7–8)

- A landing page at fluxdocs.dev with an interactive demo: "Upload a PDF, redact
  PII right in the browser — nothing sent to a server"
- A benchmark page: compare render speed/binary size with pdf.js, MuPDF, Nutrient
  (where testable)
- 3 demo GIFs: live annotation / safe redaction / AI document Q&A
- A public GitHub repo, README with a "100% Go core, MIT license" badge

### 15.2 Launch Day (Week 8)

- **Show HN:** *"Show HN: FluxDocs — MIT-licensed PDF SDK in Go, with a WASM build
  for client-side processing"* — the "real Go core" angle is especially appealing
  to an HN audience that cares about performance/Go
- **Product Hunt:** a visual annotation + redact demo
- **Reddit:**
  - r/golang (very important — the main audience for the "Go PDF SDK" angle)
  - r/webdev
  - r/programming
  - r/privacy (the "process sensitive PDFs fully client-side" angle)
- **Dev.to / Hashnode:** *"Why we wrote a PDF engine in Go (and compiled it to
  WASM)"* — a deep technical post on the parser, content stream, and the reasons
  for choosing Go
- **Email outreach** to startups in contract management, e-signature, legal tech:
  "an MIT alternative to Nutrient, a Go core, self-hostable"

### 15.3 Post-Launch (Ongoing)

**SEO content:**
- "FluxDocs vs Nutrient (PSPDFKit)" — targeting the price pain point directly
- "FluxDocs vs pdf.js + pdf-lib" — targeting people stitching multiple libs
- "How to redact a PDF safely in Go" — a technical tutorial that ranks well since
  there is little quality content on the topic
- "PDF rendering in WASM: lessons from building FluxDocs" — deep technical
  content, attracting a Go/Rust audience

**Conference talks:** GopherCon, FOSDEM (Go track or document-processing track)

**Open-source goodwill:** contribute back to the pdfcpu/MuPDF communities if bugs
are found while building, raising technical credibility.

---

## 16. 18-Week Execution Plan

| Week | Phase | Deliverable | Key metric |
|---|---|---|---|
| 1 | Build | Go module setup, basic parser, xref resolver | Public repo, CI green for both native + WASM |
| 2 | Build | Render raster PNG, basic CLI tool | Render correctly for 95% of sample PDFs |
| 3 | Build | Font decoder, vector path render, WASM build | WASM runs in a browser demo |
| 4 | Build | Multi-page goroutine pool, performance benchmark | Publishable benchmark (ms/page) |
| 5 | Build | Annotation engine (4 basic types) | Annotations render correctly on both native/WASM |
| 6 | Build | React + Vue wrapper, sample app | npm publish alpha |
| 7 | Polish | Document ops (merge/split/rotate), text extraction | Docs site live |
| 8 | **LAUNCH** | Show HN + Product Hunt + Reddit r/golang | 500+ GH stars, 1k+ npm/go get downloads |
| 9 | Listen | Bug fixes, review PRs, community engagement | Triage 80% of issues |
| 10 | Listen | Iterate on feedback | DX polish, expand examples |
| 11 | Pre-order | Email blast Pro early bird $349 | 30–50 pre-orders |
| 12 | Build Pro | Form fill + AcroForm parser | Fill 20 sample form files correctly |
| 13 | Build Pro | Digital signature (PKCS#7) | Sign + verify successfully |
| 14 | Build Pro | Safe redaction engine (true flatten) | Extract-after-redact tests pass 100% |
| 15 | Build Pro | OCR integration (Tesseract CGO) | Publish OCR accuracy benchmark |
| 16 | Build Pro | Document compare, watermark, PDF/A export | Export passes a standard PDF/A validator |
| 17 | Polish | Pro docs, license key system | License system working |
| 18 | **LAUNCH Pro** | Pro tier live | 50+ Pro licenses = $15k+ revenue |

---

## 17. Validation Milestones

### 17.1 Hard Gates (Go/No-Go Decisions)

**After Week 8 (Free MVP Launch):**

| Metric | Target | If below target |
|---|---|---|
| GitHub stars (30 days) | 500+ | Audit distribution, especially r/golang |
| go get + npm downloads | 1,000+ | DX needs work, check onboarding docs |
| Email waitlist signups | 200+ | Skip the Pro launch |
| Benchmark re-shared by a third party | 5+ times | The performance claim isn't convincing enough |

**Action matrix:** keep the same structure as FluxGantt — 4/4 pass → continue to
Wave 2; below that → reduce scope or delay following the same steps.

**After Week 18 (Pro Tier Launch):**

| Metric | Target | If below target |
|---|---|---|
| Pro licenses sold | 50+ | Reposition, emphasize the price benchmark vs Nutrient more |
| Redaction test pass rate (safe) | 100% | Do not launch Pro until met — this feature carries legal risk if wrong |
| Refund rate | <5% | Audit rendering edge-case quality |

**After Month 6 (Cloud Tier Decision):**

Signals to proceed with Cloud:
- 100+ Pro customers
- 15+ inquiries about hosted AI extraction/OCR (a higher threshold than FluxGantt
  since this feature has clearer value for the PDF category)
- At least 2 Enterprise self-host inquiries

---

## 18. Risk Assessment & Mitigation

### 18.1 Technical Risks

**Risk:** The hand-written PDF parser isn't robust enough for "broken" PDF files
(very common in practice — many PDF generators violate the spec)
**Mitigation:** Build a large test corpus from real PDFs (not just "clean" ones).
Apply a "lenient parsing" strategy like pdf.js — try to render as much as
possible even if the file isn't fully spec-compliant, with a brute-force scan
fallback.

**Risk:** Inaccurate font rendering (especially embedded fonts, CJK, complex
ligatures)
**Mitigation:** Prioritize good TrueType/OpenType support (most common). For hard
cases, allow a build tag using MuPDF via CGO as a "high-fidelity mode" fallback —
accepting the license trade-off for those who need maximum accuracy.

**Risk:** A large WASM bundle size hurts web page load speed
**Mitigation:** Optimize the build (TinyGo for parts not needing the full Go
runtime where feasible), lazy-load the WASM only when the viewer actually mounts,
and track bundle size as a continuous KPI.

**Risk:** Redaction has a safety flaw (content still extractable after redaction)
**Mitigation:** A mandatory test suite runs `ExtractText` after every redaction
test case before merging code. This is the most serious reputation risk — a
single publicly discovered redaction flaw (as has happened to governments/orgs)
would destroy the product's credibility.

### 18.2 Market Risks

**Risk:** Nutrient/Apryse cut prices or release a cheaper entry-level tier to
block competition
**Mitigation:** Keep the MIT + Go + WASM advantage — these are architectural
differences, not just price, and hard to copy quickly.

**Risk:** Google Document AI / AWS Textract crush the AI extraction part via
economies of scale
**Mitigation:** Position FluxDocs not to compete head-on on pure AI extraction,
but to sell the integrated experience (viewer + annotation + extraction in one
SDK, no need to stitch multiple services).

**Risk:** Another OSS competitor appears (e.g. a pdfcpu fork with a UI added)
**Mitigation:** Ship speed for Wave 2/3, especially AI features that pure OSS
struggles to sustain due to LLM API cost.

### 18.3 Execution Risks

**Risk:** Writing the PDF parser from scratch takes much longer than estimated
(the biggest roadmap risk — the PDF spec is very complex, over 750 pages)
**Mitigation:** Scope Wave 1 to only support the most common subset well (90% of
real PDFs generated by ~10 tools: Word, LaTeX, Chrome print-to-PDF, Adobe). No
need to support 100% of the spec on day one — increase coverage gradually per
real feedback, the way pdf.js did.

**Risk:** Solo-developer burnout — the core PDF parser demands higher technical
focus than FluxGantt
**Mitigation:** Consider using MuPDF via CGO for the hardest rendering parts as
early as Wave 1 if Wave 1 is slower than expected, then gradually replace with
pure Go — avoiding the risk of a late launch from insisting on 100% pure Go from
the start.

### 18.4 Legal Risks

**Risk:** Accidentally implementing a patented algorithm/technique (unlikely for
PDF since the spec has been standardized for a long time, but OCR/AI may have
gray areas)
**Mitigation:** Clean-room implementation based on the public ISO 32000 spec, not
referencing Nutrient/Apryse source code.

**Risk:** Using MuPDF (AGPL) as a CGO fallback could force the entire binary to
be AGPL if not separated correctly
**Mitigation:** Separate the MuPDF-using part into its own build tag, document
clearly in the README that building with the `mupdf` tag is subject to AGPL while
the default build (pure Go) stays MIT. Consult IP counsel before shipping this
feature.

**Risk:** A redaction flaw causes legal consequences for a customer using
FluxDocs (leaking sensitive information)
**Mitigation:** A clear disclaimer in the Pro docs, plus a public test suite
proving safety. Consider an independent security audit before heavily marketing
the redaction feature.

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
    "title": "Service Agreement - Company ABC",
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
    "name": "Legal team's review",
    "annotations": [
      {
        "id": "ann-01ARZ3NDEKTSV4RRFFQ69G5FAX",
        "pageId": "page-3",
        "type": "highlight",
        "rect": { "x": 72, "y": 480, "width": 320, "height": 14 },
        "color": "#fde047",
        "opacity": 0.4,
        "content": "Clause to re-confirm with the client",
        "authorId": "user-long",
        "createdAt": "2026-06-10T09:15:00Z"
      },
      {
        "id": "ann-01ARZ3NDEKTSV4RRFFQ69G5FAY",
        "pageId": "page-5",
        "type": "redact",
        "rect": { "x": 120, "y": 200, "width": 180, "height": 20 },
        "content": "Bank account number",
        "authorId": "user-long",
        "createdAt": "2026-06-10T09:20:00Z"
      }
    ]
  }
}
```

**Example processing job (Cloud tier):**

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
    "contractParty": "ABC Co., Ltd",
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

A complete outline for a reference implementation of the render pipeline, from
opening the file to producing a raster image.

```
function openAndRenderPage(filePath string, pageIndex int, opts RenderOptions) []byte {

    // Step 1: Read the file, parse the header + find the xref table
    file = readFile(filePath)
    if not file.startsWith("%PDF-"):
        throw InvalidPDFError

    xrefOffset = findStartXref(file)  // read from the end of the file for "startxref"
    xrefTable = parseXrefChain(file, xrefOffset)

    // Step 2: Resolve the trailer, find the root object (Catalog)
    trailer = parseTrailer(file, xrefTable)
    catalog = resolveObject(file, xrefTable, trailer.Root)

    // Step 3: Resolve the page tree, get the requested page
    pageTree = resolveObject(file, xrefTable, catalog.Pages)
    page = walkPageTree(pageTree, pageIndex)  // the page tree is a tree, possibly deeply nested

    // Step 4: Decode the page's content stream (may be Flate/LZW compressed)
    rawContent = resolveObject(file, xrefTable, page.Contents)
    contentStream = decodeStream(rawContent)  // decompress per /Filter

    // Step 5: Set up the canvas per page size + requested DPI
    width = page.MediaBox.width * (opts.DPI / 72.0)
    height = page.MediaBox.height * (opts.DPI / 72.0)
    canvas = newCanvas(width, height)

    // Step 6: Resolve the resource dictionary (fonts, images, color spaces used on the page)
    resources = resolveObject(file, xrefTable, page.Resources)

    // Step 7: Tokenize and execute the content stream operators
    tokens = tokenize(contentStream)
    graphicsState = GraphicsState.default(scale = opts.DPI / 72.0)
    executeOperators(tokens, graphicsState, resources, canvas)

    // Step 8: Encode the canvas to the requested format
    switch opts.Format:
        case "png":  return encodePNG(canvas)
        case "jpeg": return encodeJPEG(canvas, opts.Quality)
        case "svg":  return encodeSVG(canvas)  // uses a separate vector pipeline, no rasterize
}

function walkPageTree(node, targetIndex, currentIndex = {value: 0}) Page {
    if node.Type == "Page":
        if currentIndex.value == targetIndex:
            return node
        currentIndex.value += 1
        return null

    // node.Type == "Pages" (intermediate node), with Kids as a child array
    for child in node.Kids:
        resolvedChild = resolveObject(child)
        result = walkPageTree(resolvedChild, targetIndex, currentIndex)
        if result != null:
            return result

    return null
}
```

**Performance note:** `walkPageTree` runs O(n) in the number of pages if it walks
sequentially on each `RenderPage` call. For a many-page document, FluxDocs caches
the flat page array after the first resolution (`Document.pages []Page` resolved
at `OpenDocument()` time), avoiding re-walking the tree on every render.

---

## 21. Appendix C: Competitor Comparison Matrix

| Feature | FluxDocs | Nutrient (PSPDFKit) | Apryse | ComPDF | pdf.js + pdf-lib |
|---|---|---|---|---|---|
| License | MIT | Comm. | Comm. | Comm. (limited free tier) | Apache/MIT |
| Entry-level price | $0 (Core) / $499 (Pro) | $25k+/yr | Comparable to Nutrient | Cheaper than Nutrient, still pricier than FluxDocs | $0 |
| Core language | Go | C++ | C++ | C++ | JavaScript |
| Server-side processing | ✓ | ✓ | ✓ | ✓ | ✗ (pdf-lib limited) |
| Official WASM client-side build | ✓ | ~ (exists but heavy) | ~ | ✗ | ✓ (pdf.js is JS natively) |
| Full annotation engine | ✓ | ✓ | ✓ | ✓ | ✗ |
| Form fill | ✓** | ✓ | ✓ | ✓ | ~ (pdf-lib basic) |
| Digital signature | ✓** | ✓ | ✓ | ✓ | ✗ |
| Safe redaction (true flatten) | ✓** | ✓ | ✓ | ~ | ✗ |
| Integrated OCR | ✓** | ✓ | ✓ | ✓ | ✗ |
| AI document Q&A | ✓*** | ✓ (separate add-on, expensive) | ~ | ✗ | ✗ |
| AI structured extraction | ✓*** | ✓ (XtractFlow, expensive) | ~ | ✗ | ✗ |
| Transparent pricing (not sales-gated) | ✓ | ✗ | ✗ | ~ | ✓ |
| Free self-hostable core | ✓ | ✗ | ✗ | ~ | ✓ |
| Goroutine-native parallel render | ✓ | N/A (C++ threads) | N/A | N/A | ✗ (JS single-thread) |
| Actively maintained | ✓ | ✓ | ✓ | ✓ | ✓ (pdf.js very good) |

**Legend:**
`✓` = Yes · `✓**` = Yes, Pro tier · `✓***` = Yes, Cloud tier · `✗` = No · `~` =
Partial / limited

---

## Closing

This is a living-document spec. Compared with FluxGantt, FluxDocs has a clearly
higher price ceiling thanks to the market benchmark established by
Nutrient/Apryse, and Go plays a genuinely core architectural role — not a
cosmetic language choice. The biggest risk lies in the complexity of writing a
PDF parser from scratch; track Wave 1 progress closely and be ready to fall back
to CGO/MuPDF if needed to avoid a late launch.

**Contact:**

| | |
|---|---|
| GitHub | github.com/fluxtoolkit/fluxdocs |
| Email | hello@fluxdocs.dev |
| Twitter | @fluxdocs |
