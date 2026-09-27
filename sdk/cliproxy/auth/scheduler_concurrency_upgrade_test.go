package auth

import (
	"context"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestSchedulerRebuildKeepsLiveCredentialAdmission(t *testing.T) {
	m, executor, auths, models := newCredentialConcurrencyManager(t, 2, 0)
	stale := m.snapshotAuths()
	releases := make([]func(), 0, 10)
	for range 10 {
		release, errAcquire := m.AcquireCredentialConcurrency(context.Background(), auths[0])
		if errAcquire != nil {
			t.Fatal(errAcquire)
		}
		releases = append(releases, release)
		t.Cleanup(release)
	}
	m.syncSchedulerFromSnapshot(stale)
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 10, 10)
	executedID := ""
	executor.execute = func(_ context.Context, selected *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		executedID = selected.ID
		return cliproxyexecutor.Response{}, nil
	}
	_, errExecute := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, cliproxyexecutor.Options{})
	if errExecute != nil || executedID != auths[1].ID {
		t.Fatalf("rebuild executed full credential: %v/%v", executedID, errExecute)
	}
	current, _ := m.GetByID(auths[0].ID)
	current.Metadata["concurrency_limit"] = 3
	current.ProxyURL = "socks5://synthetic.invalid:1080"
	current.Metadata["identity-marker"] = "unchanged"
	if _, errUpdate := m.Update(context.Background(), current); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	m.syncSchedulerFromSnapshot(stale)
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 10, 3)
	after, _ := m.GetByID(auths[0].ID)
	if after.ProxyURL != current.ProxyURL || after.Metadata["identity-marker"] != "unchanged" {
		t.Fatal("scheduler rebuild changed credential identity")
	}
	if _, errAcquire := m.AcquireCredentialConcurrency(context.Background(), auths[0]); !IsCredentialConcurrencyError(errAcquire) {
		t.Fatalf("stale scheduler snapshot reset updated limit: %v", errAcquire)
	}
	for _, release := range releases {
		release()
	}
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 0, 3)
}

func TestSchedulerRebuildPreservesAdmissionAcrossRegistrationEpochs(t *testing.T) {
	m, _, auths, _ := newCredentialConcurrencyManager(t, 1, 3)
	oldRelease, errAcquire := m.AcquireCredentialConcurrency(context.Background(), auths[0])
	if errAcquire != nil {
		t.Fatal(errAcquire)
	}
	defer oldRelease()
	stale := m.snapshotAuths()
	m.Remove(context.Background(), auths[0].ID)
	replacement, errRegister := m.Register(context.Background(), &Auth{ID: auths[0].ID, Provider: "test", Status: StatusActive, Metadata: map[string]any{"concurrency_limit": 1}})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	m.syncSchedulerFromSnapshot(stale)
	if _, errStale := m.AcquireCredentialConcurrency(context.Background(), auths[0]); errStale == nil {
		t.Fatal("removed registration regained admission through rebuild")
	}
	if _, errFull := m.AcquireCredentialConcurrency(context.Background(), replacement); !IsCredentialConcurrencyError(errFull) {
		t.Fatalf("replacement ignored still-running previous request: %v", errFull)
	}
	oldRelease()
	release, errAcquire := m.AcquireCredentialConcurrency(context.Background(), replacement)
	if errAcquire != nil {
		t.Fatal(errAcquire)
	}
	release()
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 0, 1)
}

func TestSchedulerStructuralSyncConcurrentAdmissionNeverExceedsDefault(t *testing.T) {
	m, _, auths, _ := newCredentialConcurrencyManager(t, 1, 0)
	const count = 30
	start := make(chan struct{})
	finish := make(chan struct{})
	admitted := make(chan bool, count)
	var workers sync.WaitGroup
	for range count {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			m.RefreshSchedulerEntry(auths[0].ID)
			m.syncScheduler()
			release, errAcquire := m.AcquireCredentialConcurrency(context.Background(), auths[0])
			if errAcquire != nil {
				admitted <- false
				return
			}
			admitted <- true
			<-finish
			release()
		}()
	}
	close(start)
	successes := 0
	for range count {
		if <-admitted {
			successes++
		}
	}
	if successes != 10 {
		close(finish)
		workers.Wait()
		t.Fatalf("concurrent structural sync admitted %d, want10", successes)
	}
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 10, 10)
	close(finish)
	workers.Wait()
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 0, 10)
}

func TestSessionNodeMetadataUpgradeClearsStaleRole(t *testing.T) {
	for _, kind := range []string{"fork", "compaction"} {
		t.Run(kind, func(t *testing.T) {
			ctx := logging.WithClientRequestMetadata(context.Background(), logging.ClientRequestMetadata{SessionID: "old", ParentSessionID: "old-parent", NodeKind: "fork", IsFork: true})
			metadata := map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "new", cliproxyexecutor.ParentSessionIDMetadataKey: "parent", cliproxyexecutor.NodeKindMetadataKey: kind, cliproxyexecutor.IsForkMetadataKey: kind == "fork", cliproxyexecutor.IsCompactionMetadataKey: kind == "compaction"}
			ctx = syncMetadataSessionToContext(ctx, metadata)
			got := logging.GetClientRequestMetadata(ctx)
			if got.SessionID != "new" || got.ParentSessionID != "parent" || got.NodeKind != kind || got.IsFork != (kind == "fork") || got.IsCompaction != (kind == "compaction") {
				t.Fatalf("wrong role projection: %+v", got)
			}
			got = logging.GetClientRequestMetadata(syncMetadataSessionToContext(ctx, map[string]any{}))
			if got.SessionID != "" || got.ParentSessionID != "" || got.NodeKind != "" || got.IsFork || got.IsCompaction {
				t.Fatalf("stale role survives metadata reset: %+v", got)
			}
		})
	}
}

func TestLCPCompactionDoesNotBypassCredentialCapacity(t *testing.T) {
	m, executor, auths, models := newCredentialConcurrencyManager(t, 2, 0)
	selector := NewSessionAffinitySelector(&FillFirstSelector{})
	defer selector.Stop()
	m.SetSelector(selector)
	firstPayload := []byte(`{"contents":[{"role":"user","parts":[{"text":"step one"}]},{"role":"model","parts":[{"text":"ack one"}]},{"role":"user","parts":[{"text":"step two"}]},{"role":"model","parts":[{"text":"ack two"}]},{"role":"user","parts":[{"text":"step three"}]}]}`)
	compactedPayload := []byte(`{"contents":[{"role":"user","parts":[{"text":"summary of steps one-two"}]},{"role":"model","parts":[{"text":"ack two"}]},{"role":"user","parts":[{"text":"step three"}]},{"role":"user","parts":[{"text":"step four"}]}]}`)
	var selected string
	var metadata map[string]any
	executor.execute = func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, executionOpts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		selected = a.ID
		metadata = cloneSchedulerAnyMap(executionOpts.Metadata)
		return cliproxyexecutor.Response{}, nil
	}
	first := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, OriginalRequest: firstPayload, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "compaction-caller"}}
	if _, err := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0], Payload: firstPayload}, first); err != nil {
		t.Fatal(err)
	}
	if selected != auths[0].ID {
		t.Fatalf("initial auth=%s", selected)
	}
	parent := metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]
	for range 10 {
		release, err := m.AcquireCredentialConcurrency(context.Background(), auths[0])
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
	compacted := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, OriginalRequest: compactedPayload, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "compaction-caller"}}
	if _, err := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0], Payload: compactedPayload}, compacted); err != nil {
		t.Fatal(err)
	}
	if selected != auths[1].ID {
		t.Fatalf("compaction bypassed full parent credential: %s", selected)
	}
	if metadata[cliproxyexecutor.NodeKindMetadataKey] != "compaction" || metadata[cliproxyexecutor.ParentSessionIDMetadataKey] != parent {
		t.Fatalf("compaction lineage lost during capacity fallback: %#v", metadata)
	}
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 10, 10)
	requireCredentialConcurrencySnapshot(t, m, auths[1].ID, 0, 10)
	compactedSession := metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]
	other := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, OriginalRequest: compactedPayload, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "other-caller"}}
	if _, err := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0], Payload: compactedPayload}, other); err != nil {
		t.Fatal(err)
	}
	if metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey] == compactedSession || metadata[cliproxyexecutor.ParentSessionIDMetadataKey] != nil {
		t.Fatalf("another key inherited compaction lineage: %#v", metadata)
	}
}

func TestKimiDomainAliasesKeepIndependentDefaultAdmission(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	executor := &credentialConcurrencyExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: "kimi"}}
	var executed string
	executor.execute = func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		executed = a.ID
		return cliproxyexecutor.Response{}, nil
	}
	manager.RegisterExecutor(executor)
	credentials := make([]*Auth, 0, 2)
	for _, provider := range []string{"kimi.com", "kimi.ai"} {
		a, err := manager.Register(context.Background(), &Auth{ID: "domain-capacity-" + provider, Provider: provider, Status: StatusActive})
		if err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, provider, []*registry.ModelInfo{{ID: "kimi-k2"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
		credentials = append(credentials, a)
	}
	held := make([]func(), 0, 10)
	for range 10 {
		release, err := manager.AcquireCredentialConcurrency(context.Background(), credentials[0])
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, release)
		t.Cleanup(release)
	}
	for _, provider := range []string{"kimi", "kimi.com"} {
		executed = ""
		_, err := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: "kimi-k2"}, cliproxyexecutor.Options{})
		if !IsCredentialConcurrencyError(err) || executed != "" {
			t.Fatalf("full com domain leaked to another credential via %s: %v/%s", provider, err, executed)
		}
	}
	for _, provider := range []string{"kimi-ai", "kimi.ai"} {
		executed = ""
		_, err := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: "kimi-k2"}, cliproxyexecutor.Options{})
		if err != nil || executed != credentials[1].ID {
			t.Fatalf("AI domain unavailable while com full via %s: %v/%s", provider, err, executed)
		}
	}
	requireCredentialConcurrencySnapshot(t, manager, credentials[0].ID, 10, 10)
	requireCredentialConcurrencySnapshot(t, manager, credentials[1].ID, 0, 10)
	for _, release := range held {
		release()
	}
	for _, a := range credentials {
		requireCredentialConcurrencySnapshot(t, manager, a.ID, 0, 10)
	}
}
