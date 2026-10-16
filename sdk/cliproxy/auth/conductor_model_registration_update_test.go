package auth

import (
	"context"
	"testing"
	"time"
)

func TestUpdateModelRegistrationAuthPreservesNativeUpdateFields(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := WithSkipPersist(context.Background())
	base, errRegister := manager.Register(ctx, &Auth{ID: "discovery-update-fields", Provider: "codex", Status: StatusActive})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	updated := base.Clone()
	updated.Label = "discovered account"
	updated.Prefix = "discovered"
	updated.ProxyURL = "http://selected-proxy.invalid:8080"
	updated.Disabled = true
	updated.Status = StatusDisabled
	updated.NextRefreshAfter = time.Unix(2_000_000_000, 0).UTC()
	updated.Metadata = map[string]any{"access_token": "discovered-token", "plugin_data": "retained"}
	actual, errUpdate := manager.UpdateModelRegistrationAuth(ctx, base, updated)
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if actual.Label != updated.Label || actual.Prefix != updated.Prefix || actual.ProxyURL != updated.ProxyURL || !actual.Disabled || actual.Status != StatusDisabled || !actual.NextRefreshAfter.Equal(updated.NextRefreshAfter) || actual.Metadata["access_token"] != "discovered-token" || actual.Metadata["plugin_data"] != "retained" {
		t.Fatal("model registration CAS narrowed the native full AuthUpdate contract")
	}
}

func TestUpdateModelRegistrationAuthRejectsStaleSnapshot(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "new_generation", true: "new_registration_epoch"}[replace], func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			ctx := WithSkipPersist(context.Background())
			base, errRegister := manager.Register(ctx, &Auth{ID: "discovery-update-stale", Provider: "codex", ProxyURL: "http://old.invalid:8080", Metadata: map[string]any{"access_token": "old-token", "codex_identity_namespace": "old-namespace"}})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			newer := base.Clone()
			newer.ProxyURL = "http://new.invalid:8080"
			newer.Metadata["access_token"] = "new-token"
			newer.Metadata["codex_identity_namespace"] = "new-namespace"
			if replace {
				manager.Remove(ctx, base.ID)
				if _, errNew := manager.Register(ctx, newer); errNew != nil {
					t.Fatal(errNew)
				}
			} else if _, errNew := manager.Update(ctx, newer); errNew != nil {
				t.Fatal(errNew)
			}
			stale := base.Clone()
			stale.Metadata["access_token"] = "stale-plugin-token"
			if updated, errUpdate := manager.UpdateModelRegistrationAuth(ctx, base, stale); errUpdate == nil || updated != nil {
				t.Fatal("stale model discovery update was accepted")
			}
			actual, _ := manager.GetByID(base.ID)
			if actual.ProxyURL != newer.ProxyURL || actual.Metadata["access_token"] != "new-token" || actual.Metadata["codex_identity_namespace"] != "new-namespace" {
				t.Fatal("stale model discovery update changed the current credential")
			}
		})
	}
}

func TestV830ConditionalRegistrationUpdateRejectsInvalidConcurrency(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := WithSkipPersist(context.Background())
	base, errRegister := manager.Register(ctx, &Auth{ID: t.Name(), Provider: "codex", Metadata: map[string]any{"access_token": "original", "concurrency_limit": 3}})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	changed := base.Clone()
	changed.Metadata["concurrency_limit"] = 0
	if updated, committed, errUpdate := manager.UpdateIfRegistrationCurrent(ctx, base, changed); errUpdate == nil || committed || updated != nil {
		t.Fatalf("invalid plugin update committed: %+v %t %v", updated, committed, errUpdate)
	}
	current, _ := manager.GetByID(base.ID)
	if EffectiveConcurrencyLimit(current) != 3 || current.Generation != base.Generation {
		t.Fatal("rejected update changed existing credential")
	}
}

func TestV830ConditionalRegistrationUpdatePreservesNewCredentialControls(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := WithSkipPersist(context.Background())
	base, errRegister := manager.Register(ctx, &Auth{ID: t.Name(), Provider: "codex", ProxyURL: "direct", Metadata: map[string]any{"access_token": "old", "concurrency_limit": 3, "codex_identity_namespace": "old-identity"}})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	fresh := base.Clone()
	fresh.ProxyURL = "http://new.invalid:8080"
	fresh.Metadata["access_token"] = "new"
	fresh.Metadata["concurrency_limit"] = 2
	fresh.Metadata["codex_identity_namespace"] = "new-identity"
	if _, errUpdate := manager.Update(ctx, fresh); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	stale := base.Clone()
	stale.Metadata["access_token"] = "old-plugin-result"
	if updated, committed, _ := manager.UpdateIfRegistrationCurrent(ctx, base, stale); committed || updated != nil {
		t.Fatal("old plugin discovery overwrote newer token and controls")
	}
	current, _ := manager.GetByID(base.ID)
	if current.Metadata["access_token"] != "new" || current.Metadata["codex_identity_namespace"] != "new-identity" || current.ProxyURL != fresh.ProxyURL || EffectiveConcurrencyLimit(current) != 2 {
		t.Fatal("new credential controls changed")
	}
}
