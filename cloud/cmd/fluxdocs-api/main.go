// Command fluxdocs-api runs the FluxDocs Cloud HTTP backend. This skeleton wires
// the in-memory stores; the production build swaps in PostgreSQL + Cloudflare R2
// behind the same api.DocumentStore / api.KeyStore interfaces.
//
// Configuration (env):
//
//	PORT               listen port (default 8080)
//	FLUXDOCS_API_KEY   an API key to accept (required); its hash is registered
//	                   for the "default" org. The plaintext is never stored.
//	ANTHROPIC_API_KEY  if set, enables the AI endpoints via the Claude backend.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/thai-pc/fluxdocs/cloud/ai"
	"github.com/thai-pc/fluxdocs/cloud/api"
	"github.com/thai-pc/fluxdocs/core"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fluxdocs-api:", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	key := os.Getenv("FLUXDOCS_API_KEY")
	if key == "" {
		return fmt.Errorf("FLUXDOCS_API_KEY is required")
	}
	keys := api.NewMemKeyStore()
	keys.Add(api.HashKey(key), "default") // only the hash is stored

	// Enable the AI endpoints only when a Claude key is present.
	var aiClient core.AIClient
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		aiClient = ai.New("")
		log.Info("AI endpoints enabled")
	}

	srv := api.New(api.NewMemDocumentStore(), keys, aiClient, log)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Info("listening", "addr", addr)
	return http.ListenAndServe(addr, srv.Handler())
}
