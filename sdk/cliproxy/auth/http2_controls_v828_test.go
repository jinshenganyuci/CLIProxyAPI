package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestV828HTTP2RetryPreservesThreeKeyBindingAndLeases(t *testing.T) {
	for _, message := range []string{"http2: client conn is closed", "http2: client connection force closed via ClientConn.Close"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", message, stream), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx := context.Background()
					m, e, auths, models := newCredentialConcurrencyManager(t, 3, 0)
					m.SetRetryConfig(1, 0, 0)
					p := &credentialConcurrencyPlugin{allowed: map[string]map[string]bool{}}
					for i, a := range auths {
						updated := a.Clone()
						updated.ProxyURL = []string{"direct", "http://selected.invalid:8123", "socks5://selected.invalid:8124"}[i]
						updated.Metadata["proxy_url"] = updated.ProxyURL
						updated.Metadata["codex_identity_namespace"] = fmt.Sprintf("identity-%d", i)
						if i < 2 {
							updated.Metadata["concurrency_limit"] = []int{3, 2}[i]
						}
						var err error
						auths[i], err = m.Update(ctx, updated)
						if err != nil {
							t.Fatal(err)
						}
						p.allowed[fmt.Sprintf("key-%d", i)] = map[string]bool{a.ID: true}
					}
					m.SetPluginScheduler(p)
					var mu sync.Mutex
					attempts := map[string]int{}
					attempt := func(a *Auth, o coreexecutor.Options) error {
						mu.Lock()
						defer mu.Unlock()
						key, _ := o.Metadata[coreexecutor.CallerScopeMetadataKey].(string)
						if !p.allowed[key][a.ID] {
							t.Errorf("retry escaped the caller allow-list")
						}
						for i, want := range auths {
							if a.ID == want.ID {
								if a.ProxyURL != want.ProxyURL || a.Metadata["codex_identity_namespace"] != want.Metadata["codex_identity_namespace"] {
									t.Errorf("retry changed credential route/identity")
								}
								if n, limit := m.CredentialConcurrencySnapshot(a.ID); n != 1 || limit != []int{3, 2, 10}[i] {
									t.Errorf("attempt lease=%d/%d", n, limit)
								}
							}
						}
						attempts[a.ID]++
						if attempts[a.ID] == 1 {
							return errors.New(message)
						}
						return nil
					}
					e.execute = func(_ context.Context, a *Auth, _ coreexecutor.Request, o coreexecutor.Options) (coreexecutor.Response, error) {
						if err := attempt(a, o); err != nil {
							return coreexecutor.Response{}, err
						}
						return coreexecutor.Response{Payload: []byte("ok")}, nil
					}
					e.stream = func(_ context.Context, a *Auth, _ coreexecutor.Request, o coreexecutor.Options) (*coreexecutor.StreamResult, error) {
						if err := attempt(a, o); err != nil {
							return nil, err
						}
						chunks := make(chan coreexecutor.StreamChunk, 1)
						chunks <- coreexecutor.StreamChunk{Payload: []byte("ok")}
						close(chunks)
						return &coreexecutor.StreamResult{Chunks: chunks}, nil
					}
					for i, a := range auths {
						opts := credentialConcurrencyOptions(fmt.Sprintf("key-%d", i))
						opts.Metadata[coreexecutor.PinnedAuthMetadataKey] = a.ID
						request := coreexecutor.Request{Model: models[0]}
						if stream {
							result, err := m.ExecuteStream(ctx, []string{e.Identifier()}, request, opts)
							if err != nil {
								t.Fatal(err)
							}
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									t.Fatal(chunk.Err)
								}
							}
						} else {
							if _, err := m.Execute(ctx, []string{e.Identifier()}, request, opts); err != nil {
								t.Fatal(err)
							}
						}
						synctest.Wait()
						if attempts[a.ID] != 2 {
							t.Fatalf("attempts=%d,want2", attempts[a.ID])
						}
						assertNoCooldown(t, m, a.ID, models[0])
						for j, other := range auths {
							requireCredentialConcurrencySnapshot(t, m, other.ID, 0, []int{3, 2, 10}[j])
						}
					}
					if p.callCount() < 6 {
						t.Fatal("retry bypassed plugin selection")
					}
					for _, a := range auths {
						registry.GetGlobalRegistry().UnregisterClient(a.ID)
					}
				})
			})
		}
	}
}
