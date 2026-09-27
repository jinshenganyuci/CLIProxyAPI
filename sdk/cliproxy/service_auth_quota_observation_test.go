package cliproxy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type serviceQuotaRefreshExecutor struct {
	syncTestExecutor
	observation coreauth.QuotaState
}

func (executor *serviceQuotaRefreshExecutor) Identifier() string { return "devin" }

func (executor *serviceQuotaRefreshExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	updated := auth.Clone()
	updated.Metadata["api_key"] = "refreshed-devin-fixture-key"
	updated.Metadata["plan"] = "Pro"
	updated.Quota = executor.observation.Clone()
	return updated, nil
}

func TestServiceAuthQuotaObservationSurvivesRefreshPersistenceAndCanonicalWatcher(t *testing.T) {
	const authID = "devin-quota-watcher.json"
	const proxyURL = "http://127.0.0.1:1"
	cfg := &config.Config{AuthDir: t.TempDir()}
	path := filepath.Join(cfg.AuthDir, authID)
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(cfg.AuthDir)
	seed := &coreauth.Auth{
		ID: authID, FileName: authID, Provider: "devin", Status: coreauth.StatusActive,
		ProxyURL: proxyURL, Attributes: map[string]string{coreauth.AttributePath: path},
		Metadata: map[string]any{
			"type": "devin", "api_key": "original-devin-fixture-key", "proxy_url": proxyURL,
			"weight": float64(3), "concurrency": float64(2), "prefix": "operator-prefix",
		},
	}
	if _, errSave := store.Save(context.Background(), seed); errSave != nil {
		t.Fatalf("persist initial credential: %v", errSave)
	}
	fileWatcher, errWatcher := watcher.NewWatcher("", cfg.AuthDir, nil)
	if errWatcher != nil {
		t.Fatal(errWatcher)
	}
	t.Cleanup(func() {
		if errStop := fileWatcher.Stop(); errStop != nil {
			t.Errorf("stop file watcher: %v", errStop)
		}
	})
	fileWatcher.SetConfig(cfg)
	manager := coreauth.NewManager(store, nil, nil)
	manager.SetConfig(cfg)
	service := &Service{cfg: cfg, coreManager: manager}
	t.Cleanup(func() { internalregistry.GetGlobalRegistry().UnregisterClient(authID) })
	ctx := coreauth.WithSkipPersist(context.Background())
	canonicalUpdate := func(input *coreauth.Auth) watcher.AuthUpdate {
		t.Helper()
		update := watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: authID, Auth: input}
		// No queue or background listener is needed: the watcher returns the
		// canonical file snapshot and revision synchronously to the caller.
		queued, revision := fileWatcher.DispatchPersistedAuthUpdateWithRevision(&update)
		if queued || revision == 0 || update.Revision() != revision || update.Auth == nil {
			t.Fatalf("canonical watcher result: queued=%t revision=%d update=%+v", queued, revision, update)
		}
		return update
	}
	initialUpdate := canonicalUpdate(seed)
	service.handleAuthUpdate(ctx, initialUpdate)
	initial, exists := manager.GetByID(authID)
	if !exists || initial.Metadata["api_key"] != "original-devin-fixture-key" {
		t.Fatal("canonical baseline was not registered")
	}
	wantQuota := coreauth.QuotaState{
		ObservedAt: time.Unix(200, 0).UTC(),
		Signals:    map[string]string{"daily_quota_remaining_percent": "95%", "weekly_quota_remaining_percent": "45%"},
	}
	manager.RegisterExecutor(&serviceQuotaRefreshExecutor{observation: wantQuota})
	refreshed, errRefresh := manager.ForceRefreshAuth(context.Background(), authID)
	if errRefresh != nil {
		t.Fatalf("force refresh: %v", errRefresh)
	}
	if !reflect.DeepEqual(refreshed.Quota, wantQuota) {
		t.Fatalf("refresh result lost observation before watcher: %+v", refreshed.Quota)
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read refreshed credential: %v", errRead)
	}
	var persisted map[string]any
	if errDecode := json.Unmarshal(data, &persisted); errDecode != nil {
		t.Fatalf("decode refreshed credential: %v", errDecode)
	}
	if persisted["api_key"] != "refreshed-devin-fixture-key" || persisted["plan"] != "Pro" {
		t.Fatal("refresh did not persist new credential and durable metadata")
	}
	for _, key := range []string{"quota", "observed_at", "signals", "daily_quota_remaining_percent", "weekly_quota_remaining_percent"} {
		if _, exists := persisted[key]; exists {
			t.Fatalf("transient observation %q was persisted", key)
		}
	}
	refreshedUpdate := canonicalUpdate(refreshed)
	if refreshedUpdate.Revision() <= initialUpdate.Revision() {
		t.Fatal("persisted refresh did not advance watcher revision")
	}
	if !refreshedUpdate.Auth.Quota.ObservedAt.IsZero() || len(refreshedUpdate.Auth.Quota.Signals) != 0 {
		t.Fatal("fixture did not exercise a canonical file snapshot without passive quota")
	}
	if coreauth.CredentialsChanged(refreshed, refreshedUpdate.Auth) {
		t.Fatal("canonical file did not contain the current refreshed credential")
	}
	service.handleAuthUpdate(ctx, refreshedUpdate)
	current, exists := manager.GetByID(authID)
	if !exists || !reflect.DeepEqual(current.Quota, wantQuota) {
		t.Fatalf("canonical watcher reload erased refreshed passive quota: %+v", current)
	}
	if current.RegistrationEpoch != initial.RegistrationEpoch || current.Generation <= refreshed.Generation || service.authRevisions[authID] != refreshedUpdate.Revision() {
		t.Fatal("canonical reload changed registration epoch or lost generation/revision ordering")
	}
	if current.ProxyURL != proxyURL || current.Metadata["proxy_url"] != proxyURL || current.Metadata["weight"] != float64(3) || current.Metadata["concurrency"] != float64(2) || current.Prefix != "operator-prefix" || !current.LastRefreshedAt.Equal(refreshed.LastRefreshedAt) {
		t.Fatal("canonical reload lost configured controls or refresh timestamp")
	}
	if !refreshedUpdate.Auth.Quota.ObservedAt.IsZero() || len(refreshedUpdate.Auth.Quota.Signals) != 0 {
		t.Fatal("service mutated the canonical watcher input snapshot")
	}
	service.handleAuthUpdate(ctx, initialUpdate)
	afterStale, _ := manager.GetByID(authID)
	if !reflect.DeepEqual(afterStale, current) {
		t.Fatal("older canonical revision changed refreshed runtime state")
	}
}

func TestPrepareCoreAuthQuotaObservationBoundaries(t *testing.T) {
	for _, test := range []struct {
		name         string
		provider     string
		token        string
		incoming     coreauth.QuotaState
		wantExisting bool
	}{
		{name: "same credential canonical reload", wantExisting: true},
		{name: "same credential older observation", incoming: coreauth.QuotaState{ObservedAt: time.Unix(10, 0), Signals: map[string]string{"daily": "older"}}, wantExisting: true},
		{name: "token replacement", token: "replacement-key"},
		{name: "provider replacement", provider: "claude"},
		{name: "incoming newer snapshot", incoming: coreauth.QuotaState{ObservedAt: time.Unix(30, 0), Signals: map[string]string{"weekly": "newer"}}},
		{name: "incoming empty observed snapshot", incoming: coreauth.QuotaState{ObservedAt: time.Unix(30, 0)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			service := &Service{cfg: &config.Config{}, coreManager: manager}
			existing := &coreauth.Auth{
				ID: "devin-quota-boundaries", Provider: "devin", Status: coreauth.StatusActive,
				Metadata: map[string]any{"api_key": "same-key"},
				Quota: coreauth.QuotaState{
					Exceeded: true, Reason: "expired-fixture", NextRecoverAt: time.Unix(5, 0), BackoffLevel: 4,
					ObservedAt: time.Unix(20, 0), Signals: map[string]string{"daily": "current", "stale": "old-snapshot"},
				},
			}
			if _, errRegister := manager.Register(context.Background(), existing); errRegister != nil {
				t.Fatal(errRegister)
			}
			incoming := &coreauth.Auth{
				ID: existing.ID, Provider: existing.Provider, Status: coreauth.StatusActive,
				Metadata: map[string]any{"api_key": "same-key"}, Quota: test.incoming.Clone(),
			}
			if test.token != "" {
				incoming.Metadata["api_key"] = test.token
			}
			if test.provider != "" {
				incoming.Provider = test.provider
			}
			beforeIncoming := incoming.Clone()
			prepared := service.prepareCoreAuthForModelRegistration(coreauth.WithSkipPersist(context.Background()), incoming)
			current, exists := manager.GetByID(existing.ID)
			want := test.incoming.Clone()
			if test.wantExisting {
				want.ObservedAt = existing.Quota.ObservedAt
				want.Signals = existing.Quota.Clone().Signals
			}
			if !exists || prepared == nil || !reflect.DeepEqual(current.Quota, want) || !reflect.DeepEqual(prepared.Quota, want) {
				t.Fatalf("prepared/runtime quota = %#v / %#v, want %#v", prepared, current, want)
			}
			if current.Provider != incoming.Provider || current.Metadata["api_key"] != incoming.Metadata["api_key"] {
				t.Fatal("quota preservation interfered with credential replacement")
			}
			if !reflect.DeepEqual(incoming, beforeIncoming) {
				t.Fatal("preparation mutated incoming auth")
			}
			if len(prepared.Quota.Signals) > 0 {
				prepared.Quota.Signals["mutation"] = "must remain isolated"
				stored, _ := manager.GetByID(existing.ID)
				if stored.Quota.Signals["mutation"] != "" || incoming.Quota.Signals["mutation"] != "" || existing.Quota.Signals["mutation"] != "" {
					t.Fatal("prepared observation aliases a source or manager snapshot")
				}
			}
		})
	}
}
