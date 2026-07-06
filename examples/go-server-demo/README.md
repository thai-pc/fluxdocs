# go-server-demo

A tiny HTTP service embedding the FluxDocs core — the same library that runs in
the browser via WASM also runs server-side, no CGO.

```bash
go run ./examples/go-server-demo      # listens on :8080

curl --data-binary @in.pdf localhost:8080/extract
curl --data-binary @in.pdf "localhost:8080/thumbnail?page=0&dpi=150" -o page.png
curl --data-binary @in.pdf "localhost:8080/redact?pattern=SECRET" -o out.pdf
```

Handlers are separated from `main` (`NewServer()`), so they are unit-tested with
`httptest`.
