# AGENTS.md — FluxDocs

> Hướng dẫn vận hành cho AI coding agent (Claude Code, Cursor, Copilot Agent, Aider…) làm việc trong repo FluxDocs.
> Đọc cùng `.claude/CLAUDE.md` (project rules) và `.claude/SECURITY.md` (ràng buộc bảo mật).
> Đây là repo greenfield (pre-build): nhiều thư mục còn placeholder `.gitkeep` — bạn sẽ là người tạo file thật.

---

## TL;DR cho agent

- **Ngôn ngữ chính:** Go 1.23+. Core phải pure-Go, MIT-safe.
- **Hai build target:** native + WASM. Đừng làm vỡ build WASM.
- **Tách license theo tier:** Core (MIT) ⊄ Pro/Cloud. CGO chỉ sau build tag.
- **Redaction & parser an toàn = ưu tiên tuyệt đối.** Xem SECURITY.md trước khi đụng.
- **Annotation tách rời PDF gốc** (JSON layer), không bake trừ khi `Flatten*`.

## Cách tổ chức công việc

1. **Xác định tier** của thay đổi (Core / Pro / Cloud) → quyết định đặt code ở package nào (xem bảng §2 trong CLAUDE.md).
2. **Lập kế hoạch nhỏ, commit theo từng package.** Đừng trộn parse + render + cloud trong một thay đổi.
3. **Viết test cùng lúc với code** (table-driven cho parser, golden-file cho render).
4. **Chạy gate cục bộ** (xem dưới) trước khi báo hoàn thành.

## Build & test commands (dùng / tạo khi có `go.work`)

```bash
# Build native
go build ./...

# Build WASM (PHẢI green — đây là khác biệt cốt lõi của sản phẩm)
GOOS=js GOARCH=wasm go build -o /dev/null ./wasm

# Test toàn bộ (race detector bật cho code concurrency)
go test -race ./...

# Test kèm build tag Pro (CGO)
go test -tags ocr ./core/ocr/...

# Security gate: redaction leak test — PHẢI pass 100% mới được merge
go test ./testing/security/redaction/...

# Lint / vet
go vet ./...
gofmt -l .          # không được có output (mọi file đã format)

# Benchmark
go test -bench=. ./testing/benchmark/...
```

> JS wrappers: `pnpm install && pnpm -r build && pnpm -r test` trong `packages/`.

## Definition of Done (checklist agent tự verify trước khi báo xong)

- [ ] `go build ./...` xanh
- [ ] `GOOS=js GOARCH=wasm go build ./wasm` xanh (nếu đụng core/render/annotation)
- [ ] `go test -race ./...` xanh
- [ ] `gofmt -l .` không có output; `go vet ./...` sạch
- [ ] Nếu đụng redaction → `testing/security/redaction/` pass 100%
- [ ] Không có code Core import package Pro/Cloud hoặc package CGO (trừ sau build tag)
- [ ] Không thêm network call gửi nội dung PDF ra ngoài ở đường WASM/client
- [ ] Test mới đi kèm; golden file (nếu đổi render) được regenerate có chủ đích và review

## Ranh giới — agent KHÔNG được tự ý làm

- ❌ Bypass / nới lỏng / skip test redaction để cho "pass".
- ❌ Thêm dependency CGO vào đường build mặc định (phá vỡ MIT).
- ❌ Copy code từ Nutrient/Apryse/MuPDF source (clean-room — chỉ tham khảo ISO 32000 công khai).
- ❌ Đẩy nội dung PDF / PII lên service ngoài trong chế độ client-side privacy.
- ❌ Commit secret, key thật, hay file PDF chứa dữ liệu nhạy cảm thật vào `testing/corpus/`.
- ❌ Thêm `import` chéo tier làm Core kéo theo Pro/Cloud.
- ❌ Tự ý `git push` / publish npm / tạo release khi chưa được yêu cầu rõ.

## Khi nào dừng lại và hỏi người dùng

- Cần đánh đổi license (vd: dùng MuPDF CGO để render đúng hơn) → hỏi trước.
- Hành vi PDF mơ hồ mà ISO 32000 không quyết được → nêu phương án, hỏi.
- Thay đổi public API (`core/fluxdocs.go`, type trong `core/types.go`) → xác nhận vì ảnh hưởng consumer.
- Quyết định schema DB Cloud (`cloud/db/schema.sql`) → ảnh hưởng migration, xác nhận.

## Style nhanh

- Go: theo `gofmt` + Effective Go. Sentinel error + `errors.Is`. Không `Get` prefix thừa.
- Comment & docs: brand voice trực tiếp, kỹ thuật, có benchmark cụ thể. Tránh marketing sáo rỗng.
- CSS: BEM, prefix `fd-` / `--fd-*`.
- Commit message: ngắn, mô tả thay đổi theo package, vd `core/parse: handle compressed xref streams`.

## Tham chiếu

- Spec đầy đủ: `/Users/thai-pc/Downloads/fluxdocs-spec.md`
- Project rules: `.claude/CLAUDE.md`
- Security constraints: `.claude/SECURITY.md`
- Testing strategy: `testing/README.md`
- Thuật toán tham chiếu (xref resolution, content stream, redaction, parallel render): §13 + Appendix B của spec.
