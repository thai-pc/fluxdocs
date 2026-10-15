package core

import "runtime/debug"

// guard turns a recoverable panic escaping a public entry point into an
// *InternalError stored in *err, so a hostile document fails that one call
// instead of crashing the process (SECURITY.md §2). Natively nothing else
// catches it, and a panic in a RenderAllPages worker goroutine would take the
// whole process down. Fatal runtime errors (stack overflow, out of memory,
// concurrent map writes) are not panics and cannot be recovered here; their
// inputs must be bounded (e.g. parser nesting depth).
// Defer it directly as the first statement — recover only works in the
// deferred call itself — with err the named error result and the other results
// left unnamed (`_`), so they keep their zero value on a panic:
//
//	func (d *Document) RenderPage(index int, opts RenderOptions) (_ []byte, err error) {
//	    defer guard("RenderPage", index, &err)
//
// page is the 0-based page index, or -1 when the operation is not page-scoped.
//
// This is the last line of defense, not a substitute for bounding the input:
// the fuzz targets in testing/security/fuzz fail on any ErrInternal, so a
// recovered panic is still treated as a bug.
func guard(op string, page int, err *error) {
	if r := recover(); r != nil {
		*err = &InternalError{Op: op, Page: page, Value: r, Stack: debug.Stack()}
	}
}

// testHookPanic, when non-nil, is called by injectTestPanic at the start of the
// guarded work of OpenBytes, RenderPage, each RenderAllPages worker,
// ExtractTextByPage, and RedactText (after argument checks), so
// tests can force a panic there without leaving a real bug in the code. It is
// nil outside tests and must only be set while no call is in flight.
var testHookPanic func(op string, page int)

// injectTestPanic calls testHookPanic if a test installed one.
func injectTestPanic(op string, page int) {
	if testHookPanic != nil {
		testHookPanic(op, page)
	}
}
