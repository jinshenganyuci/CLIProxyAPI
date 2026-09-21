package util

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestArrayItemsUpgrade7311PreservesExampleData(t *testing.T) {
	input := `{"type":"object","properties":{"config":{"type":"object","default":{"items":[1,2],"type":"inventory"},"examples":[{"items":[3,4],"type":"inventory"}],"additionalProperties":false},"values":{"items":{"type":"string"}}}}`
	out := CleanJSONSchemaForGeminiJSONSchema(input)
	for _, path := range []string{"properties.config.default", "properties.config.examples.0"} {
		if gjson.Get(out, path+".items.#").Int() != 2 || gjson.Get(out, path+".type").String() != "inventory" {
			t.Errorf("example data mutated at %s: %s", path, out)
		}
	}
	if gjson.Get(out, "properties.values.type").String() != "array" {
		t.Fatalf("real array schema not normalized: %s", out)
	}
}

func TestArrayItemsUpgrade7311AnnotationNamesCanBeRealProperties(t *testing.T) {
	input := `{"type":"object","properties":{"default":{"items":{"type":"string"}},"examples":{"items":{"type":"integer"}},"items":{"type":"object","properties":{"items":{"items":{"type":"boolean"}}}}}}`
	out := CleanJSONSchemaForGeminiJSONSchema(input)
	for _, path := range []string{"properties.default", "properties.examples", "properties.items.properties.items"} {
		if gjson.Get(out, path+".type").String() != "array" {
			t.Errorf("real schema at %s skipped: %s", path, out)
		}
	}
}
