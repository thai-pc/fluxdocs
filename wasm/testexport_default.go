//go:build js && wasm && !smoketest

package main

import "syscall/js"

// registerTestExports adds nothing in a normal build; see
// testexport_smoketest.go.
func registerTestExports(js.Value) {}
