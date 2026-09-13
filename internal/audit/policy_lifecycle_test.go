package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPolicyArchiveRestoreDeleteAndImport(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	s := app.Store
	ctx := context.Background()
	var file policyFile
	if err := json.Unmarshal(call("GET", "/admin/policies/"+p.ID+"/export", nil, 200), &file); err != nil {
		t.Fatal(err)
	}
	call("PUT", "/admin/policies/"+p.ID+"/archive", map[string]any{"archived": true, "expected_revision": p.Revision}, 200)
	archived, err := s.Policy(ctx, p.ID)
	if err != nil || !archived.Archived || archived.Enabled {
		t.Fatal(archived, err)
	}
	if _, err := s.PolicyByAlias(ctx, p.Alias); err != ErrNotFound {
		t.Fatal("archived policy resolved", err)
	}
	if _, _, err := s.RouteSnapshot(ctx, p.ID, nil); errorCode(err) != "policy_archived" {
		t.Fatal(err)
	}
	if err := s.SetPolicyState(ctx, "admin", p.ID, archived.Revision, true); errorCode(err) != "policy_archived" {
		t.Fatal(err)
	}
	items, err := s.Policies(ctx)
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	channels, err := s.Channels(ctx)
	if err != nil || !strings.Contains(channels[0].PolicyNames[0], "已归档") {
		t.Fatal(channels, err)
	}
	call("PUT", "/admin/policies/"+p.ID+"/archive", map[string]any{"archived": false, "expected_revision": archived.Revision}, 200)
	restored, err := s.Policy(ctx, p.ID)
	if err != nil || restored.Archived || restored.Enabled {
		t.Fatal(restored, err)
	}
	call("PUT", "/admin/policies/"+p.ID+"/archive", map[string]any{"archived": true, "expected_revision": restored.Revision}, 200)
	call("DELETE", "/admin/policies/"+p.ID, nil, 409)
	keys, err := s.Keys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := keys[0]
	key.Active = false
	key.PolicyIDs = []string{}
	if err := s.UpdateKey(ctx, "admin", key, key.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, AuditLog{ID: "historical-policy", PolicyID: p.ID}, "", 30); err != nil {
		t.Fatal(err)
	}
	call("DELETE", "/admin/policies/"+p.ID, nil, 200)
	if _, err := s.LogDetail(ctx, "historical-policy"); err != nil {
		t.Fatal("history deleted", err)
	}
	if err := s.Bootstrap(ctx, "admin", "unused-password-123456"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Policy(ctx, p.ID); err != ErrNotFound {
		t.Fatal("deleted default policy recreated", err)
	}
	file.Name = "Imported"
	file.Alias = "imported-policy"
	var imported Policy
	if err := json.Unmarshal(call("POST", "/admin/policies/import", file, 201), &imported); err != nil || imported.Enabled || imported.Archived {
		t.Fatal(imported, err)
	}
	call("POST", "/admin/policies/import", file, 409)
	updated := imported.Config
	updated.Prompt = "private policy material"
	updated.Threshold = 0.65
	if err := s.SaveConfig(ctx, "admin", imported.ID, imported.Name, imported.Revision, updated); err != nil {
		t.Fatal(err)
	}
	var details string
	if err := s.DB.QueryRow("SELECT details::text FROM admin_action_logs WHERE action='policy.save' AND resource_id=$1 ORDER BY id DESC LIMIT 1", imported.ID).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(details, "private policy material") || !strings.Contains(details, "after_sha256") || !strings.Contains(details, "threshold") {
		t.Fatal("unsafe or missing change summary", details)
	}
}
