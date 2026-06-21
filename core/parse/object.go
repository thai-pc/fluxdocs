// Package parse hiện thực lớp parse PDF theo ISO 32000-1:2008: object model,
// lexer, xref, page tree, content stream. Clean-room — chỉ dựa trên spec công
// khai (xem .claude/SECURITY.md §6).
//
// File này định nghĩa object model: 8 loại object cơ bản của PDF (§7.3 spec
// ISO) cộng Reference (indirect object) và Stream.
package parse

import "fmt"

// Object là một giá trị trong object model của PDF. Tập kín: Null, Boolean,
// Integer, Real, String, Name, Array, Dict, Reference, Stream.
type Object interface {
	// String trả biểu diễn debug, không phải serialize PDF.
	String() string

	isObject()
}

// Null là đối tượng null của PDF (từ khóa `null`).
type Null struct{}

// Boolean là `true` / `false`.
type Boolean bool

// Integer là số nguyên PDF (lưu int64 để chứa offset lớn).
type Integer int64

// Real là số thực PDF.
type Real float64

// String là chuỗi PDF đã decode về byte thô. Hex ghi lại cú pháp gốc
// (literal `(...)` hay hex `<...>`) để re-encode trung thực khi cần.
type String struct {
	Value []byte
	Hex   bool
}

// Name là tên PDF, lưu KHÔNG kèm dấu `/` mở đầu và đã decode chuỗi thoát `#xx`.
type Name string

// Array là mảng PDF; phần tử có thể là bất kỳ Object nào, kể cả Reference.
type Array []Object

// Dict là từ điển PDF. Khóa luôn là Name (theo spec). Dùng cho cả dictionary
// thường lẫn phần dictionary của một Stream.
type Dict map[Name]Object

// Reference là tham chiếu gián tiếp `n g R` tới một indirect object.
type Reference struct {
	Number     int
	Generation int
}

// Stream là một dictionary kèm dữ liệu thô (chưa giải nén theo /Filter). Việc
// decode (Flate/LZW/…) thuộc về bước render/extract, không phải lexer.
type Stream struct {
	Dict Dict
	Raw  []byte
}

func (Null) isObject()      {}
func (Boolean) isObject()   {}
func (Integer) isObject()   {}
func (Real) isObject()      {}
func (String) isObject()    {}
func (Name) isObject()      {}
func (Array) isObject()     {}
func (Dict) isObject()      {}
func (Reference) isObject() {}
func (*Stream) isObject()   {}

func (Null) String() string      { return "null" }
func (b Boolean) String() string { return fmt.Sprintf("%t", bool(b)) }
func (i Integer) String() string { return fmt.Sprintf("%d", int64(i)) }
func (r Real) String() string    { return fmt.Sprintf("%g", float64(r)) }
func (s String) String() string  { return fmt.Sprintf("(%s)", s.Value) }
func (n Name) String() string    { return "/" + string(n) }

func (a Array) String() string {
	out := "["
	for i, o := range a {
		if i > 0 {
			out += " "
		}
		out += o.String()
	}
	return out + "]"
}

func (d Dict) String() string {
	out := "<<"
	for k, v := range d {
		out += " " + k.String() + " " + v.String()
	}
	return out + " >>"
}

func (r Reference) String() string { return fmt.Sprintf("%d %d R", r.Number, r.Generation) }

func (s *Stream) String() string {
	return fmt.Sprintf("%s stream(%d bytes)", s.Dict.String(), len(s.Raw))
}

// --- Helper truy cập có kiểm tra kiểu, tránh type-assert lặp ở caller ---

// GetName trả giá trị Name tại key, ok=false nếu thiếu hoặc sai kiểu.
func (d Dict) GetName(key Name) (Name, bool) {
	n, ok := d[key].(Name)
	return n, ok
}

// GetInt trả giá trị Integer tại key.
func (d Dict) GetInt(key Name) (Integer, bool) {
	i, ok := d[key].(Integer)
	return i, ok
}

// GetDict trả Dict con tại key.
func (d Dict) GetDict(key Name) (Dict, bool) {
	sub, ok := d[key].(Dict)
	return sub, ok
}

// GetArray trả Array tại key.
func (d Dict) GetArray(key Name) (Array, bool) {
	a, ok := d[key].(Array)
	return a, ok
}

// GetReference trả Reference tại key.
func (d Dict) GetReference(key Name) (Reference, bool) {
	r, ok := d[key].(Reference)
	return r, ok
}
