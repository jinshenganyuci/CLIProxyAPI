package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpgrade804MetaSearchSanitizationPreservesPreviewAndBusinessSchema(t *testing.T) {
	body := []byte(`{"tools":[{"type":"web_search","search_content_types":["image"],"external_web_access":false},{"type":"web_search_preview","search_content_types":["text"]},{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"search_content_types":{"type":"array"}}}},{"type":"namespace","name":"research","tools":[{"type":"web_search","search_content_types":null,"filters":{"allowed_domains":["example.test"]}},{"type":"web_search_preview","search_content_types":["image"]},{"type":"function","name":"web_search","parameters":{"search_content_types":"business value"}}]}],"input":[{"type":"function_call","arguments":"{\"search_content_types\":[\"business value\"]}"}]}`)
	out := SanitizeMetaWebSearchTools(body)
	for _, path := range []string{"tools.0.search_content_types", "tools.3.tools.0.search_content_types"} {
		if gjson.GetBytes(out, path).Exists() {
			t.Fatalf("unsupported field %s survived: %s", path, out)
		}
	}
	for _, path := range []string{"tools.0.external_web_access", "tools.1", "tools.2", "tools.3.tools.0.filters", "tools.3.tools.1", "tools.3.tools.2", "input"} {
		if gjson.GetBytes(out, path).Raw != gjson.GetBytes(body, path).Raw {
			t.Fatalf("supported or business field %s changed: %s", path, out)
		}
	}
	if !bytes.Equal(out, SanitizeMetaWebSearchTools(out)) {
		t.Fatal("Meta search sanitization is not idempotent")
	}
}
