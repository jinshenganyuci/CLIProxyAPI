package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type selectionUsageV844 struct{ records chan coreusage.Record }

func (p *selectionUsageV844) HandleUsage(_ context.Context, r coreusage.Record) { p.records <- r }

func TestV844SelectionUsageKeepsConcurrencyAndCallerScope(t *testing.T) {
	m, e, auths, models := newCredentialConcurrencyManager(t, 3, 3)
	b := auths[1].Clone()
	b.Metadata["concurrency_limit"] = 2
	var err error
	b, err = m.Update(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	auths[1] = b
	c := auths[2].Clone()
	delete(c.Metadata, "concurrency_limit")
	c, err = m.Update(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	auths[2] = c
	hold := func(a *Auth, n int) {
		t.Helper()
		for range n {
			release, err := m.AcquireCredentialConcurrency(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
		}
	}
	hold(auths[0], 3)
	usage := coreusage.NewManager(8)
	usage.Start(t.Context())
	t.Cleanup(usage.Stop)
	capture := &selectionUsageV844{records: make(chan coreusage.Record, 3)}
	usage.Register(capture)
	var ids, scopes []string
	scheduler := &fakePluginScheduler{pick: func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
		id, _ := req.Options.Metadata[cliproxyexecutor.SelectionIDMetadataKey].(string)
		if _, err := uuid.Parse(id); err != nil {
			t.Fatalf("selection ID: %q %v", id, err)
		}
		ids = append(ids, id)
		scope, _ := req.Options.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
		scopes = append(scopes, scope)
		for _, a := range req.Candidates {
			if a.ID == auths[0].ID {
				t.Fatal("saturated credential entered delegated candidate set")
			}
		}
		return pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst}, true, nil
	}}
	m.SetPluginScheduler(scheduler)
	turn := 0
	e.execute = func(ctx context.Context, a *Auth, _ cliproxyexecutor.Request, o cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		want := auths[2]
		if turn == 0 {
			want = auths[1]
		}
		if a.ID != want.ID {
			t.Fatal("delegated scheduler escaped eligible credentials")
		}
		limit := 10
		if turn == 0 {
			limit = 2
		}
		requireCredentialConcurrencySnapshot(t, m, a.ID, 1, limit)
		scope, _ := o.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
		if scope != []string{"key-a", "key-b", "key-c"}[turn] {
			t.Fatal("caller scope changed at execution")
		}
		selectionID := coreusage.SelectionIDFromContext(ctx)
		if turn < 2 && selectionID != ids[turn] {
			t.Fatal("execution lost plugin selection ID")
		}
		if turn == 2 && selectionID != "" {
			t.Fatal("built-in selection inherited stale plugin ID")
		}
		usage.Publish(ctx, coreusage.Record{Provider: "test", AuthID: a.ID})
		return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
	}
	for i, key := range []string{"key-a", "key-b", "key-c"} {
		turn = i
		if i == 1 {
			hold(auths[1], 2)
		}
		if i == 2 {
			m.SetPluginScheduler(nil)
		}
		opts := credentialConcurrencyOptions(key)
		opts.Metadata[cliproxyexecutor.SelectionIDMetadataKey] = "caller-spoof"
		ctx := coreusage.WithSelectionID(t.Context(), "inherited-spoof")
		if _, err = m.Execute(ctx, []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, opts); err != nil {
			t.Fatal(err)
		}
		record := awaitCredentialConcurrencySignal(t, capture.records)
		wantID := ""
		if i < 2 {
			wantID = ids[i]
		}
		if record.SelectionID != wantID {
			t.Fatalf("published selection=%q want=%q", record.SelectionID, wantID)
		}
		if opts.Metadata[cliproxyexecutor.SelectionIDMetadataKey] != "caller-spoof" {
			t.Fatal("attempt mutated caller-owned metadata")
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] || scopes[0] != "key-a" || scopes[1] != "key-b" {
		t.Fatal("independent selections reused ID or caller scope")
	}
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 3, 3)
	requireCredentialConcurrencySnapshot(t, m, auths[1].ID, 2, 2)
	requireCredentialConcurrencySnapshot(t, m, auths[2].ID, 0, 10)
}
