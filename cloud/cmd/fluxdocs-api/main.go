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
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	_ "github.com/lib/pq" // registers the "postgres" database/sql driver

	"github.com/thai-pc/fluxdocs/cloud/ai"
	"github.com/thai-pc/fluxdocs/cloud/api"
	"github.com/thai-pc/fluxdocs/cloud/db"
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

	// Pick persistence: PostgreSQL when DATABASE_URL is set, else in-memory.
	docs, keys, shares, err := stores(os.Getenv("DATABASE_URL"), api.HashKey(key), log)
	if err != nil {
		return err
	}

	// Enable the AI endpoints only when a Claude key is present.
	var aiClient core.AIClient
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		aiClient = ai.New("")
		log.Info("AI endpoints enabled")
	}

	srv := api.New(docs, keys, shares, aiClient, log)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Info("listening", "addr", addr)
	return http.ListenAndServe(addr, srv.Handler())
}

// stores returns the document and key stores. With a DATABASE_URL it applies the
// schema, seeds the default org and the given API-key hash, and returns
// PostgreSQL-backed stores; otherwise it returns in-memory stores. Only the key
// hash is ever persisted.
func stores(dsn, keyHash string, log *slog.Logger) (api.DocumentStore, api.KeyStore, api.ShareStore, error) {
	if dsn == "" {
		log.Info("persistence", "backend", "in-memory")
		keys := api.NewMemKeyStore()
		keys.Add(keyHash, "default")
		return api.NewMemDocumentStore(), keys, api.NewMemShareStore(), nil
	}

	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open database: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, nil, nil, fmt.Errorf("connect to database: %w", err)
	}
	if _, err := sqlDB.Exec(db.Schema); err != nil {
		return nil, nil, nil, fmt.Errorf("apply schema: %w", err)
	}
	if _, err := sqlDB.Exec(
		`INSERT INTO orgs (id, name) VALUES ('default', 'Default') ON CONFLICT (id) DO NOTHING`); err != nil {
		return nil, nil, nil, fmt.Errorf("seed org: %w", err)
	}
	if _, err := sqlDB.Exec(
		`INSERT INTO api_keys (key_hash, org_id) VALUES ($1, 'default') ON CONFLICT (key_hash) DO NOTHING`,
		keyHash); err != nil {
		return nil, nil, nil, fmt.Errorf("seed api key: %w", err)
	}
	log.Info("persistence", "backend", "postgres")
	return api.NewPostgresDocumentStore(sqlDB), api.NewPostgresKeyStore(sqlDB), api.NewPostgresShareStore(sqlDB), nil
}
