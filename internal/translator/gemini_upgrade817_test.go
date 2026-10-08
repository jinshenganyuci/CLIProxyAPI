package translator

import (
	"bytes"
	"testing"

	claude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/gemini"
	gemini "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	"github.com/tidwall/gjson"
)

func TestUpgrade817GeminiDocumentSchemaAndToolHistoryRemainSeparated(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjQK"}},{"type":"text","text":"Read document"}]},{"role":"assistant","content":[{"type":"tool_use","id":"call-original","name":"read","input":{"properties":{"keep":false},"required":["literal"],"items":{"count":9007199254740993}}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-original","content":"done"}]}],"tools":[{"name":"read","input_schema":{"properties":{"endpoint":{"type":["string","object"],"properties":{"traces":{"type":"string"}},"required":["traces"]},"items":{"type":"string"}},"required":["endpoint","items"]}}],"tool_choice":{"type":"tool","name":"read"}}`)
	before := bytes.Clone(raw)
	for _, stream := range []bool{false, true} {
		out, errConvert := gemini.ConvertClaudeRequestToGemini("gemini-test", raw, stream)
		if errConvert != nil {
			t.Fatalf("request translation failed: %v", errConvert)
		}
		if !bytes.Equal(raw, before) {
			t.Fatal("translator changed the input document")
		}
		if gjson.GetBytes(out, "contents.0.parts.0.inline_data.mime_type").String() != "application/pdf" || gjson.GetBytes(out, "contents.0.parts.0.inline_data.data").String() != "JVBERi0xLjQK" {
			t.Fatalf("document bytes or MIME changed: %s", out)
		}
		arguments := gjson.GetBytes(out, "contents.1.parts.0.functionCall.args")
		if arguments.Get("properties.keep").Raw != "false" || arguments.Get("required.0").String() != "literal" || arguments.Get("items.count").Raw != "9007199254740993" || arguments.Get("type").Exists() {
			t.Fatalf("schema cleanup mutated history arguments: %s", out)
		}
		schema := gjson.GetBytes(out, "tools.0.functionDeclarations.0.parametersJsonSchema")
		if schema.Get("type").String() != "object" || schema.Get("properties.endpoint.type").String() != "object" || schema.Get("properties.endpoint.required.0").String() != "traces" || schema.Get("properties.items.type").String() != "string" {
			t.Fatalf("nested schema object inference or keyword-named property changed: %s", out)
		}
		if gjson.GetBytes(out, "toolConfig.functionCallingConfig.allowedFunctionNames.0").String() != "read" {
			t.Fatalf("declared tool selection lost its binding: %s", out)
		}
	}
}

func TestUpgrade817GeminiSystemInstructionCamelCasePresenceWins(t *testing.T) {
	for _, test := range []struct {
		name, camel, want string
	}{
		{"text", `{"parts":[{"text":"canonical"}]}`, "canonical"},
		{"explicit empty", `{"parts":[]}`, ""},
		{"explicit null", `null`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(`{"systemInstruction":` + test.camel + `,"system_instruction":{"parts":[{"text":"legacy-must-not-override"}]},"contents":[{"role":"user","parts":[{"text":"question"}]}]}`)
			before := bytes.Clone(raw)
			out, errConvert := claude.ConvertGeminiRequestToClaude("claude-test", raw, false)
			if errConvert != nil {
				t.Fatalf("request translation failed: %v", errConvert)
			}
			messages := gjson.GetBytes(out, "messages").Array()
			wantCount := 1
			if test.want != "" {
				wantCount++
			}
			if len(messages) != wantCount || !bytes.Equal(raw, before) {
				t.Fatalf("canonical system instruction presence changed message count or input: %s", out)
			}
			if test.want != "" && messages[0].Get("content.0.text").String() != test.want {
				t.Fatalf("canonical system instruction was lost: %s", out)
			}
			if messages[len(messages)-1].Get("content.0.text").String() != "question" || bytes.Contains(out, []byte("legacy-must-not-override")) {
				t.Fatalf("legacy instruction overrode a present canonical instruction: %s", out)
			}
		})
	}
}
