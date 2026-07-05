//go:build ocr

// Package ocr wraps the Tesseract OCR engine via CGO. It is compiled ONLY under
// the `ocr` build tag, so the default FluxDocs build stays 100% pure Go with no
// CGO dependency (CLAUDE.md §2 — CGO parts sit behind a build tag). A binary
// built with `-tags ocr` links libtesseract (Apache-2.0) and libleptonica
// (BSD-style) and is subject to their licenses.
//
// Build/run requires the Tesseract and Leptonica development libraries and the
// language data (e.g. eng.traineddata). The CFLAGS/LDFLAGS below assume a
// standard /usr/local install; adjust them (or use a pkg-config wrapper) for
// other layouts.
package ocr

/*
#cgo CFLAGS: -I/usr/local/include
#cgo LDFLAGS: -L/usr/local/lib -ltesseract -lleptonica
#include <stdlib.h>
#include <tesseract/capi.h>
#include <leptonica/allheaders.h>
*/
import "C"

import (
	"errors"
	"strings"
	"unsafe"
)

// Image runs OCR on an encoded image (PNG/JPEG/TIFF bytes) and returns the
// recognized text. lang is a Tesseract language code (e.g. "eng"); datapath is
// the tessdata directory, or "" to use the Tesseract default (TESSDATA_PREFIX or
// the compiled-in path).
func Image(data []byte, lang, datapath string) (string, error) {
	if len(data) == 0 {
		return "", errors.New("ocr: empty image")
	}
	if lang == "" {
		lang = "eng"
	}

	api := C.TessBaseAPICreate()
	if api == nil {
		return "", errors.New("ocr: could not create tesseract API")
	}
	defer C.TessBaseAPIDelete(api)

	clang := C.CString(lang)
	defer C.free(unsafe.Pointer(clang))
	var cpath *C.char
	if datapath != "" {
		cpath = C.CString(datapath)
		defer C.free(unsafe.Pointer(cpath))
	}
	if C.TessBaseAPIInit3(api, cpath, clang) != 0 {
		return "", errors.New("ocr: failed to init tesseract (is " + lang + ".traineddata installed?)")
	}
	defer C.TessBaseAPIEnd(api)

	pix := C.pixReadMem((*C.l_uint8)(unsafe.Pointer(&data[0])), C.size_t(len(data)))
	if pix == nil {
		return "", errors.New("ocr: could not decode the image")
	}
	defer C.pixDestroy(&pix)

	C.TessBaseAPISetImage2(api, pix)
	if C.TessBaseAPIRecognize(api, nil) != 0 {
		return "", errors.New("ocr: recognition failed")
	}
	ctext := C.TessBaseAPIGetUTF8Text(api)
	if ctext == nil {
		return "", nil
	}
	defer C.TessDeleteText(ctext)
	return strings.TrimRight(C.GoString(ctext), "\n"), nil
}
