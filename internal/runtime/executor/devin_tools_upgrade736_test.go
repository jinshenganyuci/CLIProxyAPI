package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"google.golang.org/protobuf/encoding/protowire"
)

const upgrade736ExecDescription = "Runs a command, returning output or a session ID for ongoing interaction."
const upgrade736StdinDescription = "Writes characters to an existing unified exec session and returns recent output."

func upgrade736ResponsesToolsPayload(t *testing.T, model string) []byte {
	t.Helper()
	payload, errSet := sjson.SetBytes([]byte(`{
		"input":[{"type":"message","role":"user","content":"hello"}],
		"tools":[
			{"type":"namespace","name":"mcp__codex_app","tools":[
				{"type":"function","name":"automation_update","description":"Update automation","parameters":{"type":"object","properties":{}}},
				{"type":"function","name":"read_resource","description":"Read a resource","parameters":{"type":"object","properties":{}}}
			]},
			{"type":"namespace","name":"other","tools":[
				{"type":"function","name":"automation_update","description":"Unrelated automation","parameters":{"type":"object","properties":{}}}
			]},
			{"type":"function","name":"exec_command","description":"Runs a command, returning output or a session ID for ongoing interaction.","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},
			{"type":"function","name":"write_stdin","description":"Writes characters to an existing unified exec session and returns recent output.","parameters":{"type":"object","properties":{"session_id":{"type":"integer"}}}}
		],
		"tool_choice":{"type":"function","namespace":"mcp__codex_app","name":"automation_update"}
	}`), "model", model)
	if errSet != nil {
		t.Fatal(errSet)
	}
	return payload
}

func TestUpgrade736DevinAliasToolsRemainFilteredOnWire(t *testing.T) {
	for _, model := range []string{"devin/swe-2", "swe-2", "gemini-3-7-flash", "customer-alias"} {
		t.Run(model, func(t *testing.T) {
			payload := upgrade736ResponsesToolsPayload(t, model)
			auth := &cliproxyauth.Auth{ID: "upgrade736-devin-" + model, Provider: "devin", Attributes: map[string]string{"api_key": "synthetic-key"}}
			req, _, _, errPrepare := NewDevinExecutor(&config.Config{}).prepareDevinHTTPRequest(context.Background(), auth,
				cliproxyexecutor.Request{Model: model, Payload: payload},
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload})
			if errPrepare != nil {
				t.Fatalf("prepare request: %v", errPrepare)
			}
			defer func() {
				if errClose := req.Body.Close(); errClose != nil {
					t.Errorf("close request: %v", errClose)
				}
			}()
			flag, wireBody, errFrame := helps.ReadConnectFrame(req.Body)
			if errFrame != nil || flag != 0 {
				t.Fatalf("Connect request frame: flag=%d err=%v", flag, errFrame)
			}
			tools := make(map[string][]string)
			for _, toolBytes := range upgrade736ProtoBytesFields(t, wireBody, 10) {
				names := upgrade736ProtoBytesFields(t, toolBytes, 1)
				descriptions := upgrade736ProtoBytesFields(t, toolBytes, 2)
				parameters := upgrade736ProtoBytesFields(t, toolBytes, 3)
				if len(names) != 1 || len(descriptions) != 1 || len(parameters) != 1 {
					t.Fatalf("tool missing name, description or parameters: %x", toolBytes)
				}
				name := string(names[0])
				if _, exists := tools[name]; exists {
					t.Fatalf("duplicate wire tool %q", name)
				}
				tools[name] = []string{string(descriptions[0]), string(parameters[0])}
			}
			if len(tools) != 4 {
				t.Fatalf("wire tools = %v, want four retained tools", tools)
			}
			if _, exists := tools["mcp__codex_app__automation_update"]; exists {
				t.Fatal("filtered automation reached Devin wire request")
			}
			for name, want := range map[string]string{
				"mcp__codex_app__read_resource": "Read a resource",
				"other__automation_update":      "Unrelated automation",
				"exec_command":                  "Runs a command, returning output or an session ID for ongoing interaction.",
				"write_stdin":                   "Writes characters to a existing unified exec session and returns recent output.",
			} {
				got := tools[name]
				if len(got) != 2 || got[0] != want {
					t.Errorf("wire tool %q = %v, want description %q", name, got, want)
				}
			}
			if tool := tools["exec_command"]; len(tool) != 2 || gjson.Get(tool[1], "properties.cmd.type").String() != "string" {
				t.Errorf("exec_command schema changed: %v", tool)
			}
		})
	}
}

func TestUpgrade736GeminiInteractionsPreservesDevinSpecificToolsOnWire(t *testing.T) {
	for _, model := range []string{"gemini-3.1-flash-lite", "customer-gemini-alias"} {
		t.Run(model, func(t *testing.T) {
			type capturedRequest struct {
				path string
				body []byte
				err  error
			}
			captured := make(chan capturedRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				captured <- capturedRequest{path: r.URL.Path, body: body, err: errRead}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"interaction_upgrade736","status":"completed","steps":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
			}))
			defer server.Close()
			auth := &cliproxyauth.Auth{ID: "upgrade736-gemini-" + model, Provider: "gemini-interactions", ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic-key", "base_url": server.URL}}
			payload := upgrade736ResponsesToolsPayload(t, model)
			_, errExecute := NewGeminiInteractionsExecutor(&config.Config{}).Execute(context.Background(), auth,
				cliproxyexecutor.Request{Model: model, Payload: payload},
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatInteractions, OriginalRequest: payload})
			if errExecute != nil {
				t.Fatalf("execute: %v", errExecute)
			}
			got := <-captured
			if got.err != nil || got.path != "/v1beta/interactions" {
				t.Fatalf("upstream request path=%q err=%v", got.path, got.err)
			}
			tools := make(map[string]gjson.Result)
			for _, tool := range gjson.GetBytes(got.body, "tools").Array() {
				tools[tool.Get("name").String()] = tool
			}
			if len(tools) != 5 {
				t.Errorf("retained tools = %d, want five; body=%s", len(tools), got.body)
			}
			for name, want := range map[string]string{
				"mcp__codex_app__automation_update": "Update automation",
				"mcp__codex_app__read_resource":     "Read a resource",
				"other__automation_update":          "Unrelated automation",
				"exec_command":                      upgrade736ExecDescription,
				"write_stdin":                       upgrade736StdinDescription,
			} {
				if desc := tools[name].Get("description").String(); desc != want {
					t.Errorf("upstream tool %q description=%q, want %q", name, desc, want)
				}
			}
			if name := gjson.GetBytes(got.body, "generation_config.tool_choice.name").String(); name != "mcp__codex_app__automation_update" {
				t.Errorf("tool_choice name = %q, want retained qualified automation tool", name)
			}
			if tools["exec_command"].Get("parameters.properties.cmd.type").String() != "string" {
				t.Error("exec_command schema was changed")
			}
		})
	}
}

func upgrade736ProtoBytesFields(t *testing.T, body []byte, wanted protowire.Number) [][]byte {
	t.Helper()
	var values [][]byte
	for len(body) > 0 {
		number, wireType, n := protowire.ConsumeTag(body)
		if n < 0 {
			t.Fatalf("invalid protobuf tag: %v", protowire.ParseError(n))
		}
		body = body[n:]
		if number == wanted {
			if wireType != protowire.BytesType {
				t.Fatalf("field %d type = %d, want bytes", number, wireType)
			}
			value, size := protowire.ConsumeBytes(body)
			if size < 0 {
				t.Fatalf("invalid protobuf bytes: %v", protowire.ParseError(size))
			}
			values = append(values, value)
		}
		size := protowire.ConsumeFieldValue(number, wireType, body)
		if size < 0 {
			t.Fatalf("invalid protobuf field: %v", protowire.ParseError(size))
		}
		body = body[size:]
	}
	return values
}
