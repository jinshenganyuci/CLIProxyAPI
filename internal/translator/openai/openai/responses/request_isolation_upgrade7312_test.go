package responses

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesIndexUpgrade7312ParallelRequestIsolation(t *testing.T) {
	const count = 32
	ready := make(chan struct{}, count)
	proceed := make(chan struct{})
	errors := make(chan string, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			namespace := fmt.Sprintf("request_%02d", i)
			kind := "function"
			if i%2 == 1 {
				kind = "custom"
			}
			original := []byte(fmt.Sprintf(`{"model":"test","instructions":%q,"tools":[{"type":"namespace","name":%q,"tools":[{"type":%q,"name":"run"}]}],"input":[]}`, namespace, namespace, kind))
			before := bytes.Clone(original)
			translated := []byte(`{"model":"test","tools":[{"type":"function","name":"foreign"}]}`)
			var state any
			start := []byte(`data: {"id":"fixed","created":1,"choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
			ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test", original, translated, start, &state)
			ready <- struct{}{}
			<-proceed
			tool := []byte(`data: {"id":"fixed","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_shared","function":{"name":"run","arguments":"{\"input\":\"<hello>&\"}"}}]},"finish_reason":"tool_calls"}]}`)
			toolBefore := bytes.Clone(tool)
			events := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test", original, translated, tool, &state)
			events = append(events, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test", original, translated, []byte("data: [DONE]"), &state)...)
			if !bytes.Equal(original, before) || !bytes.Equal(tool, toolBefore) {
				errors <- "translator modified caller input"
				return
			}
			found := false
			for _, event := range events {
				for _, line := range bytes.Split(event, []byte("\n")) {
					if !bytes.HasPrefix(line, []byte("data: ")) {
						continue
					}
					root := gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: ")))
					if root.Get("type").String() != "response.completed" {
						continue
					}
					found = true
					output := root.Get("response.output.0")
					wantType := "function_call"
					if kind == "custom" {
						wantType = "custom_tool_call"
					}
					if output.Get("namespace").String() != namespace || output.Get("name").String() != "run" || output.Get("type").String() != wantType || root.Get("response.instructions").String() != namespace {
						errors <- fmt.Sprintf("request %d acquired foreign namespace/type: %s", i, root.Raw)
						return
					}
					if kind == "custom" && output.Get("input").String() != "<hello>&" {
						errors <- "custom arguments changed"
						return
					}
				}
			}
			if !found {
				errors <- "missing completion"
			}
		}(i)
	}
	for i := 0; i < count; i++ {
		<-ready
	}
	close(proceed)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}
