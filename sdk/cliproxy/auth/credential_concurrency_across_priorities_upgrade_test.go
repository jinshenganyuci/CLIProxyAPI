package auth

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestCredentialConcurrencyAcrossPrioritiesExecution(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		for _, policy := range []string{"explicit-low", "legacy", "unhandled", "delegate", "full-id"} {
			t.Run(mode+"/"+policy, func(t *testing.T) {
				m, executor, auths, models := newCredentialConcurrencyManager(t, 5, 0)
				for index, priority := range []int{10, 5, 100, 50, 1} {
					updated, _ := m.GetByID(auths[index].ID)
					updated.Attributes = map[string]string{"priority": fmt.Sprint(priority)}
					if index == 3 {
						updated.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(time.Hour)}
					}
					if index == 4 {
						updated.Disabled = true
					}
					var errUpdate error
					auths[index], errUpdate = m.Update(context.Background(), updated)
					if errUpdate != nil {
						t.Fatal(errUpdate)
					}
				}
				for range 10 {
					release, errAcquire := m.AcquireCredentialConcurrency(context.Background(), auths[2])
					if errAcquire != nil {
						t.Fatal(errAcquire)
					}
					t.Cleanup(release)
				}
				wantID := auths[0].ID
				if policy == "explicit-low" {
					wantID = auths[1].ID
				}
				scheduler := &fakePluginScheduler{acrossPriorities: policy != "legacy"}
				scheduler.pick = func(_ context.Context, request pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
					want := []string{auths[0].ID, auths[1].ID}
					if policy == "legacy" {
						want = want[:1]
					}
					var got []string
					for _, candidate := range request.Candidates {
						got = append(got, candidate.ID)
					}
					sort.Strings(got)
					sort.Strings(want)
					if strings.Join(got, ",") != strings.Join(want, ",") {
						t.Fatalf("plugin candidates = %v, want %v; full, cooled and disabled credentials must remain excluded", got, want)
					}
					switch policy {
					case "unhandled":
						return pluginapi.SchedulerPickResponse{}, false, nil
					case "delegate":
						return pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst}, true, nil
					case "full-id":
						return pluginapi.SchedulerPickResponse{Handled: true, AuthID: auths[2].ID}, true, nil
					default:
						return pluginapi.SchedulerPickResponse{Handled: true, AuthID: wantID}, true, nil
					}
				}
				m.SetPluginScheduler(scheduler)
				calls := 0
				observe := func(auth *Auth) {
					calls++
					if auth.ID != wantID {
						t.Fatalf("executed credential %s, want %s", auth.ID, wantID)
					}
					requireCredentialConcurrencySnapshot(t, m, wantID, 1, 10)
				}
				executor.execute = func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					observe(auth)
					return cliproxyexecutor.Response{}, nil
				}
				executor.stream = func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					observe(auth)
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				}
				var errExecute error
				request := cliproxyexecutor.Request{Model: models[0]}
				switch mode {
				case "execute":
					_, errExecute = m.Execute(context.Background(), []string{"test"}, request, credentialConcurrencyOptions("key-a"))
				case "count":
					_, errExecute = m.ExecuteCount(context.Background(), []string{"test"}, request, credentialConcurrencyOptions("key-b"))
				case "stream":
					var stream *cliproxyexecutor.StreamResult
					stream, errExecute = m.ExecuteStream(context.Background(), []string{"test"}, request, credentialConcurrencyOptions("key-c"))
					if errExecute == nil {
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					}
				}
				if errExecute != nil || calls != 1 || scheduler.calls != 1 {
					t.Fatalf("error/executor/plugin calls = %v/%d/%d", errExecute, calls, scheduler.calls)
				}
				requireCredentialConcurrencySnapshot(t, m, wantID, 0, 10)
				requireCredentialConcurrencySnapshot(t, m, auths[2].ID, 10, 10)
			})
		}
	}
}

func TestCredentialConcurrencyAcrossPrioritiesSpecialSelections(t *testing.T) {
	for name, selectAuth := range specialConcurrencySelectors() {
		t.Run(name, func(t *testing.T) {
			m, high, low := newSpecialConcurrencyManager(t)
			high.Attributes = map[string]string{"priority": "100"}
			if _, errUpdate := m.Update(context.Background(), high); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			scheduler := &fakePluginScheduler{acrossPriorities: true, resp: pluginapi.SchedulerPickResponse{Handled: true, AuthID: low.ID}, handled: true}
			m.SetPluginScheduler(scheduler)
			selected, release, errSelect := selectAuth(context.Background(), m, cliproxyexecutor.Options{})
			if release != nil {
				defer release()
			}
			if errSelect != nil || selected == nil || selected.ID != low.ID || scheduler.calls != 1 || len(scheduler.requests[0].Candidates) != 2 {
				t.Fatalf("selection/error/plugin = %#v/%v/%#v", selected, errSelect, scheduler.requests)
			}
			requireCredentialConcurrencySnapshot(t, m, low.ID, 1, 1)
			release()
			requireCredentialConcurrencySnapshot(t, m, low.ID, 0, 1)
		})
	}
}

func TestCredentialConcurrencyAcrossPrioritiesAdmissionRace(t *testing.T) {
	m, executor, auths, models := newCredentialConcurrencyManager(t, 2, 1)
	high, _ := m.GetByID(auths[0].ID)
	high.Attributes = map[string]string{"priority": "100"}
	if _, errUpdate := m.Update(context.Background(), high); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	scheduler := &fakePluginScheduler{acrossPriorities: true}
	scheduler.pick = func(ctx context.Context, request pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
		if len(request.Candidates) != 2 {
			t.Fatalf("candidate count = %d, want 2", len(request.Candidates))
		}
		release, errAcquire := m.AcquireCredentialConcurrency(ctx, auths[1])
		if errAcquire != nil {
			t.Fatal(errAcquire)
		}
		t.Cleanup(release)
		return pluginapi.SchedulerPickResponse{Handled: true, AuthID: auths[1].ID}, true, nil
	}
	m.SetPluginScheduler(scheduler)
	executor.execute = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		t.Fatal("lost admission race reached executor")
		return cliproxyexecutor.Response{}, nil
	}
	_, errExecute := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, credentialConcurrencyOptions("key-a"))
	if !IsCredentialConcurrencyError(errExecute) || scheduler.calls != 1 {
		t.Fatalf("error/plugin calls = %v/%d, want local busy/1", errExecute, scheduler.calls)
	}
	current, _ := m.GetByID(auths[1].ID)
	if current.Failed != 0 || current.Unavailable || current.Quota.Exceeded || !current.NextRetryAfter.IsZero() || len(current.ModelStates) != 0 {
		t.Fatalf("local capacity rejection mutated upstream state: %#v", current)
	}
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 0, 1)
	requireCredentialConcurrencySnapshot(t, m, auths[1].ID, 1, 1)
}
