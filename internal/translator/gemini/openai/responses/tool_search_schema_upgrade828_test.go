package responses

import (
	"context"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGeminiToolSearchSchemaAndIdentityRoundTripUpgrade828(t *testing.T) {
	model := "gemini-tool-search-schema-upgrade828"
	registerTestWebSearchModel(t, "tool-search-schema-upgrade828", "gemini", model, true)
	request := []byte(`{
		"tools":[
			{"type":"function","name":"tool_search","description":"direct-exact","parameters":{"type":"object"}},
			{"type":"function","name":"tool.search","description":"direct-dotted","parameters":{"type":"object"}},
			{"type":"tool_search","execution":"client","description":"discovery"},
			{"type":"web_search","filters":{"allowed_domains":["example.com"]}},
			{"type":"function","name":"hidden","defer_loading":true,"parameters":{"type":"object"}}
		],
		"input":[
			{"type":"message","role":"user","content":"Find a rendering tool"},
			{"type":"tool_search_call","execution":"client","call_id":"discover-1","arguments":{"query":"<render>&"}},
			{"type":"tool_search_output","execution":"client","call_id":"discover-1","tools":[
				{"type":"namespace","name":"ns.tools","tools":[{"type":"function","name":"render","description":"discovered-render","defer_loading":true,
				"parameters":{"type":"object","properties":{"payload":{"anyOf":[
					{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]},
					{"type":"object","properties":{"audio":{"type":"string"},"title":{"type":"string"},"default":{"type":"string"}},"required":["audio"]}
				]}}}}]}
			]},
			{"type":"function_call","namespace":"ns.tools","name":"render","call_id":"render-1","arguments":"{\"payload\":{\"audio\":\"<audio>&\",\"title\":\"literal-title\",\"default\":\"literal-default\"}}"},
			{"type":"function_call_output","call_id":"render-1","output":"rendered"}
		]
	}`)
	converted, err := ConvertOpenAIResponsesRequestToGemini(model, request, true)
	if err != nil {
		t.Fatal(err)
	}
	root := gjson.ParseBytes(converted)
	if root.Get("tools.0.googleSearch.includedDomains.0").String() != "example.com" {
		t.Fatalf("native web search was replaced by client discovery: %s", converted)
	}
	names := map[string]string{}
	unique := map[string]bool{}
	for _, tool := range root.Get("tools.1.functionDeclarations").Array() {
		name := tool.Get("name").String()
		if name == "" || unique[name] || name == "hidden" {
			t.Fatalf("duplicate/empty name or undiscovered tool leaked: %s", converted)
		}
		unique[name] = true
		names[tool.Get("description").String()] = name
		if tool.Get("description").String() == "discovered-render" {
			branches := tool.Get("parametersJsonSchema.properties.payload.anyOf").Array()
			if len(branches) != 2 || branches[0].Get("properties.text.type").String() != "string" || branches[1].Get("properties.audio.type").String() != "string" || branches[1].Get("properties.default.type").String() != "string" {
				t.Fatalf("discovered non-first schema branch was lost: %s", tool.Raw)
			}
		}
	}
	if len(unique) != 4 || names["direct-exact"] != "tool_search" || names["discovery"] == "tool_search" || names["discovery"] == names["direct-dotted"] {
		t.Fatalf("client search and same-name user tools collided: %+v", names)
	}
	contents := root.Get("contents").Array()
	if len(contents) != 5 || contents[1].Get("parts.0.functionCall.name").String() != names["discovery"] || contents[2].Get("parts.0.functionResponse.name").String() != names["discovery"] {
		t.Fatalf("discovery history was routed to a user function: %s", root.Get("contents").Raw)
	}
	if contents[3].Get("parts.0.functionCall.name").String() != names["discovered-render"] || contents[4].Get("parts.0.functionResponse.name").String() != names["discovered-render"] {
		t.Fatalf("namespaced tool call/result identity diverged: %s", root.Get("contents").Raw)
	}
	args := contents[3].Get("parts.0.functionCall.args.payload")
	if args.Get("audio").String() != "<audio>&" || args.Get("title").String() != "literal-title" || args.Get("default").String() != "literal-default" {
		t.Fatalf("schema sanitization rewrote conversation arguments: %s", args.Raw)
	}
	for _, tc := range []struct{ description, typ, name, namespace string }{
		{"discovery", "tool_search_call", "", ""},
		{"direct-exact", "function_call", "tool_search", ""},
		{"direct-dotted", "function_call", "tool.search", ""},
		{"discovered-render", "function_call", "render", "ns.tools"},
	} {
		t.Run(tc.description, func(t *testing.T) {
			upstream := []byte(fmt.Sprintf(`{"responseId":"roundtrip-828","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call-native-828","name":%q,"args":{"query":"<render>&"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":3,"thoughtsTokenCount":2,"totalTokenCount":16}}`, names[tc.description]))
			var state any
			var completed, done gjson.Result
			var addedID string
			for _, chunk := range [][]byte{append([]byte("data: "), upstream...), []byte("data: [DONE]")} {
				for _, event := range ConvertGeminiResponseToOpenAIResponses(context.Background(), model, request, converted, chunk, &state) {
					typ, data := parseSSEEvent(t, event)
					switch typ {
					case "response.output_item.added":
						addedID = data.Get("item.id").String()
					case "response.output_item.done":
						done = data.Get("item")
					case "response.completed":
						completed = data.Get("response")
					}
				}
			}
			nonstream := gjson.ParseBytes(ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), model, request, converted, upstream, nil))
			for _, item := range []gjson.Result{done, completed.Get("output.0"), nonstream.Get("output.0")} {
				if item.Get("type").String() != tc.typ || item.Get("name").String() != tc.name || item.Get("namespace").String() != tc.namespace || item.Get("status").String() != "completed" {
					t.Fatalf("stream/nonstream identity changed: %s", item.Raw)
				}
				if tc.typ == "tool_search_call" && (item.Get("call_id").String() != "call-native-828" || item.Get("arguments.query").String() != "<render>&") {
					t.Fatalf("native discovery call id or object arguments lost: %s", item.Raw)
				}
			}
			if addedID == "" || addedID != done.Get("id").String() || addedID != completed.Get("output.0.id").String() {
				t.Fatal("output item ID changed during the streaming lifecycle")
			}
			if completed.Get("usage.input_tokens").Int() != 11 || completed.Get("usage.output_tokens").Int() != 5 || nonstream.Get("usage.output_tokens").Int() != 5 {
				t.Fatalf("client usage lost thinking tokens: stream=%s nonstream=%s", completed.Get("usage").Raw, nonstream.Get("usage").Raw)
			}
		})
	}
}
