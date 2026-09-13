package audit

import (
	"context"
	"testing"
)

func TestMigrationsAreIdempotentAndRejectDrift(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	files, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM audit_schema_migrations").Scan(&count); err != nil || count != len(files) {
		t.Fatal(count, err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"UPDATE audit_schema_migrations SET checksum='modified' WHERE version=1",
		"INSERT INTO audit_schema_migrations(version,name,checksum) VALUES(99999,'future','future')",
	} {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, query); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := applyMigrations(ctx, tx); err == nil {
			tx.Rollback()
			t.Fatal("incompatible migration accepted")
		}
		_ = tx.Rollback()
	}
}
