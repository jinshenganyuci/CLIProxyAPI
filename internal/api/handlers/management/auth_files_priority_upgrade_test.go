package management

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAuthFilePriorityPatchPreservesLiveConcurrencyUpgrade(t *testing.T) {
	h, manager, original := newConcurrencyManagementFixture(t)
	original.Attributes[coreauth.AttributeSourceBackend] = coreauth.AuthSourceFile
	original.ProxyURL, _ = original.Metadata["proxy_url"].(string)
	original, errUpdate := manager.Update(context.Background(), original)
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), original)
	if errAcquire != nil {
		t.Fatal(errAcquire)
	}
	t.Cleanup(release)
	for _, raw := range []string{"7", "0", "-2", "null"} {
		response := concurrencyManagementRequest(h, http.MethodPatch, "/auth-files/fields", `{"name":"a.json","concurrency_limit":3,"priority":`+raw+`}`)
		if response.Code != http.StatusOK {
			t.Fatalf("priority %s: %d/%s", raw, response.Code, response.Body.String())
		}
		updated, _ := manager.GetByID(original.ID)
		want := raw
		if raw == "0" || raw == "null" {
			want = ""
		}
		if updated.Attributes["priority"] != want || (updated.Attributes[coreauth.AttributeFilePriority] == "true") != (raw != "null") {
			t.Fatalf("priority %s: runtime attribute or file marker mismatch", raw)
		}
		if updated.ID != original.ID || updated.Index != original.Index || updated.ProxyURL != original.ProxyURL || updated.Metadata["access_token"] != original.Metadata["access_token"] {
			t.Fatal("priority patch changed credential identity, token, or proxy")
		}
		if inflight, limit := manager.CredentialConcurrencySnapshot(original.ID); inflight != 1 || limit != 3 {
			t.Fatalf("priority patch changed active leases: %d/%d", inflight, limit)
		}
		data, errRead := os.ReadFile(filepath.Join(h.cfg.AuthDir, "a.json"))
		if errRead != nil {
			t.Fatal(errRead)
		}
		var persisted map[string]any
		if errDecode := json.Unmarshal(data, &persisted); errDecode != nil {
			t.Fatal(errDecode)
		}
		priority, present := persisted["priority"]
		if (present && priority != nil) != (raw != "null") || persisted["proxy_url"] != original.Metadata["proxy_url"] || persisted["concurrency_limit"] != float64(3) {
			t.Fatalf("priority %s persistence mismatch: present=%t priority=%v proxy_equal=%t limit=%v", raw, present, priority, persisted["proxy_url"] == original.Metadata["proxy_url"], persisted["concurrency_limit"])
		}
	}
}
