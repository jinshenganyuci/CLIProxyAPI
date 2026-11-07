package synthesizer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV859OAuthProviderInheritancePreservesForkMetadataSnapshot(t *testing.T) {
	for _, explicitHint := range []string{"", "plugin-portal"} {
		t.Run("hint="+explicitHint, func(t *testing.T) {
			const namespace = "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"
			const preciseNumber = uint64(9007199254740993)
			raw := []byte(`{"type":"codex","oauth_provider":" Codex-Portal ","codex_identity_version":1,"codex_identity_namespace":"` + namespace + `","proxy_url":"direct","concurrency_limit":3,"disabled":true}`)
			storage := &v859MetadataSnapshotStorage{}
			parser := multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
				metadata := map[string]any{"plugin_owned": map[string]any{"precise_number": preciseNumber}}
				if explicitHint != "" {
					metadata["oauth_provider"] = explicitHint
				}
				return []*coreauth.Auth{{ID: "codex.json", Provider: "codex", Metadata: metadata, Storage: storage}}, true, nil
			})
			authDir := t.TempDir()
			auths, errSynthesize := SynthesizeAuthFile(&SynthesisContext{
				AuthDir: authDir, PluginAuthParser: parser, Now: time.Now(), StrictFileParsing: true,
			}, filepath.Join(authDir, "codex.json"), raw)
			if errSynthesize != nil || len(auths) != 1 {
				t.Fatalf("SynthesizeAuthFile() count=%d error=%v", len(auths), errSynthesize)
			}
			auth := auths[0]
			wantHint := explicitHint
			if wantHint == "" {
				wantHint = "codex-portal"
			}
			for _, metadata := range []map[string]any{auth.Metadata, storage.metadata} {
				if metadata["oauth_provider"] != wantHint || metadata[codex.CredentialIdentityNamespaceMetadataKey] != namespace || metadata[codex.CredentialIdentityVersionMetadataKey] != float64(1) {
					t.Fatalf("OAuth provider or credential identity was lost: %#v", metadata)
				}
				if metadata["concurrency_limit"] != 3 || metadata["disabled"] != true {
					t.Fatalf("storage snapshot preceded inherited controls: %#v", metadata)
				}
				if metadata["plugin_owned"].(map[string]any)["precise_number"] != preciseNumber {
					t.Fatal("plugin metadata lost integer precision")
				}
			}
			if auth.ProxyURL != "direct" || coreauth.EffectiveConcurrencyLimit(auth) != 3 || !auth.Disabled {
				t.Fatalf("inherited credential controls differ: proxy=%q limit=%d disabled=%t", auth.ProxyURL, coreauth.EffectiveConcurrencyLimit(auth), auth.Disabled)
			}
			auth.Metadata["plugin_owned"].(map[string]any)["precise_number"] = uint64(1)
			if storage.metadata["plugin_owned"].(map[string]any)["precise_number"] != preciseNumber {
				t.Fatal("persisted metadata snapshot shares mutable plugin metadata")
			}
		})
	}
}

type v859MetadataSnapshotStorage struct {
	metadata map[string]any
}

func (s *v859MetadataSnapshotStorage) SetMetadata(metadata map[string]any) {
	s.metadata = (&coreauth.Auth{Metadata: metadata}).Clone().Metadata
}

func (s *v859MetadataSnapshotStorage) SaveTokenToFile(string) error {
	return nil
}
