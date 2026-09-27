package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestCatalogQuotaDedupDoesNotChangeLocalAdmissionUpgrade(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, executor, auths, models := newCredentialConcurrencyManager(t, 3, 0)
	for _, model := range models {
		m.MarkResult(context.Background(), Result{AuthID: auths[2].ID, Provider: "test", Model: model, Success: true})
	}
	retry := time.Hour
	m.MarkResult(context.Background(), Result{AuthID: auths[2].ID, Provider: "test", Model: models[0], Error: &Error{HTTPStatus: http.StatusTooManyRequests, Code: "usage_limit_reached"}, RetryAfter: &retry, CredentialScope: true})
	for range 10 {
		release, err := m.AcquireCredentialConcurrency(context.Background(), auths[0])
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
	for _, model := range models {
		if count := registry.GetGlobalRegistry().GetModelCount(model); count != 2 {
			t.Fatalf("catalog healthy count=%d, want2 despite local occupancy", count)
		}
	}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: auths[0].ID}}
	if _, err := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, opts); !IsCredentialConcurrencyError(err) {
		t.Fatalf("full credential error=%v", err)
	}
	executor.execute = func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		if a.ID != auths[1].ID {
			t.Fatalf("busy or upstream-cooled credential selected: %s", a.ID)
		}
		return cliproxyexecutor.Response{}, nil
	}
	if _, err := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, cliproxyexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	healthy, _ := m.GetByID(auths[0].ID)
	if healthy.Failed != 0 || healthy.Unavailable || healthy.Quota.Exceeded {
		t.Fatal("catalog dedup or local busy created provider cooldown")
	}
	cooled, _ := m.GetByID(auths[2].ID)
	if !cooled.Quota.Exceeded || cooled.Quota.Reason != "credential_quota" || !cooled.NextRetryAfter.After(time.Now()) {
		t.Fatal("catalog dedup cleared real upstream credential cooldown")
	}
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 10, 10)
	requireCredentialConcurrencySnapshot(t, m, auths[1].ID, 0, 10)
	requireCredentialConcurrencySnapshot(t, m, auths[2].ID, 0, 10)
}
