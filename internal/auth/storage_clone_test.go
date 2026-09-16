package auth_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	baseauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/vertex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
)

func clonedStorageMetadata(storage baseauth.TokenStorage) map[string]any {
	switch typed := storage.(type) {
	case *codex.CodexTokenStorage:
		return typed.Metadata
	case *claude.ClaudeTokenStorage:
		return typed.Metadata
	case *kimi.KimiTokenStorage:
		return typed.Metadata
	case *xai.TokenStorage:
		return typed.Metadata
	case *meta.MetaTokenStorage:
		return typed.Metadata
	case *vertex.VertexCredentialStorage:
		return typed.Metadata
	default:
		return nil
	}
}

func TestCloneForMetadataUpdatePreservesNativeTokensAndIdentity(t *testing.T) {
	for _, test := range []struct {
		name    string
		storage baseauth.TokenStorage
	}{
		{name: "codex", storage: &codex.CodexTokenStorage{AccessToken: "access", RefreshToken: "refresh", IDToken: "id", AccountID: "account", IdentityVersion: 1, IdentityNamespace: "identity", Expire: "expiry"}},
		{name: "claude", storage: &claude.ClaudeTokenStorage{AccessToken: "access", RefreshToken: "refresh", IDToken: "id", AccountUUID: "account", OrganizationUUID: "organization", DeviceIDs: []string{"device"}, Expire: "expiry"}},
		{name: "kimi", storage: &kimi.KimiTokenStorage{AccessToken: "access", RefreshToken: "refresh", DeviceID: "device", Scope: "scope", Expired: "expiry"}},
		{name: "xai", storage: &xai.TokenStorage{AccessToken: "access", RefreshToken: "refresh", IDToken: "id", Subject: "subject", TokenEndpoint: "endpoint", RedirectURI: "redirect", Expire: "expiry"}},
		{name: "meta", storage: &meta.MetaTokenStorage{AccessToken: "access", DCAToken: "dca", APIKey: "api", AuthKind: "oauth", ExpiresIn: 600, DCAExpiresAt: 12345, Expired: "expiry"}},
		{name: "vertex", storage: &vertex.VertexCredentialStorage{ProjectID: "project", Email: "email", Prefix: "prefix", ServiceAccount: map[string]any{"private_key": "synthetic-private", "nested": map[string]any{"value": "original"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata := map[string]any{"nested": map[string]any{"items": []any{"original"}}, "proxy_url": "http://selected.invalid:8080", "custom": "preserved"}
			test.storage.(interface{ SetMetadata(map[string]any) }).SetMetadata(metadata)
			before, errBefore := json.Marshal(test.storage)
			if errBefore != nil {
				t.Fatal(errBefore)
			}
			cloned, errClone := test.storage.(baseauth.MetadataStorageCloner).CloneForMetadataUpdate()
			if errClone != nil {
				t.Fatal(errClone)
			}
			encoded, errEncoded := json.Marshal(cloned)
			if errEncoded != nil || !bytes.Equal(before, encoded) {
				t.Fatalf("native token or identity fields changed during cloning: %v", errEncoded)
			}
			cloneMetadata := clonedStorageMetadata(cloned)
			cloneMetadata["nested"].(map[string]any)["items"].([]any)[0] = "changed"
			if metadata["nested"].(map[string]any)["items"].([]any)[0] != "original" {
				t.Fatal("clone shares a nested metadata slice with live storage")
			}
			switch typed := cloned.(type) {
			case *claude.ClaudeTokenStorage:
				typed.DeviceIDs[0] = "changed"
				if test.storage.(*claude.ClaudeTokenStorage).DeviceIDs[0] != "device" {
					t.Fatal("clone shares its credential device identity slice")
				}
			case *vertex.VertexCredentialStorage:
				typed.ServiceAccount["nested"].(map[string]any)["value"] = "changed"
				if test.storage.(*vertex.VertexCredentialStorage).ServiceAccount["nested"].(map[string]any)["value"] != "original" {
					t.Fatal("clone shares its service account map")
				}
			}
			cloned.(interface{ SetMetadata(map[string]any) }).SetMetadata(map[string]any{"concurrency_limit": 3})
			if metadata["custom"] != "preserved" || clonedStorageMetadata(test.storage)["custom"] != "preserved" {
				t.Fatal("setting cloned metadata mutated the source storage")
			}
		})
	}
}

func TestCloneForMetadataUpdateRejectsNilAndUnserializableMetadata(t *testing.T) {
	for _, storage := range []baseauth.MetadataStorageCloner{
		(*codex.CodexTokenStorage)(nil), (*claude.ClaudeTokenStorage)(nil), (*kimi.KimiTokenStorage)(nil),
		(*xai.TokenStorage)(nil), (*meta.MetaTokenStorage)(nil), (*vertex.VertexCredentialStorage)(nil),
	} {
		if cloned, errClone := storage.CloneForMetadataUpdate(); errClone == nil || cloned != nil {
			t.Fatalf("nil storage clone = %#v, error = %v", cloned, errClone)
		}
	}
	if _, errClone := baseauth.CloneMetadataForUpdate(map[string]any{"invalid": make(chan struct{})}); errClone == nil {
		t.Fatal("unserializable metadata was silently discarded")
	}
	cloned, errClone := baseauth.CloneMetadataForUpdate(map[string]any{"large": json.Number("9007199254740993")})
	if errClone != nil || cloned["large"] != json.Number("9007199254740993") {
		t.Fatalf("large metadata integer lost precision: %v, %v", cloned, errClone)
	}
}

func TestCloneMetadataForUpdatePreservesNumericTypes(t *testing.T) {
	metadata := map[string]any{
		"priority": int(3), "weight": float64(4), "retry": json.Number("5"),
		"nested": map[string]any{"limits": []int{3, 10}},
	}
	cloned, errClone := baseauth.CloneMetadataForUpdate(metadata)
	if errClone != nil {
		t.Fatal(errClone)
	}
	if !reflect.DeepEqual(metadata, cloned) {
		t.Fatal("clone changed numeric types used by three-way metadata comparisons")
	}
	cloned["nested"].(map[string]any)["limits"].([]int)[0] = 9
	if metadata["nested"].(map[string]any)["limits"].([]int)[0] != 3 {
		t.Fatal("clone shares a typed metadata slice")
	}
}
