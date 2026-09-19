package audit

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestReservationRechecksExpirationAfterClientLock(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "upstream"
		if cached {
			name = "cache"
		}
		t.Run(name, func(t *testing.T) {
			store := testStore(t)
			p, key := configureBillingPolicy(t, store, 0)
			cfg := testInference(t, store, p)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			locker, err := store.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer locker.Rollback()
			var backend int
			if err := locker.QueryRowContext(ctx, "SELECT pg_backend_pid() FROM client_api_keys WHERE id=$1 FOR UPDATE", key.ID).Scan(&backend); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "lock waiter", cached, time.Now())
				done <- err
			}()
			for {
				var waiting bool
				if err := store.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", backend).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("reservation did not wait for client lock")
				case <-time.After(time.Millisecond):
				}
			}
			// This expiration is after the waiter's transaction start but before
			// it receives the row lock; transaction-stable NOW() would accept it.
			if _, err := locker.ExecContext(ctx, "UPDATE client_api_keys SET expires_at=clock_timestamp() WHERE id=$1", key.ID); err != nil {
				t.Fatal(err)
			}
			if err := locker.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; errorCode(err) != "invalid_api_key" || auditHTTPStatus(err) != http.StatusUnauthorized {
				t.Fatal("lock waiter accepted an expired key", err)
			}
		})
	}
}
