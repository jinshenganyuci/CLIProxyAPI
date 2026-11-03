package helps

import (
	"bytes"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestCodexDocStagesPreservePendingMutationsAndPatchHistory(t *testing.T) {
	raw := []byte(`{"input":[{"type":"custom_tool_call","id":"tool_1","call_id":"call_1","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"},{"type":"custom_tool_call_output","id":"result_1","call_id":"call_1","output":"ok"}],"sequence":9007199254740993}`)
	original := bytes.Clone(raw)
	doc := ojson.NewDoc(raw)
	if errSet := doc.SetRaw("tools", `[{"type":"custom","name":"apply_patch","format":{"type":"grammar","definition":"g"}},{"type":"function","name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string","pattern":"^[^\\0]*$"}}}}]`); errSet != nil {
		t.Fatal(errSet)
	}
	if errSet := doc.Set("prompt_cache_key", "pending-session"); errSet != nil {
		t.Fatal(errSet)
	}
	if !NormalizeCodexToolSchemasDoc(doc) {
		t.Fatal("schema normalization missed a pending tool declaration")
	}
	if !SanitizeCodexInputItemIDsDoc(doc) {
		t.Fatal("input ID normalization did not mutate the document")
	}
	if errNormalize := NormalizeApplyPatchResponsesRequestDoc(doc); errNormalize != nil {
		t.Fatal(errNormalize)
	}
	out := doc.Bytes()
	if ojson.GetBytes(out, "tools.1.parameters.properties.path.pattern").Exists() || ojson.GetBytes(out, "tools.0.format").Exists() {
		t.Fatalf("later normalization restored deleted schema fields: %s", out)
	}
	if ojson.GetBytes(out, "input.0.type").String() != "function_call" || ojson.GetBytes(out, "input.0.id").String() != "ctc_tool_1" || ojson.GetBytes(out, "input.1.type").String() != "function_call_output" || ojson.GetBytes(out, "input.1.id").String() != "ctco_result_1" {
		t.Fatalf("patch conversion lost normalized call identities: %s", out)
	}
	if ojson.Get(ojson.GetBytes(out, "input.0.arguments").String(), "input").String() != "*** Begin Patch\n*** End Patch" || ojson.GetBytes(out, "input.1.output").String() != "ok" {
		t.Fatalf("patch input/output content changed: %s", out)
	}
	if ojson.GetBytes(out, "sequence").Raw != "9007199254740993" || ojson.GetBytes(out, "prompt_cache_key").String() != "pending-session" || !bytes.Equal(raw, original) {
		t.Fatal("document stages changed unrelated metadata or the caller payload")
	}
}
