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

	// Wire persistence (Postgres/R2/in-memory, per env).
	docs, keys, shares, err := stores(api.HashKey(key), log)
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

	// Enable JWT bearer auth + POST /v1/token when a signing secret is set.
	if secret := os.Getenv("JWT_SECRET"); secret != "" {
		srv.UseJWT([]byte(secret))
		log.Info("JWT auth enabled")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Info("listening", "addr", addr)
	return http.ListenAndServe(addr, srv.Handler())
}

// stores wires the persistence layer. Metadata (keys, share links) lives in
// PostgreSQL when DATABASE_URL is set, else in memory. The document blob store is
// chosen independently: Cloudflare R2 when R2 is configured, else the same
// database as the metadata, else in memory. Only the key hash is ever persisted.
func stores(keyHash string, log *slog.Logger) (api.DocumentStore, api.KeyStore, api.ShareStore, error) {
	var (
		keys   api.KeyStore
		shares api.ShareStore
		sqlDB  *sql.DB
	)
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		var err error
		if sqlDB, err = openDB(dsn, keyHash); err != nil {
			return nil, nil, nil, err
		}
		keys, shares = api.NewPostgresKeyStore(sqlDB), api.NewPostgresShareStore(sqlDB)
		log.Info("metadata", "backend", "postgres")
	} else {
		mk := api.NewMemKeyStore()
		mk.Add(keyHash, "default")
		keys, shares = mk, api.NewMemShareStore()
		log.Info("metadata", "backend", "in-memory")
	}

	// Document blob store.
	if r2 := os.Getenv("R2_ENDPOINT"); r2 != "" {
		log.Info("documents", "backend", "r2")
		return api.NewR2DocumentStore(api.R2Config{
			Endpoint:  r2,
			Bucket:    os.Getenv("R2_BUCKET"),
			Region:    os.Getenv("R2_REGION"),
			AccessKey: os.Getenv("R2_ACCESS_KEY_ID"),
			SecretKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		}, nil), keys, shares, nil
	}
	if sqlDB != nil {
		log.Info("documents", "backend", "postgres")
		return api.NewPostgresDocumentStore(sqlDB), keys, shares, nil
	}
	log.Info("documents", "backend", "in-memory")
	return api.NewMemDocumentStore(), keys, shares, nil
}

// openDB opens Postgres, applies the schema, and seeds the default org + key.
func openDB(dsn, keyHash string) (*sql.DB, error) {
	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if _, err := sqlDB.Exec(db.Schema); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if _, err := sqlDB.Exec(
		`INSERT INTO orgs (id, name) VALUES ('default', 'Default') ON CONFLICT (id) DO NOTHING`); err != nil {
		return nil, fmt.Errorf("seed org: %w", err)
	}
	if _, err := sqlDB.Exec(
		`INSERT INTO api_keys (key_hash, org_id) VALUES ($1, 'default') ON CONFLICT (key_hash) DO NOTHING`,
		keyHash); err != nil {
		return nil, fmt.Errorf("seed api key: %w", err)
	}
	return sqlDB, nil
}
