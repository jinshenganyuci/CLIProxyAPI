package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUpgrade837SIWCExpiryPatchPreservesDiskPartitions(t *testing.T) {
	for _, mode := range []string{"update", "remove_legacy", "concurrent_bind"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "synthetic.json")
			original := []byte(`{"access_token":"outer-old","refresh_token":"outer-refresh","use_siwc":true,"namespace":"stable","unknown":{"exact":9007199254740993},"siwc":{"access_token":"inner-old","refresh_token":"inner-refresh","expired":"early-old","expires_at":"real-old","note":"keep"}}`)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			var before map[string]any
			if err := json.Unmarshal(original, &before); err != nil {
				t.Fatal(err)
			}
			after := cloneStringAnyMap(before)
			inner := after["siwc"].(map[string]any)
			inner["access_token"], inner["expired"] = "inner-refreshed", "early-new"
			if mode == "update" {
				inner["expires_at"] = "real-new"
			} else {
				delete(inner, "expires_at")
			}
			patch := NewCredentialRefreshPatchForChanges(before, after)
			if patch == nil || patch.RefreshedOuter() || !patch.RefreshedSIWC() {
				t.Fatal("expiry patch authorized the wrong credential partition")
			}
			disk := cloneStringAnyMap(before)
			disk["access_token"] = "outer-concurrent"
			disk["disabled"] = true
			if mode == "concurrent_bind" {
				disk["siwc"].(map[string]any)["access_token"] = "inner-bound"
				disk["siwc"].(map[string]any)["expires_at"] = "real-bound"
			}
			merged := patch.Apply(disk)
			if merged["access_token"] != "outer-concurrent" || merged["disabled"] != true || merged["namespace"] != "stable" {
				t.Fatal("SIWC refresh overwrote another partition or operator controls")
			}
			savedInner := merged["siwc"].(map[string]any)
			switch mode {
			case "concurrent_bind":
				if patch.CanApplyTo(disk) || !reflect.DeepEqual(merged, disk) {
					t.Fatal("stale expiry deletion overwrote a concurrent SIWC bind")
				}
			case "remove_legacy":
				if _, exists := savedInner["expires_at"]; exists {
					t.Fatal("legacy refresh retained an obsolete real expiry")
				}
			case "update":
				if savedInner["expires_at"] != "real-new" || savedInner["expired"] != "early-new" {
					t.Fatal("early and real expiry were not updated together")
				}
			}
			if savedInner["note"] != "keep" {
				t.Fatal("SIWC operator metadata was lost")
			}
			if err := WithFileLock(path, func() error { return WriteJSONFileAtomic(path, merged) }); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Unknown map[string]json.RawMessage `json:"unknown"`
			}
			if err := json.Unmarshal(raw, &saved); err != nil || string(saved.Unknown["exact"]) != "9007199254740993" {
				t.Fatal("expiry persistence lost unrelated numeric precision")
			}
		})
	}
	var absent *CredentialRefreshPatch
	if absent.RefreshedOuter() || absent.RefreshedSIWC() {
		t.Fatal("nil patch reported a refreshed partition")
	}
}
