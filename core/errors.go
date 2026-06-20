// Package core là engine chính của FluxDocs: parse, render, annotate và
// xử lý tài liệu PDF. Toàn bộ phần này là pure-Go, MIT-licensed — các tính
// năng phụ thuộc CGO (OCR, high-fidelity render) nằm sau build tag riêng.
package core

import "errors"

// Sentinel error cho các trường hợp phổ biến. Người dùng kiểm tra bằng
// errors.Is, ví dụ:
//
//	img, err := doc.RenderPage(0, opts)
//	if errors.Is(err, core.ErrPageNotFound) {
//	    // ...
//	}
//
// Quy ước: mọi message đều có prefix "fluxdocs:" để dễ nhận diện trong log
// khi error bị bọc (fmt.Errorf("...: %w", err)).
var (
	// ErrInvalidPDF — file không phải PDF hợp lệ hoặc cấu trúc hỏng không
	// thể phục hồi kể cả sau khi fallback brute-force scan.
	ErrInvalidPDF = errors.New("fluxdocs: invalid PDF structure")

	// ErrEncryptedDocument — tài liệu được mã hóa và chưa cung cấp mật khẩu /
	// khóa giải mã hợp lệ.
	ErrEncryptedDocument = errors.New("fluxdocs: document is encrypted")

	// ErrPageNotFound — index trang nằm ngoài khoảng [0, PageCount).
	ErrPageNotFound = errors.New("fluxdocs: page not found")

	// --- Bổ sung ngoài §10.2: các case mà public API §7 cần phân biệt ---

	// ErrAnnotationNotFound — không có annotation nào khớp AnnotationID.
	ErrAnnotationNotFound = errors.New("fluxdocs: annotation not found")

	// ErrFormFieldNotFound — không có form field nào khớp tên đã cho.
	ErrFormFieldNotFound = errors.New("fluxdocs: form field not found")

	// ErrUnsupportedFormat — RenderOptions.Format không nằm trong tập hỗ trợ
	// ("png" | "jpeg" | "svg").
	ErrUnsupportedFormat = errors.New("fluxdocs: unsupported render format")

	// ErrReadOnly — thao tác chỉnh sửa bị từ chối vì document/viewer ở chế độ
	// chỉ đọc.
	ErrReadOnly = errors.New("fluxdocs: document is read-only")

	// ErrFeatureRequiresPro — tính năng Pro (form, signature, redaction, OCR,
	// compare, watermark) được gọi nhưng license/build không cho phép.
	ErrFeatureRequiresPro = errors.New("fluxdocs: feature requires a Pro license")

	// ErrFeatureRequiresCloud — tính năng Cloud (AI extraction, document Q&A)
	// được gọi mà không có cấu hình Cloud backend.
	ErrFeatureRequiresCloud = errors.New("fluxdocs: feature requires the Cloud tier")
)
