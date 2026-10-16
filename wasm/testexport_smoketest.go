//go:build js && wasm && smoketest

package main

import (
	"errors"
	"syscall/js"
)

// registerTestExports adds test-only exports, built only with -tags smoketest
// (wasm/smoke_test.mjs). _testPanic panics with a value that holds text the
// envelope must not echo, so the smoke test can check export()'s recover path
// (code "internal", the panic value shown by type only) without depending on a
// real bug.
func registerTestExports(api js.Value) {
	api.Set("_testPanic", export("_testPanic", func(js.Value, []js.Value) any {
		panic(errors.New("SECRET document bytes"))
	}))
}
