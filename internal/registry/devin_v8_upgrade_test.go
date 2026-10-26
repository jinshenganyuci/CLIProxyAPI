package registry

import (
	"os"
	"reflect"
	"slices"
	"testing"
)

func TestDevinV8AggregationPreservesCustomCatalogMetadata(t *testing.T) {
	payload := []byte(`{"devin":[
		{"id":"Private-Custom-low-fast","display_name":"Private Custom Low Fast","context_length":1000,"max_completion_tokens":100,"supportedInputModalities":["text"],"supportedOutputModalities":["text"],"supportedGenerationMethods":["countTokens"],"thinking":{"levels":["medium","priority"]}},
		{"id":"private-custom","display_name":"Canonical Private Custom","owned_by":"private","context_length":4000,"max_completion_tokens":400,"supportedInputModalities":["image"],"supportedOutputModalities":["audio"],"supportedGenerationMethods":["generateContent"]},
		{"id":"DEVIN/PRIVATE-CUSTOM-high-priority","display_name":"Private Custom High","context_length":2000,"max_completion_tokens":200},
		{"id":"private-unrelated","display_name":"Unrelated Custom","owned_by":"other","context_length":8192}
	]}`)
	models, errValidate := ValidateDevinModelsJSON(payload)
	if errValidate != nil {
		t.Fatal(errValidate)
	}
	if len(models) != 2 {
		t.Fatalf("aggregated catalog = %d, want two independent models", len(models))
	}
	model := models[0]
	if model.ID != "devin/private-custom" || model.DisplayName != "Canonical Private Custom" || model.OwnedBy != "private" {
		t.Fatalf("canonical custom identity lost: %#v", model)
	}
	if model.ContextLength != 4000 || model.InputTokenLimit != 4000 || model.MaxCompletionTokens != 400 || model.OutputTokenLimit != 400 {
		t.Fatalf("maximum token limits lost: %#v", model)
	}
	if model.Thinking == nil || !reflect.DeepEqual(model.Thinking.Levels, []string{"low", "medium", "high"}) {
		t.Fatalf("effort union or priority exclusion incorrect: %#v", model.Thinking)
	}
	for _, values := range []struct {
		got  []string
		want []string
	}{
		{model.SupportedInputModalities, []string{"text", "image"}},
		{model.SupportedOutputModalities, []string{"text", "audio"}},
		{model.SupportedGenerationMethods, []string{"countTokens", "generateContent"}},
	} {
		for _, want := range values.want {
			if !slices.Contains(values.got, want) {
				t.Fatalf("aggregated metadata %v missing %s", values.got, want)
			}
		}
	}
	if unrelated := models[1]; unrelated.ID != "devin/private-unrelated" || unrelated.OwnedBy != "other" || unrelated.ContextLength != 8192 {
		t.Fatalf("unrelated custom model changed: %#v", unrelated)
	}
}

func TestDevinV8DynamicCatalogAggregationAndReplacement(t *testing.T) {
	devinCatalogStore.mu.RLock()
	originalModels := cloneModelInfos(devinCatalogStore.models)
	originalRaw := append([]byte(nil), devinCatalogStore.rawJSON...)
	originalRevision := devinCatalogStore.revision
	devinCatalogStore.mu.RUnlock()
	t.Cleanup(func() {
		devinCatalogStore.mu.Lock()
		devinCatalogStore.models, devinCatalogStore.rawJSON, devinCatalogStore.revision = originalModels, originalRaw, originalRevision
		devinCatalogStore.mu.Unlock()
	})
	payload := []byte(`{"devin":[{"id":"private-one-low","thinking":{"levels":["medium"]}},{"id":"private-one-high"}]}`)
	if changed, errLoad := loadDevinModelsFromBytes(payload, "v8-raw-test"); errLoad != nil || !changed {
		t.Fatalf("load raw variants: changed=%v err=%v", changed, errLoad)
	}
	if models := GetDevinModels(); len(models) != 2 {
		t.Fatalf("expected one aggregated private model plus Slow builtin, got %d", len(models))
	}
	for _, id := range []string{"private-one", "DEVIN/PRIVATE-ONE-HIGH", "private-one-low-fast"} {
		model := LookupDevinModel(id)
		if model == nil || model.ID != "devin/private-one" || !reflect.DeepEqual(model.Thinking.Levels, []string{"low", "medium", "high"}) {
			t.Fatalf("variant metadata lookup %q did not resolve aggregate: %#v", id, model)
		}
		model.Thinking.Levels[0] = "mutated"
	}
	raw, revision := GetDevinModelsSnapshot()
	if string(raw) != string(payload) || revision != originalRevision+1 {
		t.Fatal("aggregation altered raw snapshot or failed to increment revision")
	}
	if changed, errLoad := loadDevinModelsFromBytes(payload, "v8-repeat-test"); errLoad != nil || changed {
		t.Fatalf("identical update should be unchanged: changed=%v err=%v", changed, errLoad)
	}
	if _, errLoad := loadDevinModelsFromBytes([]byte(`{"devin":[{"id":"private-two"}]}`), "v8-replacement-test"); errLoad != nil {
		t.Fatal(errLoad)
	}
	if LookupDevinModel("private-one") != nil || LookupDevinModel("private-two") == nil || LookupDevinModel("swe-1-6-slow") == nil {
		t.Fatal("valid dynamic replacement must replace prior custom entries while preserving Slow builtin")
	}
}

func TestDevinV8PreviousCatalogIDsHaveBaseMetadata(t *testing.T) {
	// Old catalog aliases remain valid when a user loads that catalog. The
	// current upstream catalog may retire models, so this compatibility test
	// deliberately uses the published v8.0.42 catalog rather than the live one.
	data, errRead := os.ReadFile("testdata/devin_models_v842.json")
	if errRead != nil {
		t.Fatal(errRead)
	}
	devinCatalogStore.mu.RLock()
	originalModels := cloneModelInfos(devinCatalogStore.models)
	originalRaw := append([]byte(nil), devinCatalogStore.rawJSON...)
	originalRevision := devinCatalogStore.revision
	devinCatalogStore.mu.RUnlock()
	t.Cleanup(func() {
		devinCatalogStore.mu.Lock()
		devinCatalogStore.models, devinCatalogStore.rawJSON, devinCatalogStore.revision = originalModels, originalRaw, originalRevision
		devinCatalogStore.mu.Unlock()
	})
	if _, errLoad := loadDevinModelsFromBytes(data, "published-v842-compatibility"); errLoad != nil {
		t.Fatal(errLoad)
	}

	previous := map[string]string{
		"devin/claude-opus-5-5":                       "devin/claude-opus-5-5",
		"devin/claude-fable-5-1":                      "devin/claude-fable-5-1",
		"devin/claude-sonnet-5":                       "devin/claude-sonnet-5",
		"devin/gemini-3-8-flash":                      "devin/gemini-3-8-flash",
		"devin/gpt-6-astra":                           "devin/gpt-6-astra",
		"devin/gpt-6-sol":                             "devin/gpt-6-sol",
		"devin/gpt-6-luna":                            "devin/gpt-6-luna",
		"devin/glm-5-2":                               "devin/glm-5-2",
		"devin/kimi-k3":                               "devin/kimi-k3",
		"devin/glm-5-3":                               "devin/glm-5-3",
		"devin/swe-1-7-lightning":                     "devin/swe-1-7-lightning",
		"devin/swe-2":                                 "devin/swe-2",
		"devin/claude-opus-4-7":                       "devin/claude-opus-4-7",
		"devin/claude-opus-4-8":                       "devin/claude-opus-4-8",
		"devin/claude-opus-4-8-low-fast":              "devin/claude-opus-4-8",
		"devin/claude-opus-4-8-medium-fast":           "devin/claude-opus-4-8",
		"devin/claude-opus-4-8-high-fast":             "devin/claude-opus-4-8",
		"devin/claude-opus-4-8-xhigh-fast":            "devin/claude-opus-4-8",
		"devin/claude-opus-4-8-max-fast":              "devin/claude-opus-4-8",
		"devin/claude-opus-5":                         "devin/claude-opus-5",
		"devin/claude-opus-5-low-fast":                "devin/claude-opus-5",
		"devin/claude-opus-5-medium-fast":             "devin/claude-opus-5",
		"devin/claude-opus-5-high-fast":               "devin/claude-opus-5",
		"devin/claude-opus-5-xhigh-fast":              "devin/claude-opus-5",
		"devin/claude-opus-5-max-fast":                "devin/claude-opus-5",
		"devin/claude-opus-5-5-low-fast":              "devin/claude-opus-5-5",
		"devin/claude-opus-5-5-medium-fast":           "devin/claude-opus-5-5",
		"devin/claude-opus-5-5-high-fast":             "devin/claude-opus-5-5",
		"devin/claude-opus-5-5-xhigh-fast":            "devin/claude-opus-5-5",
		"devin/claude-opus-5-5-max-fast":              "devin/claude-opus-5-5",
		"devin/claude-5-fable":                        "devin/claude-5-fable",
		"devin/gemini-3-5-flash":                      "devin/gemini-3-5-flash",
		"devin/gemini-3-6-flash":                      "devin/gemini-3-6-flash",
		"devin/gemini-3-7-flash":                      "devin/gemini-3-7-flash",
		"devin/gpt-5-6-sol":                           "devin/gpt-5-6-sol",
		"devin/gpt-5-6-sol-none":                      "devin/gpt-5-6-sol",
		"devin/gpt-5-6-sol-low":                       "devin/gpt-5-6-sol",
		"devin/gpt-5-6-sol-medium":                    "devin/gpt-5-6-sol",
		"devin/gpt-5-6-sol-high":                      "devin/gpt-5-6-sol",
		"devin/gpt-5-6-sol-xhigh":                     "devin/gpt-5-6-sol",
		"devin/gpt-5-6-sol-max":                       "devin/gpt-5-6-sol",
		"devin/gpt-5-6-terra":                         "devin/gpt-5-6-terra",
		"devin/gpt-5-6-terra-none":                    "devin/gpt-5-6-terra",
		"devin/gpt-5-6-terra-low":                     "devin/gpt-5-6-terra",
		"devin/gpt-5-6-terra-medium":                  "devin/gpt-5-6-terra",
		"devin/gpt-5-6-terra-high":                    "devin/gpt-5-6-terra",
		"devin/gpt-5-6-terra-xhigh":                   "devin/gpt-5-6-terra",
		"devin/gpt-5-6-terra-max":                     "devin/gpt-5-6-terra",
		"devin/gpt-5-6-luna":                          "devin/gpt-5-6-luna",
		"devin/gpt-5-6-luna-none":                     "devin/gpt-5-6-luna",
		"devin/gpt-5-6-luna-low":                      "devin/gpt-5-6-luna",
		"devin/gpt-5-6-luna-medium":                   "devin/gpt-5-6-luna",
		"devin/gpt-5-6-luna-high":                     "devin/gpt-5-6-luna",
		"devin/gpt-5-6-luna-xhigh":                    "devin/gpt-5-6-luna",
		"devin/gpt-5-6-luna-max":                      "devin/gpt-5-6-luna",
		"devin/gpt-6-astra-low":                       "devin/gpt-6-astra",
		"devin/gpt-6-astra-medium":                    "devin/gpt-6-astra",
		"devin/gpt-6-astra-high":                      "devin/gpt-6-astra",
		"devin/gpt-6-astra-xhigh":                     "devin/gpt-6-astra",
		"devin/gpt-6-astra-max":                       "devin/gpt-6-astra",
		"devin/gpt-6-sol-none":                        "devin/gpt-6-sol",
		"devin/gpt-6-sol-low":                         "devin/gpt-6-sol",
		"devin/gpt-6-sol-medium":                      "devin/gpt-6-sol",
		"devin/gpt-6-sol-high":                        "devin/gpt-6-sol",
		"devin/gpt-6-sol-xhigh":                       "devin/gpt-6-sol",
		"devin/gpt-6-sol-max":                         "devin/gpt-6-sol",
		"devin/gpt-6-luna-none":                       "devin/gpt-6-luna",
		"devin/gpt-6-luna-low":                        "devin/gpt-6-luna",
		"devin/gpt-6-luna-medium":                     "devin/gpt-6-luna",
		"devin/gpt-6-luna-high":                       "devin/gpt-6-luna",
		"devin/gpt-6-luna-xhigh":                      "devin/gpt-6-luna",
		"devin/gpt-6-luna-max":                        "devin/gpt-6-luna",
		"devin/glm-5-2-1m":                            "devin/glm-5-2-1m",
		"devin/glm-5-2-max-1m":                        "devin/glm-5-2-1m",
		"devin/glm-5-2-none-1m":                       "devin/glm-5-2-1m",
		"devin/grok-4-5":                              "devin/grok-4-5",
		"devin/grok-4-6":                              "devin/grok-4-6",
		"devin/grok-4-7":                              "devin/grok-4-7",
		"devin/inkling":                               "devin/inkling",
		"devin/glm-5-3-flash":                         "devin/glm-5-3-flash",
		"devin/deepseek-v4-flash":                     "devin/deepseek-v4-flash",
		"devin/deepseek-v4-1-flash":                   "devin/deepseek-v4-1-flash",
		"devin/swe-1-7":                               "devin/swe-1-7",
		"devin/claude-opus-4-6":                       "devin/claude-opus-4-6",
		"devin/claude-opus-4-6-thinking":              "devin/claude-opus-4-6",
		"devin/claude-opus-4-6-1m":                    "devin/claude-opus-4-6-1m",
		"devin/claude-opus-4-6-thinking-1m":           "devin/claude-opus-4-6-1m",
		"devin/gpt-5-4":                               "devin/gpt-5-4",
		"devin/gpt-5-5":                               "devin/gpt-5-5",
		"devin/gpt-5-5-none":                          "devin/gpt-5-5",
		"devin/gpt-5-5-low":                           "devin/gpt-5-5",
		"devin/gpt-5-5-medium":                        "devin/gpt-5-5",
		"devin/gpt-5-5-high":                          "devin/gpt-5-5",
		"devin/gpt-5-5-xhigh":                         "devin/gpt-5-5",
		"devin/gpt-5-4-none":                          "devin/gpt-5-4",
		"devin/gpt-5-4-low":                           "devin/gpt-5-4",
		"devin/gpt-5-4-medium":                        "devin/gpt-5-4",
		"devin/gpt-5-4-high":                          "devin/gpt-5-4",
		"devin/gpt-5-4-xhigh":                         "devin/gpt-5-4",
		"devin/gpt-5-4-mini":                          "devin/gpt-5-4-mini",
		"devin/claude-sonnet-4-6":                     "devin/claude-sonnet-4-6",
		"devin/claude-sonnet-4-6-thinking":            "devin/claude-sonnet-4-6",
		"devin/claude-sonnet-4-6-1m":                  "devin/claude-sonnet-4-6-1m",
		"devin/claude-sonnet-4-6-thinking-1m":         "devin/claude-sonnet-4-6-1m",
		"devin/model_gpt_5_2_low":                     "devin/model_gpt_5_2",
		"devin/model_gpt_5_2_medium":                  "devin/model_gpt_5_2",
		"devin/model_claude_4_5_opus":                 "devin/model_claude_4_5_opus",
		"devin/model_claude_4_5_opus_thinking":        "devin/model_claude_4_5_opus",
		"devin/model_private_11":                      "devin/model_private_11",
		"devin/model_private_2":                       "devin/model_private_2",
		"devin/model_private_3":                       "devin/model_private_3",
		"devin/model_chat_gpt_4_1_2025_04_14":         "devin/model_chat_gpt_4_1_2025_04_14",
		"devin/model_private_12":                      "devin/model_private_12",
		"devin/model_private_13":                      "devin/model_private_13",
		"devin/model_private_14":                      "devin/model_private_14",
		"devin/model_private_15":                      "devin/model_private_15",
		"devin/model_gpt_5_2_none":                    "devin/model_gpt_5_2",
		"devin/model_gpt_5_2_high":                    "devin/model_gpt_5_2",
		"devin/model_gpt_5_2_xhigh":                   "devin/model_gpt_5_2",
		"devin/gpt-5-3-codex":                         "devin/gpt-5-3-codex",
		"devin/gpt-5-3-codex-low":                     "devin/gpt-5-3-codex",
		"devin/gpt-5-3-codex-medium":                  "devin/gpt-5-3-codex",
		"devin/gpt-5-3-codex-high":                    "devin/gpt-5-3-codex",
		"devin/gpt-5-3-codex-xhigh":                   "devin/gpt-5-3-codex",
		"devin/kimi-k2-6":                             "devin/kimi-k2-6",
		"devin/kimi-k2-7":                             "devin/kimi-k2-7",
		"devin/nemotron-3-ultra":                      "devin/nemotron-3-ultra",
		"devin/swe-1-6":                               "devin/swe-1-6",
		"devin/swe-1-6-fast":                          "devin/swe-1-6",
		"devin/gemini-3-1-pro":                        "devin/gemini-3-1-pro",
		"devin/model_google_gemini_3_0_flash_minimal": "devin/model_google_gemini_3_0_flash",
		"devin/model_google_gemini_3_0_flash_low":     "devin/model_google_gemini_3_0_flash",
		"devin/model_google_gemini_3_0_flash_medium":  "devin/model_google_gemini_3_0_flash",
		"devin/model_google_gemini_3_0_flash_high":    "devin/model_google_gemini_3_0_flash",
		"devin/deepseek-v4-pro":                       "devin/deepseek-v4-pro",
	}
	for oldID, wantBase := range previous {
		t.Run(oldID, func(t *testing.T) {
			model := LookupDevinModel(oldID)
			if model == nil || model.ID != wantBase {
				t.Fatalf("previous ID %q: got %#v, want aggregate %q", oldID, model, wantBase)
			}
		})
	}
}
