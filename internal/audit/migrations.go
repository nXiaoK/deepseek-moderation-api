package audit

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func applyMigrations(ctx context.Context, tx *sql.Tx) error {
	files, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS audit_schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,checksum TEXT NOT NULL,applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT version,name,checksum FROM audit_schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	type applied struct{ name, checksum string }
	versions := map[int]applied{}
	for rows.Next() {
		var version int
		var item applied
		if err := rows.Scan(&version, &item.name, &item.checksum); err != nil {
			rows.Close()
			return err
		}
		if version < 1 || version > len(files) {
			rows.Close()
			return fmt.Errorf("database migration version %d is not supported by this binary", version)
		}
		if version != len(versions)+1 {
			rows.Close()
			return fmt.Errorf("database migration history has a gap at version %d", version)
		}
		versions[version] = item
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for index, file := range files {
		prefix, _, ok := strings.Cut(file.Name(), "_")
		version, parseErr := strconv.Atoi(prefix)
		if file.IsDir() || !ok || parseErr != nil || version != index+1 {
			return fmt.Errorf("invalid migration sequence: %s", file.Name())
		}
		raw, err := migrationFiles.ReadFile("migrations/" + file.Name())
		if err != nil {
			return err
		}
		script := strings.ReplaceAll(string(raw), "\r\n", "\n")
		checksum := digest(script)
		if previous, ok := versions[version]; ok {
			if previous.name != file.Name() || previous.checksum != checksum {
				return fmt.Errorf("migration %d checksum or name changed; add a new migration instead", version)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, script); err != nil {
			return fmt.Errorf("apply migration %s: %w", file.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO audit_schema_migrations(version,name,checksum) VALUES($1,$2,$3)", version, file.Name(), checksum); err != nil {
			return err
		}
	}
	return nil
}
