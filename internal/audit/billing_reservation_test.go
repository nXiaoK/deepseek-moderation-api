package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBudgetEnabledAfterUnestimatedImageExpense(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "in_flight"
		if pending {
			name = "pending"
		}
		t.Run(name, func(t *testing.T) {
			store := testStore(t)
			ctx := context.Background()
			p, key := configureBillingPolicy(t, store, 0)
			cfg := testInference(t, store, p)
			cfg.ImageCount = 1
			now := time.Now()
			entry, err := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "image request", false, now)
			if err != nil || entry.Price == nil || entry.Reserved != 0 {
				t.Fatal("image reservation failed", entry, err)
			}
			var valid bool
			if err := store.DB.QueryRow("SELECT reservation_valid FROM audit_costs WHERE id=$1", entry.ID).Scan(&valid); err != nil || valid {
				t.Fatal("image reservation was marked reliable", valid, err)
			}
			if pending {
				cost, err := store.SettleCost(ctx, entry, Usage{Attempted: true}, now)
				if err != nil || cost.Status != "pending" || cost.AmountCNY != nil {
					t.Fatal("missing image usage did not stay pending", cost, err)
				}
			}
			if _, err := store.DB.Exec("INSERT INTO client_budgets(client_id,monthly_limit) VALUES($1,1000000000000)", key.ID); err != nil {
				t.Fatal(err)
			}
			cfg.ImageCount = 0
			if _, err := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "new text request", false, now); errorCode(err) != "budget_pending" {
				t.Fatal("unestimated image spend was ignored by new budget", err)
			}
			if _, err := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "cached request", true, now); err != nil {
				t.Fatal("unknown spend blocked a free local cache hit", err)
			}
			if !pending {
				return
			}
			app, err := NewServer(store, "http://localhost:8090", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"amount_cny":"0","reason":"supplier confirmed no charge"}`))
			req.SetPathValue("id", entry.ID)
			req = req.WithContext(context.WithValue(ctx, sessionKey{}, session{Username: "admin"}))
			if err := app.reconcileCost(httptest.NewRecorder(), req); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "after reconciliation", false, now); err != nil {
				t.Fatal("reconciled image cost still blocked budget", err)
			}
		})
	}
}

func TestFreeTextReservationRemainsValidWhileUsagePending(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	p, key := configureBillingPolicy(t, store, 0)
	cfg := testInference(t, store, p)
	raw, _ := json.Marshal(PriceRates{})
	if _, err := store.DB.Exec("INSERT INTO model_prices(model,rates,source,author,credential_id,effective_at) VALUES($1,$2,'free tariff','admin',$3,clock_timestamp()-INTERVAL '1 second')", cfg.Model, string(raw), cfg.CredentialID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	entry, err := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "free text", false, now)
	if err != nil || entry.Reserved != 0 {
		t.Fatal("free text reservation failed", err)
	}
	if _, err := store.SettleCost(ctx, entry, Usage{Attempted: true}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec("INSERT INTO client_budgets(client_id,monthly_limit) VALUES($1,0)", key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "more free text", false, now); err != nil {
		t.Fatal("valid zero reservation was treated as unknown expense", err)
	}
}

func TestReservationValidityMigrationPreservesLegacyHolds(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	p, key := configureBillingPolicy(t, store, 0)
	cfg := testInference(t, store, p)
	price, err := store.ConnectionPrice(ctx, cfg.Model, cfg.CredentialID, time.Now())
	if err != nil || price == nil {
		t.Fatal("missing price", err)
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("ALTER TABLE audit_costs DROP COLUMN reservation_valid"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id       string
		reserved int64
		price    *PriceCard
	}{
		{"image_unknown", 0, price},
		{"text_pending", 1000, price},
		{"text_free", 0, &PriceCard{ID: price.ID, Rates: PriceRates{}}},
		{"unpriced", 0, nil},
	} {
		snapshot, _ := json.Marshal(tc.price)
		var priceID any
		if tc.price != nil {
			priceID = tc.price.ID
		}
		if _, err := tx.Exec(`INSERT INTO audit_costs(id,client_id,kind,policy_id,request_id,model,price_id,price_snapshot,started_at,budget_date,reserved_pico,status) VALUES($1,$2,'production',$3,$1,$4,$5,$6,NOW(),CURRENT_DATE,$7,'pending')`, tc.id, key.ID, p.ID, cfg.Model, priceID, string(snapshot), tc.reserved); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := migrationFiles.ReadFile("migrations/0005_cost_reservation_validity.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"image_unknown": false, "text_pending": true, "text_free": true, "unpriced": false} {
		var valid bool
		if err := tx.QueryRow("SELECT reservation_valid FROM audit_costs WHERE id=$1", id).Scan(&valid); err != nil || valid != want {
			t.Fatalf("legacy %s valid=%v, want %v: %v", id, valid, want, err)
		}
	}
}
