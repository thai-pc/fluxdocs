// ai-extraction-demo is its own module because it imports the cloud module
// (cloud/ai); keeping it out of the root module keeps `GOWORK=off go build ./...`
// at the repo root free of any Cloud dependency. The replace directives build it
// against this checkout, with or without go.work.
module github.com/thai-pc/fluxdocs/examples/ai-extraction-demo

go 1.23

require (
	github.com/thai-pc/fluxdocs v0.2.8
	github.com/thai-pc/fluxdocs/cloud v0.0.0-00010101000000-000000000000
)

require (
	golang.org/x/image v0.18.0 // indirect
	golang.org/x/text v0.21.0 // indirect
)

replace (
	github.com/thai-pc/fluxdocs => ../..
	github.com/thai-pc/fluxdocs/cloud => ../../cloud
)
