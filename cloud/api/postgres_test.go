package api

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, mock
}

func TestPostgresDocumentStore_Put(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewPostgresDocumentStore(db)

	mock.ExpectExec(regexp.QuoteMeta(
		`INSERT INTO documents (org_id, id, data) VALUES ($1, $2, $3)`)).
		WithArgs("org-a", "doc1", []byte("PDF")).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := s.Put(context.Background(), "org-a", "doc1", []byte("PDF")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestPostgresDocumentStore_GetScopedByOrg(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewPostgresDocumentStore(db)

	// The query must filter by BOTH org_id and id — that WHERE clause is the
	// tenant-isolation boundary. WithArgs asserts the org is passed as $1.
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT data FROM documents WHERE org_id = $1 AND id = $2`)).
		WithArgs("org-a", "doc1").
		WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow([]byte("PDF")))

	data, err := s.Get(context.Background(), "org-a", "doc1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != "PDF" {
		t.Errorf("data = %q", data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestPostgresDocumentStore_GetNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewPostgresDocumentStore(db)

	// A wrong-org (or missing) read yields no rows -> ErrNotFound, so a cross
	// tenant request is a 404, not a leak.
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT data FROM documents WHERE org_id = $1 AND id = $2`)).
		WithArgs("org-b", "doc1").
		WillReturnError(sql.ErrNoRows)

	if _, err := s.Get(context.Background(), "org-b", "doc1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestPostgresKeyStore(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewPostgresKeyStore(db)
	q := regexp.QuoteMeta(`SELECT org_id FROM api_keys WHERE key_hash = $1`)

	mock.ExpectQuery(q).WithArgs("hash-1").
		WillReturnRows(sqlmock.NewRows([]string{"org_id"}).AddRow("org-a"))
	if org, ok := s.OrgForKeyHash(context.Background(), "hash-1"); !ok || org != "org-a" {
		t.Errorf("OrgForKeyHash = %q,%v; want org-a,true", org, ok)
	}

	mock.ExpectQuery(q).WithArgs("unknown").WillReturnError(sql.ErrNoRows)
	if org, ok := s.OrgForKeyHash(context.Background(), "unknown"); ok || org != "" {
		t.Errorf("unknown key = %q,%v; want \"\",false", org, ok)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
