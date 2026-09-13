package audit

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	container := os.Getenv("AUDIT_TEST_PG_CONTAINER")
	if container == "" {
		t.Skip("set AUDIT_TEST_PG_CONTAINER for an isolated pg_dump/pg_restore exercise")
	}
	s := testStore(t)
	dsn, err := url.Parse(os.Getenv("AUDIT_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	if dsn.Hostname() != "localhost" && dsn.Hostname() != "127.0.0.1" && dsn.Hostname() != "::1" {
		t.Fatal("restore exercise requires a local test PostgreSQL server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var schema string
	if err := s.DB.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	credential, err := s.SaveCredential(ctx, "admin", "", "restore test", "test-only-restore-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.CreateKey(ctx, "admin", "restore caller", []string{"abuse-default"}, 60)
	if err != nil {
		t.Fatal(err)
	}
	log := AuditLog{ID: "restore-log", Kind: "test", InputStored: true, ModelOutputStored: true, ModelOutput: "test-only-output", CreatedAt: time.Now()}
	if err := s.Record(ctx, log, "test-only-input", 30); err != nil {
		t.Fatal(err)
	}
	database := "audit_restore_" + strings.ToLower(digest(randomToken(""))[:16])
	if _, err := s.DB.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(database)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.DB.ExecContext(context.Background(), "DROP DATABASE "+pq.QuoteIdentifier(database)+" WITH (FORCE)"); err != nil {
			t.Errorf("temporary restore database cleanup: %v", err)
		}
	})
	user := dsn.User.Username()
	source := strings.TrimPrefix(dsn.Path, "/")
	dump := exec.CommandContext(ctx, "docker", "exec", container, "pg_dump", "-U", user, "-d", source, "-n", schema, "-Fc")
	var stderr bytes.Buffer
	dump.Stderr = &stderr
	backup, err := dump.Output()
	if err != nil {
		t.Fatalf("test-schema pg_dump: %v: %s", err, stderr.String())
	}
	restore := exec.CommandContext(ctx, "docker", "exec", "-i", container, "pg_restore", "-U", user, "-d", database, "--no-owner", "--no-privileges", "--exit-on-error")
	restore.Stdin = bytes.NewReader(backup)
	stderr.Reset()
	restore.Stderr = &stderr
	if err := restore.Run(); err != nil {
		t.Fatalf("temporary database pg_restore: %v: %s", err, stderr.String())
	}
	dsn.Path = "/" + database
	query := dsn.Query()
	query.Set("search_path", schema)
	dsn.RawQuery = query.Encode()
	restored, err := OpenStore(ctx, dsn.String(), s.Vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restored.DB.Close() })
	secret, err := restored.CredentialSecret(ctx, credential)
	if err != nil || secret != "test-only-restore-secret" {
		t.Fatal("restored credential cannot decrypt", err)
	}
	if _, err := restored.AuthenticateKey(ctx, token); err != nil {
		t.Fatal("restored access verifier failed", err)
	}
	detail, err := restored.LogDetail(ctx, log.ID)
	if err != nil || detail.Input != "test-only-input" || detail.ModelOutput != "test-only-output" {
		t.Fatal("restored audit ciphertext failed", err)
	}
}
