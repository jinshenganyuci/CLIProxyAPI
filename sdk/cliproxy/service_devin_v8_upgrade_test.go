package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestDevinV8ServiceRoutesAggregatesAndPreservesCredentialControls(t *testing.T) {
	usePublishedDevinCatalogV842(t)
	const authID = "devin-v8-catalog-migration"
	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(authID)
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	auth := &coreauth.Auth{
		ID: authID, Provider: "devin", Status: coreauth.StatusActive,
		ProxyURL:   "socks5h://127.0.0.1:19001",
		Attributes: map[string]string{"auth_kind": "oauth", "concurrency_limit": "3", "priority": "7"},
		Metadata:   map[string]any{"api_key": "synthetic-devin-v8-key", "proxy_url": "socks5h://127.0.0.1:19001", "concurrency_limit": 3},
	}
	before := auth.Clone()
	service := &Service{cfg: &config.Config{}}
	service.registerModelsForAuth(context.Background(), auth)
	if !reflect.DeepEqual(auth, before) {
		t.Fatalf("catalog registration mutated credential/key/proxy controls: got %#v, want %#v", auth, before)
	}
	if got, want := len(reg.GetModelsForClient(authID)), len(registry.GetDevinModels()); got != want {
		t.Fatalf("registered catalog = %d, want %d", got, want)
	}

	for _, tc := range []struct {
		legacy string
		base   string
		effort string
		uid    string
	}{
		{"devin/gpt-6-astra-high", "devin/gpt-6-astra", "high", "gpt-6-astra-high"},
		{"devin/claude-opus-5-low-fast", "devin/claude-opus-5", "low", "claude-opus-5-low"},
		{"devin/model_gpt_5_2_high", "devin/model_gpt_5_2", "high", "MODEL_GPT_5_2_HIGH"},
	} {
		t.Run(tc.legacy, func(t *testing.T) {
			if got := util.GetProviderName(tc.legacy); len(got) != 0 {
				t.Fatalf("legacy exact variant unexpectedly routable: %v", got)
			}
			metadata := registry.LookupDevinModel(tc.legacy)
			if metadata == nil || metadata.ID != tc.base {
				t.Fatalf("legacy metadata lookup failed: %#v", metadata)
			}
			migrated := tc.base + "(" + tc.effort + ")"
			parsed := thinking.ParseSuffix(migrated)
			if got := util.GetProviderName(parsed.ModelName); len(got) != 1 || got[0] != "devin" {
				t.Fatalf("migrated model lacks Devin route: %v", got)
			}
			if uid := helps.ResolveDevinChatModelUID(migrated, "", 0); uid != tc.uid {
				t.Fatalf("migrated model upstream UID = %q, want %q", uid, tc.uid)
			}
		})
	}
}

func TestDevinV8AliasSourcesAndExclusionsNeedCanonicalIDs(t *testing.T) {
	const authID = "devin-v8-alias-migration"
	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(authID)
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	auth := &coreauth.Auth{ID: authID, Provider: "devin", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}}
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"devin": {{Name: "devin/gpt-6-astra-high", Alias: "private-old-high", Fork: true}},
	}}
	service := &Service{cfg: cfg}
	service.registerModelsForAuth(context.Background(), auth)
	if got := util.GetProviderName("private-old-high"); len(got) != 0 {
		t.Fatalf("removed variant source should not produce a registered alias: %v", got)
	}
	cfg.OAuthModelAlias["devin"][0].Name = "devin/gpt-6-astra"
	service.registerModelsForAuth(context.Background(), auth)
	if got := util.GetProviderName("private-old-high"); len(got) != 1 || got[0] != "devin" {
		t.Fatalf("canonical source should restore custom alias route: %v", got)
	}
	models := registry.GetDevinModels()
	oldExclusion := applyExcludedModels(models, []string{"devin/gpt-6-astra-high"})
	newExclusion := applyExcludedModels(models, []string{"devin/gpt-6-astra"})
	contains := func(models []*ModelInfo, id string) bool {
		for _, model := range models {
			if model.ID == id {
				return true
			}
		}
		return false
	}
	if !contains(oldExclusion, "devin/gpt-6-astra") || contains(newExclusion, "devin/gpt-6-astra") {
		t.Fatal("exclusion matching did not distinguish old variant and canonical base")
	}
}

// usePublishedDevinCatalogV842 preserves the published catalog compatibility
// contract even when the current upstream catalog retires legacy model IDs.
func usePublishedDevinCatalogV842(t *testing.T) {
	t.Helper()
	original := registry.GetDevinModelsJSON()
	originalPath := filepath.Join(t.TempDir(), "original-devin.json")
	if errWrite := os.WriteFile(originalPath, original, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	fixture := func(relative string) string {
		t.Helper()
		path, errAbs := filepath.Abs(filepath.Join("..", "..", "internal", "registry", relative))
		if errAbs != nil {
			t.Fatal(errAbs)
		}
		return path
	}
	notifications := make(chan struct{}, 8)
	registry.SetModelRefreshCallback(func(providers []string) {
		for _, provider := range providers {
			if provider == "devin" {
				notifications <- struct{}{}
			}
		}
	})
	load := func(path string) {
		t.Helper()
		wanted, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if string(registry.GetDevinModelsJSON()) == string(wanted) {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		registry.StartModelCatalogUpdaters(ctx, registry.CatalogSources{
			Catalog: fixture("models/models.json"), CodexCatalog: fixture("models/codex_client_models.json"), DevinCatalog: path,
		}, false)
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-notifications:
				if string(registry.GetDevinModelsJSON()) == string(wanted) {
					return
				}
			case <-timer.C:
				t.Fatal("local Devin catalog was not published")
			}
		}
	}
	t.Cleanup(func() {
		defer registry.SetModelRefreshCallback(nil)
		load(originalPath)
	})
	load(fixture("testdata/devin_models_v842.json"))
}
