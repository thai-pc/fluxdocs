-- FluxDocs Cloud schema (PostgreSQL 16).
--
-- Multi-tenant isolation is structural: every tenant-owned row carries org_id,
-- and every query filters by it (.claude/SECURITY.md §4). API keys are stored
-- only as SHA-256 hashes — the plaintext key never touches the database.

CREATE TABLE IF NOT EXISTS orgs (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- api_keys stores the hash of each key (never the key itself), mapping it to the
-- owning org. Look-ups are by hash.
CREATE TABLE IF NOT EXISTS api_keys (
    key_hash   TEXT PRIMARY KEY,
    org_id     TEXT NOT NULL REFERENCES orgs (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS api_keys_org_id_idx ON api_keys (org_id);

-- documents holds the stored PDF bytes, keyed by (org_id, id). The org is part
-- of the primary key so a read with the wrong org can never return another
-- tenant's bytes.
CREATE TABLE IF NOT EXISTS documents (
    org_id     TEXT NOT NULL REFERENCES orgs (id),
    id         TEXT NOT NULL,
    data       BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, id)
);
