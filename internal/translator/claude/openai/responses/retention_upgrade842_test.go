package responses

import (
	"bytes"
	"github.com/tidwall/gjson"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

// Check backing addresses instead of GC timing or heap thresholds: a short
// substring can retain the whole request even when its visible text is small.
func assertNoRequestBacking842(t *testing.T, root gjson.Result, snapshot any) {
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

func TestUpgrade842ClaudeResponseSnapshotsRemainStreamLocal(t *testing.T) {
	requests := [][]byte{
		[]byte(`{"model":"first","metadata":{"owner":"first"},"tools":[{"type":"namespace","name":"editor","tools":[{"type":"function","name":"read"}]}]}`),
		[]byte(`{"model":"second","metadata":{"owner":"second"},"tools":[{"type":"function","name":"editor__read"}]}`),
	}
	var states [2]any
	chunks := claudeSSEFixture()
	for i := range states {
		ConvertClaudeResponseToOpenAIResponses(t.Context(), "", requests[i], nil, chunks[0], &states[i])
		copy(requests[i], bytes.Repeat([]byte("!"), len(requests[i])))
	}
	var completed [2]gjson.Result
	for _, chunk := range chunks[1:] {
		for i := range states {
			for _, out := range ConvertClaudeResponseToOpenAIResponses(t.Context(), "", requests[1-i], nil, chunk, &states[i]) {
				for _, line := range bytes.Split(out, []byte("\n")) {
					event := gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: ")))
					if event.Get("type").String() == "response.completed" {
						completed[i] = event.Get("response")
					}
				}
			}
		}
	}
	for i, owner := range []string{"first", "second"} {
		if completed[i].Get("model").String() != owner || completed[i].Get("metadata.owner").String() != owner {
			t.Fatalf("stream %d lost its echo snapshot: %s", i, completed[i].Raw)
		}
		call := completed[i].Get(`output.#(call_id=="toolu_1")`)
		wantName, wantNamespace := "read", "editor"
		if i == 1 {
			wantName, wantNamespace = "editor__read", ""
		}
		if call.Get("name").String() != wantName || call.Get("namespace").String() != wantNamespace || call.Get("arguments").String() != `{"path":"/tmp/a"}` {
			t.Fatalf("stream %d mixed namespace/flat identities: %s", i, call.Raw)
		}
	}
}

func TestUpgrade842ClaudeResponseSnapshotsDoNotPinInput(t *testing.T) {
	root := gjson.Parse(`{"model":"client-model","instructions":"small instruction","previous_response_id":"previous","prompt_cache_key":"key","safety_identifier":"safety","service_tier":"priority","truncation":"auto","reasoning":{"effort":"high"},"text":{"format":{"type":"text"}},"tool_choice":"auto","user":"user","metadata":{"nested":[{"tag":"value"}]},"tools":[{"type":"namespace","name":"editor","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]},{"type":"custom","name":"patch","format":{"type":"text"}}],"input":[{"role":"user","content":"` + strings.Repeat("unrelated-history-", 8192) + `"},{"type":"function_call","call_id":"old","name":"historic","arguments":"{}"}]}`)
	state := newClaudeToResponsesStateFromRoot(root, false)
	t.Run("echo", func(t *testing.T) { assertNoRequestBacking842(t, root, state.RequestEcho) })
	t.Run("tool_winners", func(t *testing.T) { assertNoRequestBacking842(t, root, state.ToolWinners) })
	t.Run("tool_names", func(t *testing.T) { assertNoRequestBacking842(t, root, state.ToolNames) })
}
