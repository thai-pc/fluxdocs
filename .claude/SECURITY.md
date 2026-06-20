# SECURITY.md — FluxDocs

> Ràng buộc bảo mật bắt buộc cho mọi người (và AI agent) đóng góp code.
> Bối cảnh: FluxDocs xử lý tài liệu nhạy cảm (hợp đồng, hồ sơ y tế, tài chính). Một lỗ hổng công khai = mất uy tín sản phẩm vĩnh viễn. Đây không phải checklist tùy chọn.

Tham chiếu spec: §13.4 (Redaction), §18 (Risk Assessment), §5.2 (Design Principles).

---

## 1. Redaction — rủi ro #1, ràng buộc cứng

**Lỗ hổng phổ biến nhất:** tool redact kém chỉ vẽ hình chữ nhật đen đè lên, nhưng text gốc vẫn nằm trong content stream → extract/copy được. Nhiều chính phủ & tổ chức đã lộ thông tin theo cách này.

**FluxDocs PHẢI làm đúng:**
- Khi redact, **loại bỏ hoàn toàn** operator vẽ text (`Tj`/`TJ`) giao với vùng redact khỏi content stream, rồi re-encode stream. KHÔNG chỉ vẽ đè.
- Xóa luôn metadata, annotation, alt-text, và mọi object ẩn có thể chứa nội dung tương tự trong vùng đó.
- Hình chữ nhật đen (`--fd-redact-fill: #18181b`, đặc, không trong suốt) chỉ để hiển thị — KHÔNG phải cơ chế bảo mật chính.
- `RedactAndFlatten()` là thao tác **không phục hồi được** — document rõ trong API.

**Test gate (không thương lượng):**
- Sau mọi redact, chạy lại `ExtractText()` + thử copy-paste trên vùng đã redact → phải rỗng / không khớp nội dung gốc.
- Bộ test sống ở `testing/security/redaction/`. **PR đụng redaction không merge nếu pass rate < 100%.**
- Cân nhắc audit bảo mật độc lập trước khi quảng bá mạnh tính năng redaction (§18.4).

## 2. Parser nhận input không tin cậy

Mọi file PDF là input thù địch tiềm tàng. Bắt buộc phòng thủ:

- **Decompression bomb:** giới hạn kích thước sau giải nén (`/Filter` FlateDecode/LZW). Có ngưỡng + tỉ lệ nén tối đa.
- **Xref loop:** chuỗi `/Prev` trong incremental update có thể tạo vòng lặp vô hạn → giới hạn độ sâu, phát hiện cycle.
- **Object stream lồng nhau:** giới hạn độ sâu đệ quy khi resolve compressed object.
- **Integer overflow:** khi tính `width = MediaBox.width * DPI/72` → kiểm tra biên trước khi alloc canvas (chống cấp phát khổng lồ gây DoS).
- **Brute-force fallback an toàn:** scan `obj`/`endobj` cho file corrupt nhưng vẫn trong giới hạn tài nguyên.
- **Fuzzing:** seeds trong `testing/security/fuzz/`. Chạy `go test -fuzz` cho parser/tokenizer trong CI định kỳ.

Mục tiêu: file độc hại làm hỏng 1 lần parse, KHÔNG được crash process, OOM, hay rò bộ nhớ.

## 3. Privacy-first qua WASM

- Ở chế độ client-side, pipeline (parse → render → annotate → redact → extract) chạy hoàn toàn trong browser.
- **KHÔNG được thêm bất kỳ network request nào** gửi nội dung PDF / annotation / kết quả extract ra ngoài trong đường WASM. Đây là lời hứa lõi với khách hàng privacy-sensitive.
- Telemetry (nếu có) chỉ được là metric ẩn danh, opt-in, KHÔNG kèm nội dung tài liệu.

## 4. Cloud tier (backend Go)

- **Secret & PII:** không bao giờ log nội dung tài liệu, PII, hay secret. Scrub log.
- **API keys:** lưu `key_hash` (hash, không plaintext) + `prefix` để hiển thị. Hỗ trợ `revoked_at`. Scope theo `scopes[]`.
- **Share links:** `token` đủ entropy (≥ 64 ký tự), `password_hash` (không plaintext), `expires_at` bắt buộc kiểm tra, đếm `view_count`.
- **Multi-tenant isolation:** MỌI query phải scoped theo `org_id`. Không truy vấn cross-org. Kiểm tra membership + role trước mọi thao tác.
- **Auth:** JWT + OAuth (Better-Auth hoặc tự viết). Verify chữ ký, kiểm hạn token.
- **Storage:** file gốc trong R2 với `storage_key` không đoán được; `sha256` để dedupe + integrity check.
- **Object storage access:** dùng signed URL có hạn, không public bucket.
- **Rate limiting** trên endpoint upload/extract/ask để chống abuse và bảo vệ chi phí LLM.

## 5. Digital signature & crypto (Pro)

- PKCS#7 đúng chuẩn; verify chain, kiểm hạn cert khi verify signature có sẵn.
- Dùng thư viện crypto chuẩn của Go (`crypto/*`), không tự cuộn thuật toán.
- Không nhúng private key vào client/WASM bundle.

## 6. Legal / License (xem §18.4)

- **Clean-room:** implement parser/render chỉ dựa trên **ISO 32000-1:2008 công khai**. KHÔNG đọc/copy source của Nutrient, Apryse, hay MuPDF.
- **MuPDF (AGPL):** chỉ qua build tag `mupdf`. Document rõ trong README: binary build với tag này chịu AGPL; build mặc định (pure Go) giữ MIT. Tư vấn luật SHTT trước khi ship.
- **Tesseract OCR:** qua build tag `ocr`, license phụ thuộc được nêu rõ.
- **Redaction disclaimer:** docs Pro phải có disclaimer + test suite công khai chứng minh độ an toàn (giảm rủi ro pháp lý cho khách hàng).

## 7. Dữ liệu test

- KHÔNG commit PDF chứa PII / dữ liệu nhạy cảm thật vào `testing/corpus/`. Dùng tài liệu synthetic hoặc đã ẩn danh.
- KHÔNG commit secret, API key thật, cert private key vào repo.

## Quy trình báo lỗi bảo mật

Lỗ hổng bảo mật báo riêng tư tới security@fluxdocs.dev (không mở issue công khai). Coordinated disclosure trước khi public.

---

**Nguyên tắc vàng:** nếu một thay đổi làm yếu đi bất kỳ mục nào ở trên để "cho nhanh" hoặc "cho pass test" — DỪNG lại và hỏi. Trong FluxDocs, đúng-về-bảo-mật quan trọng hơn nhanh.
