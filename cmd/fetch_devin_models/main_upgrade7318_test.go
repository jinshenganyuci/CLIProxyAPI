package main

import "testing"

func TestVendorNameUnknownGrok7318(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   uint64
		uid  string
		want string
	}{
		{name: "unknown Grok", id: 99, uid: "grok-4-6", want: "xai"},
		{name: "case-insensitive variant", id: 0, uid: "GROK-4-7-HIGH", want: "xai"},
		{name: "unknown non-Grok", id: 99, uid: "custom-private", want: "devin"},
		{name: "known vendor takes precedence", id: 2, uid: "grok-private-compat", want: "openai"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := vendorName(tc.id, tc.uid); got != tc.want {
				t.Fatalf("vendorName(%d, %q) = %q, want %q", tc.id, tc.uid, got, tc.want)
			}
		})
	}
}

func TestDevinGrokCatalogOwnerRawAndAggregated7318(t *testing.T) {
	input := []rawDevinModel{
		{UID: "grok-4-6", Label: "Grok 4.6", VendorID: 99, ContextLength: 500000, Multimodal: true},
		{UID: "private-model", Label: "Private model", VendorID: 99, ContextLength: 65536},
	}
	for _, tc := range []struct {
		name   string
		format func([]rawDevinModel) []devinModelJSON
	}{
		{name: "raw", format: formatRawModels},
		{name: "aggregated", format: aggregateModels},
	} {
		t.Run(tc.name, func(t *testing.T) {
			models := tc.format(input)
			if len(models) != 2 {
				t.Fatalf("model count = %d, want 2", len(models))
			}
			byID := make(map[string]devinModelJSON, len(models))
			for _, model := range models {
				byID[model.ID] = model
			}
			grok := byID["grok-4-6"]
			if grok.OwnedBy != "xai" || grok.ContextLength != 500000 || grok.MaxCompletionTokens != 64000 || len(grok.SupportedInputModalities) != 2 {
				t.Fatalf("Grok catalog metadata mismatch: %#v", grok)
			}
			private := byID["private-model"]
			if private.OwnedBy != "devin" || private.ContextLength != 65536 || private.DisplayName != "Private model" {
				t.Fatalf("custom model altered: %#v", private)
			}
		})
	}
}
