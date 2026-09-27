package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestDevinV8RawExportAndAggregatedReload(t *testing.T) {
	input := []rawDevinModel{
		{UID: "private-v8-high-priority", Label: "Private V8 High Thinking Fast", VendorID: 2, ContextLength: 1000},
		{UID: "private-v8-low-fast", Label: "Private V8 Low Fast", VendorID: 2, ContextLength: 2000, Multimodal: true},
		{UID: "private-v8", Label: "Canonical Private V8", VendorID: 3, ContextLength: 1500},
		{UID: "swe-1-6-slow", Label: "SWE-1.6 Slow", VendorID: 1, ContextLength: 200000},
	}
	raw := formatRawModels(input)
	if len(raw) != len(input) {
		t.Fatalf("raw export collapsed upstream UIDs: %d", len(raw))
	}
	for i, model := range raw {
		if model.ID != input[i].UID {
			t.Fatalf("raw UID changed: got %s, want %s", model.ID, input[i].UID)
		}
	}
	aggregated := aggregateModels(input)
	if len(aggregated) != 2 {
		t.Fatalf("aggregate = %d, want custom base and independent Slow", len(aggregated))
	}
	first := aggregated[0]
	if first.ID != "private-v8" || first.DisplayName != "Canonical Private V8" || first.OwnedBy != "anthropic" || first.ContextLength != 2000 {
		t.Fatalf("canonical metadata precedence/max context wrong: %#v", first)
	}
	if first.Thinking == nil || !reflect.DeepEqual(first.Thinking.Levels, []string{"low", "high"}) {
		t.Fatalf("fast/priority must not become reasoning levels: %#v", first.Thinking)
	}
	for _, tc := range []struct {
		name   string
		models []devinModelJSON
	}{
		{"raw", raw}, {"aggregate", aggregated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, errMarshal := json.Marshal(devinCatalogOutput{Devin: tc.models})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			reloaded, errValidate := registry.ValidateDevinModelsJSON(data)
			if errValidate != nil {
				t.Fatal(errValidate)
			}
			if len(reloaded) != 2 || reloaded[0].ID != "devin/private-v8" || reloaded[1].ID != "devin/swe-1-6-slow" {
				t.Fatalf("export/reload changed aggregate identity: %#v", reloaded)
			}
			if !reflect.DeepEqual(reloaded[0].Thinking.Levels, []string{"low", "high"}) || reloaded[0].OwnedBy != "anthropic" || reloaded[0].ContextLength != 2000 {
				t.Fatalf("export/reload changed model metadata: %#v", reloaded[0])
			}
		})
	}
}
