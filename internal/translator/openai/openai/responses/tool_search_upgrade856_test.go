package responses

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestV856ToolSearchReplayKeepsExactArgumentsAndMedia(t *testing.T) {
	const args = `{"query":"<calendar>&","limit":9007199254740993}`
	request := []byte(fmt.Sprintf(`{"model":"fixture","tools":[{"type":"tool_search","execution":"client"}],"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"keep authority"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"search"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]},{"type":"tool_search_call","execution":"client","call_id":"call_search","arguments":%s},{"type":"tool_search_output","execution":"client","call_id":"call_search","tools":[{"type":"namespace","name":"calendar","tools":[{"type":"function","name":"list","parameters":{"type":"object","properties":{"n":{"type":"integer","const":9007199254740993}}}}]}]}]}`, args))
	before := bytes.Clone(request)
	out, errConvert := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("fixture", request, false)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	if !bytes.Equal(request, before) {
		t.Fatal("tool-search translation changed the caller request")
	}
	if got := ojson.GetBytes(out, `messages.#(role=="assistant").tool_calls.0.function.arguments`).String(); got != args {
		t.Fatalf("search arguments = %s, want %s", got, args)
	}
	if ojson.GetBytes(out, "messages.0.role").String() != "developer" || ojson.GetBytes(out, `messages.#(role=="user").content.1.image_url.url`).String() != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("search bridge changed developer authority or user image: %s", out)
	}
	if got := ojson.GetBytes(out, `tools.#(function.name=="calendar__list").function.parameters.properties.n.const`).Raw; got != "9007199254740993" {
		t.Fatalf("loaded tool schema lost precision: %s", out)
	}
	if got := ojson.GetBytes(out, `messages.#(role=="tool").tool_call_id`).String(); got != "call_search" {
		t.Fatalf("search output lost call identity: %s", out)
	}
}

func TestV856ToolSearchStreamRetainsRequestScopedIndex(t *testing.T) {
	for i := range 16 {
		t.Run(fmt.Sprintf("request_%02d", i), func(t *testing.T) {
			t.Parallel()
			namespace := fmt.Sprintf("calendar_%02d", i)
			request := []byte(fmt.Sprintf(`{"model":"fixture","tools":[{"type":"function","name":"tool_search"},{"type":"tool_search","execution":"client"}],"input":[{"type":"tool_search_output","execution":"client","call_id":"previous","tools":[{"type":"namespace","name":%q,"tools":[{"type":"function","name":"list","parameters":{"type":"object"}}]}]}]}`, namespace))
			var state any
			ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "fixture", request, nil, []byte(`data: {"id":"shared","model":"fixture","created":1,"choices":[{"index":0,"delta":{"role":"assistant"}}]}`), &state)
			// A transport can reuse its input buffer after the first translated frame.
			for j := range request {
				request[j] = 'x'
			}
			frame := []byte(fmt.Sprintf(`data: {"id":"shared","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_search","function":{"name":"tool_search_1","arguments":"{\"query\":\"<q>&\",\"n\":9007199254740993}"}},{"index":1,"id":"call_loaded","function":{"name":%q,"arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, namespace+"__list"))
			emitted := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "fixture", request, nil, frame, &state)
			emitted = append(emitted, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "fixture", request, nil, []byte("data: [DONE]"), &state)...)
			var completed ojson.Result
			for _, chunk := range emitted {
				event, data := parseOpenAIResponsesSSEEvent(t, chunk)
				if strings.HasPrefix(event, "response.function_call_arguments") && strings.Contains(data.Get("item_id").String(), "call_search") {
					t.Fatalf("bridged search leaked function argument events: %s", chunk)
				}
				if event == "response.completed" {
					completed = data.Get("response")
				}
			}
			search := completed.Get(`output.#(call_id=="call_search")`)
			if search.Get("type").String() != "tool_search_call" || search.Get("arguments.n").Raw != "9007199254740993" || search.Get("arguments.query").String() != "<q>&" {
				t.Fatalf("search identity or exact arguments changed after input reuse: %s", completed.Raw)
			}
			loaded := completed.Get(`output.#(call_id=="call_loaded")`)
			if loaded.Get("name").String() != "list" || loaded.Get("namespace").String() != namespace {
				t.Fatalf("loaded tool acquired another request's identity: %s", completed.Raw)
			}
		})
	}
}
