package auth

import (
	"reflect"
	"testing"
)

func TestMergeAuthPreservesCodexCredentialIdentity(t *testing.T) {
	const originalNamespace = "6f3c61a1-f5bd-4265-93ed-a28612867322"
	const rotatedNamespace = "45686c34-dae7-498f-97d9-e6a1bf171a69"
	mergeFunctions := map[string]func(*Auth, *Auth, *Auth) *Auth{
		"refresh": MergeRefreshedAuth,
		"prepare": MergePreparedAuth,
	}
	testCases := []struct {
		name             string
		baseNamespace    string
		currentNamespace string
	}{
		{"unchanged", originalNamespace, originalNamespace},
		{"concurrent_rotation", originalNamespace, rotatedNamespace},
		{"concurrent_initialization", "", originalNamespace},
	}
	for mergeName, mergeAuth := range mergeFunctions {
		for _, testCase := range testCases {
			t.Run(mergeName+"/"+testCase.name, func(t *testing.T) {
				base := &Auth{
					ID:                "codex-identity.json",
					Provider:          "codex",
					RegistrationEpoch: 1,
					Status:            StatusActive,
					ProxyURL:          "direct",
					Metadata: map[string]any{
						"type":          "codex",
						"access_token":  "test-old-access",
						"refresh_token": "test-old-refresh",
					},
				}
				if testCase.baseNamespace != "" {
					base.Metadata["codex_identity_version"] = 1
					base.Metadata["codex_identity_namespace"] = testCase.baseNamespace
				}
				current := base.Clone()
				current.Metadata["codex_identity_version"] = 1
				current.Metadata["codex_identity_namespace"] = testCase.currentNamespace
				current.ProxyURL = "http://127.0.0.1:18080"
				updated := base.Clone()
				updated.Metadata["access_token"] = "test-new-access"
				updated.Metadata["refresh_token"] = "test-new-refresh"
				baseBefore := base.Clone()
				currentBefore := current.Clone()
				updatedBefore := updated.Clone()

				merged := mergeAuth(base, current, updated)
				if merged == nil {
					t.Fatal("merged auth is nil")
				}
				if got := merged.Metadata["codex_identity_namespace"]; got != testCase.currentNamespace {
					t.Fatalf("namespace = %v, want %s", got, testCase.currentNamespace)
				}
				if got := merged.Metadata["codex_identity_version"]; got != 1 {
					t.Fatalf("identity version = %v, want 1", got)
				}
				for _, tokenKey := range []string{"access_token", "refresh_token"} {
					if merged.Metadata[tokenKey] != updated.Metadata[tokenKey] {
						t.Fatalf("%s was not refreshed", tokenKey)
					}
				}
				if merged.ProxyURL != current.ProxyURL || merged.Metadata["proxy_url"] != current.ProxyURL {
					t.Fatal("concurrent proxy change was not preserved")
				}
				if !reflect.DeepEqual(base, baseBefore) || !reflect.DeepEqual(current, currentBefore) || !reflect.DeepEqual(updated, updatedBefore) {
					t.Fatal("merge modified an input auth")
				}
			})
		}
	}
}
