package registry

import "testing"

func TestV847WebSearchIndexHonorsRegistrationEpochFence(t *testing.T) {
	reg := newTestModelRegistry()
	reg.RegisterClient("selected", "codex", []*ModelInfo{{ID: "shared", SupportsWebSearch: true}})
	stale := reg.ClientRegistrationEpoch("selected")
	reg.RegisterClient("other", "gemini", []*ModelInfo{{ID: "shared", SupportsWebSearch: true}})
	reg.UnregisterClient("selected")
	reg.RegisterClient("selected", "codex", []*ModelInfo{{ID: "shared", SupportsWebSearch: false}})
	if reg.ApplyClientModelCapabilities("selected", stale, func(_ string, info *ModelInfo) { info.SupportsWebSearch = true }) {
		t.Fatal("stale capabilities crossed registration fence")
	}
	if reg.RegisterClientIfEpochCurrent("selected", "codex", stale, []*ModelInfo{{ID: "stale-only", SupportsWebSearch: true}}, nil) {
		t.Fatal("stale model publication crossed registration fence")
	}
	assertWebSearchIndexMatchesScan(t, reg, "stale publication rejected")
	if info := reg.GetModelInfo("shared", "codex"); info == nil || info.SupportsWebSearch {
		t.Fatal("other provider capability contaminated selected provider")
	}
	current := reg.ClientRegistrationEpoch("selected")
	if !reg.ApplyClientModelCapabilities("selected", current, func(_ string, info *ModelInfo) { info.SupportsWebSearch = true }) {
		t.Fatal("current capability mutation rejected")
	}
	reg.UnregisterClient("other")
	assertPublishedWebSearchSupport(t, reg, "shared", "codex", true)
	if !reg.ApplyClientModelCapabilities("selected", current, func(_ string, info *ModelInfo) { info.SupportsWebSearch = false }) {
		t.Fatal("current capability removal rejected")
	}
	assertPublishedWebSearchSupport(t, reg, "shared", "codex", false)
	assertWebSearchIndexMatchesScan(t, reg, "current mutations published")
}
