package audit

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPasswordChangeRejectsPreviouslyVerifiedLogin(t *testing.T) {
	store := testStore(t)
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var oldHash string
	if err = store.DB.QueryRowContext(ctx, "SELECT password_hash FROM admin_users WHERE username='admin'").Scan(&oldHash); err != nil {
		t.Fatal(err)
	}
	if !verifyPassword(oldHash, "test-password-123456") {
		t.Fatal("old password was not valid before the change")
	}
	r := httptest.NewRequest("PUT", "/admin/auth/password", strings.NewReader(`{"current":"test-password-123456","password":"new-password-123456789"}`))
	r = r.WithContext(context.WithValue(ctx, sessionKey{}, session{Username: "admin"}))
	if err = app.changePassword(httptest.NewRecorder(), r); err != nil {
		t.Fatal(err)
	}
	if err = app.createAdminSession(ctx, "admin", oldHash, "stale-login", "csrf"); errorCode(err) != "invalid_login" {
		t.Fatalf("stale password verification created a session: %v", err)
	}
	var count int
	if err = store.DB.QueryRowContext(ctx, "SELECT count(*) FROM admin_sessions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("old-password session survived password change: count=%d err=%v", count, err)
	}
}

func TestSessionCreationWaitsForConcurrentPasswordUpdate(t *testing.T) {
	store := testStore(t)
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var oldHash string
	if err = store.DB.QueryRowContext(ctx, "SELECT password_hash FROM admin_users WHERE username='admin'").Scan(&oldHash); err != nil {
		t.Fatal(err)
	}
	change, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer change.Rollback()
	if _, err = change.ExecContext(ctx, "UPDATE admin_users SET password_hash='replacement-hash' WHERE username='admin'"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.createAdminSession(ctx, "admin", oldHash, "racing-login", "csrf") }()
	select {
	case err := <-done:
		t.Fatalf("session creation did not wait for password row lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err = change.ExecContext(ctx, "DELETE FROM admin_sessions WHERE username='admin'"); err != nil {
		t.Fatal(err)
	}
	if err = change.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; errorCode(err) != "invalid_login" {
		t.Fatalf("concurrent password update was ignored: %v", err)
	}
}

func TestPasswordChangeRevokesSessionThatCommitsFirst(t *testing.T) {
	store := testStore(t)
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ctx := context.Background()
	var hash string
	if err = store.DB.QueryRowContext(ctx, "SELECT password_hash FROM admin_users WHERE username='admin'").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if err = app.createAdminSession(ctx, "admin", hash, "committed-login", "csrf"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PUT", "/admin/auth/password", strings.NewReader(`{"current":"test-password-123456","password":"new-password-123456789"}`))
	r = r.WithContext(context.WithValue(ctx, sessionKey{}, session{Username: "admin"}))
	if err = app.changePassword(httptest.NewRecorder(), r); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = store.DB.QueryRowContext(ctx, "SELECT count(*) FROM admin_sessions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("committed session was not revoked: count=%d err=%v", count, err)
	}
}
