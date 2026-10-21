package responses

import (
	"bytes"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/tidwall/gjson"
)

// Check backing addresses instead of GC timing or heap thresholds: a short
// substring can retain the whole request even when its visible text is small.
func assertNoRequestBacking837(t *testing.T, root gjson.Result, snapshot any) {
	t.Helper()
	start := uintptr(unsafe.Pointer(unsafe.StringData(root.Raw)))
	end := start + uintptr(len(root.Raw))
	var walk func(reflect.Value, string)
	walk = func(v reflect.Value, path string) {
		switch v.Kind() {
		case reflect.String:
			if v.Len() > 0 {
				p := uintptr(unsafe.Pointer(unsafe.StringData(v.String())))
				if p >= start && p < end {
					t.Errorf("snapshot %s retains the complete request backing storage", path)
				}
			}
		case reflect.Interface, reflect.Pointer:
			if !v.IsNil() {
				walk(v.Elem(), path)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i), path+"."+v.Type().Field(i).Name)
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				walk(iter.Key(), path+".key")
				walk(iter.Value(), path+".value")
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), path+"[]")
			}
		}
	}
	walk(reflect.ValueOf(snapshot), "snapshot")
	runtime.KeepAlive(root)
}

func TestUpgrade837ResponsesInterleavedEchoSnapshot(t *testing.T) {
	var states [2]any
	requests := [][]byte{
		[]byte(`{"model":"first","metadata":{"owner":"first"},"tools":[{"type":"namespace","name":"first","tools":[{"type":"function","name":"read"}]}]}`),
		[]byte(`{"model":"second","metadata":{"owner":"second"},"tools":[{"type":"namespace","name":"second","tools":[{"type":"function","name":"read"}]}]}`),
	}
	for i := range states {
		ConvertOpenAIChatCompletionsResponseToOpenAIResponses(t.Context(), "", requests[i], []byte(`{"model":"translated"}`), []byte(`data: {"id":"snapshot","choices":[{"delta":{"content":"ready"}}]}`), &states[i])
		copy(requests[i], bytes.Repeat([]byte("!"), len(requests[i])))
	}
	for i, owner := range []string{"first", "second"} {
		chunk := []byte(fmt.Sprintf(`data: {"id":"snapshot","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"%s__read","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, owner))
		out := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(t.Context(), "", requests[1-i], nil, chunk, &states[i])
		out = append(out, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(t.Context(), "", nil, nil, []byte("data: [DONE]"), &states[i])...)
		var completed gjson.Result
		for _, block := range out {
			for _, line := range bytes.Split(block, []byte("\n")) {
				p := gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: ")))
				if p.Get("type").String() == "response.completed" {
					completed = p.Get("response")
				}
			}
		}
		if completed.Get("model").String() != owner || completed.Get("metadata.owner").String() != owner {
			t.Fatalf("stream %d echo snapshot changed: %s", i, completed.Raw)
		}
		calls := completed.Get(`output.#(type=="function_call")#`).Array()
		if len(calls) != 1 || calls[0].Get("name").String() != "read" || calls[0].Get("namespace").String() != owner {
			t.Fatalf("stream %d tool identity changed: %s", i, completed.Raw)
		}
	}
}

func TestUpgrade837ResponsesSnapshotsDoNotPinInput(t *testing.T) {
	root := gjson.Parse(`{"model":"client-model","instructions":"short instructions","previous_response_id":"previous","prompt_cache_key":"key","safety_identifier":"safety","service_tier":"priority","truncation":"auto","reasoning":{"effort":"high"},"text":{"format":{"type":"text"}},"tool_choice":"auto","user":"user","metadata":{"nested":[{"tag":"value"}]},"tools":[{"type":"namespace","name":"editor","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}],"input":[{"role":"user","content":"` + strings.Repeat("private-history-", 8192) + `"},{"type":"additional_tools","tools":[{"type":"custom","name":"patch","format":{"type":"text"}}]}]}`)
	t.Run("echo", func(t *testing.T) {
		assertNoRequestBacking837(t, root, extractResponsesRequestEcho(root))
	})
	t.Run("tool_index", func(t *testing.T) {
		idx := newResponsesToolIndex(root)
		assertNoRequestBacking837(t, root, idx)
		if got := idx.applyIdentity([]byte(`{}`), "editor__read", ""); gjson.GetBytes(got, "name").String() != "read" || gjson.GetBytes(got, "namespace").String() != "editor" {
			t.Fatalf("namespace identity changed: %s", got)
		}
		if _, ok := idx.custom["patch"]; !ok || len(idx.chatTools()) != 2 {
			t.Fatal("additional_tools declarations were lost")
		}
	})
}
