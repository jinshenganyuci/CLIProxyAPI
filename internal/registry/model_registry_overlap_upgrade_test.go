package registry

import (
	"testing"
	"time"
)

func TestQuotaSuspensionOverlapIsCountedOnlyForTheSameClientUpgrade(t *testing.T) {
	for _, sameClient := range []bool{false, true} {
		name := "distinct-unavailable-clients"
		if sameClient {
			name = "one-unavailable-and-one-healthy-client"
		}
		t.Run(name, func(t *testing.T) {
			r := newTestModelRegistry()
			model := &ModelInfo{ID: "registry-overlap-upgrade", OwnedBy: "openai", Type: "openai"}
			r.RegisterClient("a", "codex", []*ModelInfo{model})
			r.RegisterClient("b", "codex", []*ModelInfo{model})
			now := time.Now()
			suspendedID := "b"
			if sameClient {
				suspendedID = "a"
			}
			r.models[model.ID].QuotaExceededClients = map[string]*time.Time{"a": &now}
			r.models[model.ID].SuspendedClients = map[string]string{suspendedID: "credential_quota"}
			want := 0
			if sameClient {
				want = 1
			}
			if got := r.GetModelCount(model.ID); got != want {
				t.Fatalf("count=%d, want%d", got, want)
			}
			if got := len(r.GetAvailableModels("openai")); got != want {
				t.Fatalf("global catalog=%d, want%d", got, want)
			}
			if got := len(r.GetAvailableModelsByProvider("codex")); got != want {
				t.Fatalf("provider catalog=%d, want%d", got, want)
			}
		})
	}
}

func TestQuotaSuspensionOverlapDoesNotHideAnotherProviderUpgrade(t *testing.T) {
	r := newTestModelRegistry()
	model := &ModelInfo{ID: "registry-provider-overlap-upgrade", OwnedBy: "openai", Type: "openai"}
	r.RegisterClient("limited-codex", "codex", []*ModelInfo{model})
	r.RegisterClient("healthy-compat", "openai", []*ModelInfo{model})
	_, epoch := r.GetModelsAndEpochForClient("limited-codex")
	if !r.ApplyClientModelProjections("limited-codex", epoch, 1, []ClientModelProjection{{ModelID: model.ID, Suspended: true, SuspendReason: "credential_quota", QuotaExceeded: true}}) {
		t.Fatal("projection failed")
	}
	if r.GetModelCount(model.ID) != 1 || len(r.GetAvailableModelsByProvider("openai")) != 1 || len(r.GetAvailableModelsByProvider("codex")) != 0 || len(r.GetAvailableModels("openai")) != 1 {
		t.Fatal("quota overlap changed provider isolation or healthy catalog")
	}
}
