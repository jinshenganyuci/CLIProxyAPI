package interactions_test

import (
	"fmt"
	"testing"

	antigravity "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/interactions"
	gemini "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/interactions"
	"github.com/tidwall/gjson"
)

func TestBuiltinToolsUpgrade7311MixedFormsAndRestrictions(t *testing.T) {
	for _, target := range []struct {
		name, path string
		convert    func(string, []byte, bool) []byte
	}{
		{"gemini", "tools", gemini.ConvertInteractionsRequestToGemini},
		{"antigravity", "request.tools", antigravity.ConvertInteractionsRequestToAntigravity},
	} {
		for _, shape := range []string{"native-function", "interactions-function"} {
			for _, choice := range []string{"auto", "none", "specific"} {
				t.Run(target.name+"/"+shape+"/"+choice, func(t *testing.T) {
					fn := `{"functionDeclarations":[{"name":"lookup","parameters":{"type":"object","properties":{}}}]}`
					if shape == "interactions-function" {
						fn = `{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}`
					}
					toolChoice := fmt.Sprintf(`%q`, choice)
					if choice == "specific" {
						toolChoice = `{"type":"function","function":{"name":"lookup"}}`
					}
					input := []byte(`{"model":"gemini-3.5-flash","input":"hi","tool_choice":` + toolChoice + `,"tools":[` + fn + `,{"type":"url_context","url_context":{"max_urls":2}},{"type":"code_execution","code_execution":{"environment":"sandbox"}},{"type":"google_search","google_search":{"includedDomains":["allowed.example"]}}]}`)
					out := target.convert("gemini-3.5-flash", input, false)
					tools := gjson.GetBytes(out, target.path).Array()
					functions, builtins := 0, 0
					for _, tool := range tools {
						functions += len(tool.Get("functionDeclarations").Array())
						if tool.Get("type").Exists() {
							t.Errorf("typed builtin was not normalized: %s", tool.Raw)
						}
						if v := tool.Get("urlContext"); v.Exists() {
							builtins++
							if v.Get("max_urls").Int() != 2 {
								t.Errorf("URL option lost: %s", tool.Raw)
							}
						}
						if v := tool.Get("codeExecution"); v.Exists() {
							builtins++
							if v.Get("environment").String() != "sandbox" {
								t.Errorf("code option lost: %s", tool.Raw)
							}
						}
						if v := tool.Get("googleSearch"); v.Exists() {
							builtins++
							if v.Get("includedDomains.0").String() != "allowed.example" {
								t.Errorf("search domain restriction lost: %s", tool.Raw)
							}
						}
					}
					if choice == "auto" {
						if functions != 1 || builtins != 3 {
							t.Fatalf("function=%d builtin=%d: %s", functions, builtins, out)
						}
					} else if builtins != 0 {
						t.Fatalf("choice %s must not enable unrelated builtin tools: %s", choice, out)
					}
					if choice == "specific" && functions != 1 {
						t.Fatalf("chosen function missing: %s", out)
					}
				})
			}
		}
	}
}

func TestBuiltinToolsUpgrade7311WebSearchAliasPreservesOptions(t *testing.T) {
	for _, target := range []struct {
		name, path string
		convert    func(string, []byte, bool) []byte
	}{
		{"gemini", "tools", gemini.ConvertInteractionsRequestToGemini},
		{"antigravity", "request.tools", antigravity.ConvertInteractionsRequestToAntigravity},
	} {
		t.Run(target.name, func(t *testing.T) {
			out := target.convert("gemini-3.5-flash", []byte(`{"input":"hi","tools":[{"type":"web_search","web_search":{"includedDomains":["allowed.example"]}}]}`), false)
			if gjson.GetBytes(out, target.path+".0.googleSearch.includedDomains.0").String() != "allowed.example" {
				t.Fatalf("web_search alias lost options: %s", out)
			}
		})
	}
}

func TestBuiltinToolsUpgrade7311NativeSchemaNumbersStayExact(t *testing.T) {
	input := []byte(`{"input":"hi","tools":[{"functionDeclarations":[{"name":"lookup","parametersJsonSchema":{"type":"object","properties":{"limit":{"type":"integer","maximum":9223372036854775807}}}}]},{"type":"url_context"}]}`)
	out := gemini.ConvertInteractionsRequestToGemini("gemini-3.5-flash", input, false)
	if got := gjson.GetBytes(out, "tools.0.functionDeclarations.0.parametersJsonSchema.properties.limit.maximum").Raw; got != "9223372036854775807" {
		t.Fatalf("native schema limit changed: %s", out)
	}
}
