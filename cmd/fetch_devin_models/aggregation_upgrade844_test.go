package main

import (
	"reflect"
	"testing"
)

func TestUpgrade844LightningAggregationKeepsTierIdentity(t *testing.T) {
	raw := []rawDevinModel{
		{UID: "probe-swe-2", Label: "Probe SWE-2", ContextLength: 1000},
		{UID: "probe-swe-2-low-lightning", Label: "Probe SWE-2 Low Lightning", ContextLength: 2000},
		{UID: "probe-swe-2-high-lightning", Label: "Probe SWE-2 High Lightning", ContextLength: 3000, Multimodal: true},
	}
	models := aggregateModels(raw)
	if len(models) != 2 {
		t.Fatalf("aggregated model count = %d, want separate base and Lightning", len(models))
	}
	byID := map[string]devinModelJSON{}
	for _, model := range models {
		byID[model.ID] = model
	}
	base, lightning := byID["probe-swe-2"], byID["probe-swe-2-lightning"]
	if base.DisplayName != "Probe SWE-2" || base.ContextLength != 1000 || lightning.DisplayName != "Probe SWE-2 Lightning" || lightning.ContextLength != 3000 {
		t.Fatal("aggregation mixed tier capabilities or display names")
	}
	if lightning.Thinking == nil || !reflect.DeepEqual(lightning.Thinking.Levels, []string{"low", "high"}) {
		t.Fatal("Lightning effort levels were lost or mixed with the base model")
	}
}
