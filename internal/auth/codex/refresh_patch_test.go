package codex

import (
	"reflect"
	"testing"
)

func TestCredentialRefreshPatchForChangesPreservesPartitionIsolation(t *testing.T) {
	for _, changed := range []string{"outer", "siwc", "both", "metadata_only", "empty_siwc", "siwc_metadata_only"} {
		t.Run(changed, func(t *testing.T) {
			before := map[string]any{
				"access_token": "outer-old", "refresh_token": "outer-ref-old",
				"proxy_url": "direct", "concurrency_limit": 3,
				"siwc": map[string]any{"access_token": "siwc-old", "refresh_token": "siwc-ref-old"},
			}
			if changed == "empty_siwc" {
				delete(before, "siwc")
			}
			after := cloneStringAnyMap(before)
			after["proxy_url"], after["concurrency_limit"] = "socks5://127.0.0.1:1080", 7
			outer := changed == "outer" || changed == "both"
			siwc := changed == "siwc" || changed == "both"
			if outer {
				after["access_token"], after["refresh_token"] = "outer-new", "outer-ref-new"
			}
			if siwc {
				after["siwc"].(map[string]any)["access_token"] = "siwc-new"
				after["siwc"].(map[string]any)["refresh_token"] = "siwc-ref-new"
			}
			if changed == "empty_siwc" {
				after["siwc"] = map[string]any{}
			}
			if changed == "siwc_metadata_only" {
				after["siwc"].(map[string]any)["note"] = "operator note"
			}
			patch := NewCredentialRefreshPatchForChanges(before, after)
			if !outer && !siwc {
				if patch != nil {
					t.Fatal("metadata-only or empty partition change authorized a credential refresh")
				}
				return
			}
			if patch == nil || !patch.CanApplyTo(before) {
				t.Fatal("matching credential baseline rejected")
			}
			disk := cloneStringAnyMap(before)
			if outer {
				disk["access_token"], disk["refresh_token"] = "outer-newer", "outer-ref-newer"
			}
			if siwc {
				disk["siwc"].(map[string]any)["access_token"] = "siwc-newer"
				disk["siwc"].(map[string]any)["refresh_token"] = "siwc-ref-newer"
			}
			if patch.CanApplyTo(disk) || !reflect.DeepEqual(patch.Apply(disk), disk) {
				t.Fatal("unchanged partition permitted a stale credential refresh")
			}
			applied := patch.Apply(before)
			if applied["proxy_url"] != "direct" || applied["concurrency_limit"] != 3 {
				t.Fatal("credential patch changed operator metadata")
			}
			if outer && applied["refresh_token"] != "outer-ref-new" {
				t.Fatal("outer refresh was lost")
			}
			if siwc && applied["siwc"].(map[string]any)["refresh_token"] != "siwc-ref-new" {
				t.Fatal("SIWC refresh was lost")
			}
		})
	}
	before := map[string]any{"access_token": "old", "refresh_token": "old-ref"}
	after := map[string]any{"access_token": "new", "refresh_token": "new-ref"}
	patch := NewCredentialRefreshPatchForChanges(before, after)
	if patch == nil || patch.CanApplyTo(map[string]any{"access_token": "newer", "refresh_token": "newer-ref"}) {
		t.Fatal("absent SIWC partition authorized a stale outer refresh")
	}
}

func TestCredentialRefreshPatchChecksPartitionsIndependently(t *testing.T) {
	for _, rotated := range []string{"outer", "siwc"} {
		t.Run(rotated, func(t *testing.T) {
			before := map[string]any{
				"access_token": "outer-old", "refresh_token": "outer-ref-old", "last_refresh": "2026-10-12T12:00:00Z",
				"siwc": map[string]any{"access_token": "siwc-old", "refresh_token": "siwc-ref-old", "last_refresh": "2026-10-12T12:00:00Z"},
			}
			after := cloneStringAnyMap(before)
			after["access_token"], after["refresh_token"] = "outer-new", "outer-ref-new"
			after["siwc"].(map[string]any)["access_token"] = "siwc-new"
			after["siwc"].(map[string]any)["refresh_token"] = "siwc-ref-new"
			patch := NewCredentialRefreshPatch(before, after, true, true)
			disk := cloneStringAnyMap(before)
			wantOuter, wantSIWC := "outer-ref-new", "siwc-ref-new"
			if rotated == "outer" {
				disk["access_token"], disk["refresh_token"] = "outer-bound", "outer-ref-bound"
				wantOuter = "outer-ref-bound"
			} else {
				disk["siwc"].(map[string]any)["access_token"] = "siwc-bound"
				disk["siwc"].(map[string]any)["refresh_token"] = "siwc-ref-bound"
				wantSIWC = "siwc-ref-bound"
			}
			// Changing source/result maps later must not mutate the immutable patch.
			before["refresh_token"] = "mutated-before"
			after["refresh_token"] = "mutated-after"
			saved := patch.Apply(disk)
			if saved["refresh_token"] != wantOuter || saved["siwc"].(map[string]any)["refresh_token"] != wantSIWC {
				t.Fatalf("partition version check lost a valid refresh or overwrote a bind: %#v", saved)
			}
		})
	}
}
