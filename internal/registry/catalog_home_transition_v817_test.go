package registry

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
)

func TestV817HomeTransitionFencesGeneralAndDevinCatalogPublication(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprintf("local=%t", local), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				previousGeneral, previousCodex, previousDevin := generalCatalogUpdater, codexCatalogUpdater, devinCatalogUpdater
				catalogRuntime.Lock()
				previousContext, previousLocal := catalogRuntime.ctx, catalogRuntime.local
				catalogRuntime.Unlock()
				ctx, cancel := context.WithCancel(context.Background())
				defer func() {
					cancel()
					synctest.Wait()
					generalCatalogUpdater, codexCatalogUpdater, devinCatalogUpdater = previousGeneral, previousCodex, previousDevin
					catalogRuntime.Lock()
					catalogRuntime.ctx, catalogRuntime.local = previousContext, previousLocal
					catalogRuntime.Unlock()
				}()
				release := make(chan struct{})
				fetches := make(chan string, 8)
				published := make(chan string, 8)
				newUpdater := func(wait bool) *catalogUpdater {
					return &catalogUpdater{
						fetch: func(_ context.Context, source string) ([]byte, error) {
							fetches <- source
							if wait {
								// Simulate a file read that finishes after cancellation.
								<-release
							}
							return []byte(source), nil
						},
						publish: func(data []byte) ([]string, error) {
							published <- string(data)
							return nil, nil
						},
					}
				}
				generalCatalogUpdater, codexCatalogUpdater, devinCatalogUpdater = newUpdater(true), newUpdater(false), newUpdater(true)
				SetLocalModelCatalogs(local)
				old := CatalogSources{Catalog: "https://example.invalid/old-general", CodexCatalog: "https://example.invalid/old-codex", DevinCatalog: "https://example.invalid/old-devin"}
				StartModelCatalogUpdaters(ctx, old, false)
				synctest.Wait()
				if len(fetches) != 3 || len(published) != 1 || <-published != old.CodexCatalog {
					t.Fatal("initial catalog reads did not reach the controlled boundary")
				}
				for len(fetches) > 0 {
					<-fetches
				}
				next := CatalogSources{Catalog: "https://example.invalid/new-general", CodexCatalog: "https://example.invalid/new-codex", DevinCatalog: "https://example.invalid/new-devin"}
				UpdateModelCatalogSources(next, true)
				synctest.Wait()
				if len(fetches) != 1 || <-fetches != next.CodexCatalog || len(published) != 1 || <-published != next.CodexCatalog {
					t.Fatal("Home must keep only the local Codex template reader")
				}
				for name, updater := range map[string]*catalogUpdater{"general": generalCatalogUpdater, "devin": devinCatalogUpdater} {
					updater.mu.Lock()
					source := updater.source
					updater.mu.Unlock()
					if source != "disabled" {
						t.Errorf("Home %s source=%q", name, source)
					}
				}
				close(release)
				synctest.Wait()
				if len(fetches) != 0 || len(published) != 0 {
					t.Fatal("a canceled pre-Home catalog read published or triggered a new fetch")
				}
				UpdateModelCatalogSources(next, false)
				synctest.Wait()
				if len(fetches) != 2 || len(published) != 2 {
					t.Fatal("leaving Home did not resume explicit general and Devin readers exactly once")
				}
				seen := map[string]bool{<-published: true, <-published: true}
				if !seen[next.Catalog] || !seen[next.DevinCatalog] {
					t.Fatalf("resumed the wrong catalog sources: %v", seen)
				}
			})
		})
	}
}
