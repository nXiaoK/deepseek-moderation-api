package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestModelOutputRetentionAndEncryption(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, enabled := range []bool{false, true} {
		id := randomToken("audit_")
		l := AuditLog{ID: id, ModelOutputStored: enabled, ModelOutputRetentionDays: 1, ModelOutput: "private diagnosis", Attempts: []AuditAttempt{{ID: "attempt", ModelOutput: "private intermediate"}}, Reason: "structured reason"}
		if err := s.Record(ctx, l, "", 30); err != nil {
			t.Fatal(err)
		}
		if l.Attempts[0].ModelOutput != "private intermediate" {
			t.Fatal("recording mutated live response")
		}
		var raw, encrypted []byte
		if err := s.DB.QueryRow("SELECT metadata,output_cipher FROM audit_requests WHERE id=$1", id).Scan(&raw, &encrypted); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("private diagnosis")) || bytes.Contains(raw, []byte("private intermediate")) {
			t.Fatal("raw output in metadata")
		}
		if (len(encrypted) > 0) != enabled {
			t.Fatal("unexpected output retention")
		}
		detail, err := s.LogDetail(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if enabled {
			if detail.ModelOutput != "private diagnosis" || detail.Attempts[0].ModelOutput != "private intermediate" {
				t.Fatal("output not restored")
			}
			if _, err := s.DB.Exec("UPDATE audit_requests SET output_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1", id); err != nil {
				t.Fatal(err)
			}
			detail, err = s.LogDetail(ctx, id)
			if err != nil || detail.ModelOutput != "" || detail.ModelOutputStored {
				t.Fatal("expired output disclosed", err)
			}
			if err := s.Cleanup(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.DB.QueryRow("SELECT output_cipher FROM audit_requests WHERE id=$1", id).Scan(&encrypted); err != nil || len(encrypted) > 0 {
				t.Fatal("expired ciphertext retained", err)
			}
		} else if detail.ModelOutput != "" || detail.Attempts[0].ModelOutput != "" {
			t.Fatal("disabled output retained")
		}
		if detail.Reason != "structured reason" {
			t.Fatal("structured verdict lost")
		}
	}
}
func TestLegacyPolicyOutputDefaultsAndRecords(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.Policy(ctx, "abuse-default")
	if err != nil {
		t.Fatal(err)
	}
	if p.Config.retainModelOutput() {
		t.Fatal("new policies must opt in to raw output")
	}
	if _, err := s.DB.Exec("UPDATE audit_policies SET config=config-'store_model_output'-'model_output_retention_days' WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	p, err = s.Policy(ctx, p.ID)
	if err != nil || !p.Config.retainModelOutput() || p.Config.ModelOutputRetentionDays != p.Config.RetentionDays {
		t.Fatal("legacy setting not preserved", err)
	}
	l := AuditLog{ID: "legacy", ModelOutput: "legacy output"}
	raw, _ := json.Marshal(l)
	if _, err := s.DB.Exec(`INSERT INTO audit_requests(id,kind,policy_id,client_id,flagged,error_code,metadata,expires_at) VALUES('legacy','test','','',FALSE,'',$1,NOW()+INTERVAL '1 day')`, string(raw)); err != nil {
		t.Fatal(err)
	}
	detail, err := s.LogDetail(ctx, "legacy")
	if err != nil || detail.ModelOutput != "legacy output" || !detail.ModelOutputStored {
		t.Fatal("legacy output unreadable", err)
	}
}
