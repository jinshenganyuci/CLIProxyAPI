package test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestUpgrade848CapsuleExpansionWithCommentaryAndLiteralReplay(t *testing.T) {
	capsule, err := helps.SealAntigravityCompaction("summary with cpa:commentary:v1 literal", "claude-sonnet-4-6")
	if err != nil {
		t.Fatal(err)
	}
	input := []byte(fmt.Sprintf(`{"input":[
		{"type":"compaction","encrypted_content":%q},
		{"type":"compaction","encrypted_content":"foreign-native-opaque"},
		{"type":"reasoning","encrypted_content":"cpa:commentary:v1","summary":[{"type":"summary_text","text":"commentary restored"}]},
		{"type":"function_call","call_id":"call_1","name":"inspect","arguments":"{\"type\":\"compaction\",\"encrypted_content\":\"cpa-ag-compact-v1:bad-literal\",\"n\":9007199254740993}"},
		{"type":"function_call_output","call_id":"call_1","output":"cpa-ag-compact-v1:bad-literal cpa:commentary:v1"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"cpa-ag-compact-v1:bad-literal"}]},
		{"type":"reasoning","encrypted_content":"foreign-opaque-reasoning","summary":[]}
	]}`, capsule))
	before := bytes.Clone(input)
	req := cliproxyexecutor.Request{Payload: input}
	opts := cliproxyexecutor.Options{OriginalRequest: bytes.Clone(input)}
	if err := helps.ExpandRecognizedResponsesCompactionCapsules(&req, &opts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(input, before) || !bytes.Equal(req.Payload, opts.OriginalRequest) {
		t.Fatal("expansion changed caller bytes or diverged from the original-request snapshot")
	}
	for _, target := range []sdktranslator.Format{sdktranslator.FormatCodex, sdktranslator.FormatClaude} {
		t.Run(string(target), func(t *testing.T) {
			model := "gpt-6-astra"
			if target == sdktranslator.FormatClaude {
				model = "claude-sonnet-4-6"
			}
			out := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, target, model, req.Payload, false)
			for _, text := range []string{"Context summary from previous turns:", "summary with cpa:commentary:v1 literal", "commentary restored", "cpa-ag-compact-v1:bad-literal", "9007199254740993"} {
				if !bytes.Contains(out, []byte(text)) {
					t.Fatalf("missing replay content %q: %s", text, out)
				}
			}
			if target == sdktranslator.FormatCodex {
				items := gjson.GetBytes(out, "input").Array()
				if len(items) != 7 || items[0].Get("role").String() != "developer" || items[1].Get("encrypted_content").String() != "foreign-native-opaque" || items[2].Get("role").String() != "assistant" || items[2].Get("phase").String() != "commentary" || items[2].Get("encrypted_content").Exists() || items[6].Get("encrypted_content").String() != "foreign-opaque-reasoning" {
					t.Fatalf("typed/opaque boundary changed: %s", out)
				}
			}
		})
	}
	bad := cliproxyexecutor.Request{Payload: []byte(`{"input":[{"type":"compaction","id":"cmp_ag_compact_expected","encrypted_content":"foreign-invalid"}]}`)}
	if err := helps.ExpandRecognizedResponsesCompactionCapsules(&bad, nil); err == nil {
		t.Fatal("a CPA-identified corrupted control item must fail closed")
	}
}

func TestUpgrade848ParallelToolResultsKeepErrorAndLiteralBoundaries(t *testing.T) {
	request := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_a","name":"first","input":{}},{"type":"tool_use","id":"call_b","name":"second","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_b","is_error":"true","content":{"error":"literal","is_error":true,"n":9007199254740993}},{"type":"text","text":"cpa:commentary:v1"},{"type":"tool_result","tool_use_id":"call_a","is_error":true,"content":[{"type":"text","text":"failed"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}]}`)
	before := bytes.Clone(request)
	for _, target := range []sdktranslator.Format{sdktranslator.FormatGemini, sdktranslator.FormatAntigravity} {
		t.Run(string(target), func(t *testing.T) {
			out := sdktranslator.TranslateRequest(sdktranslator.FormatClaude, target, "gemini-4-argon", request, false)
			root := gjson.ParseBytes(out)
			if root.Get("request").Exists() {
				root = root.Get("request")
			}
			results := map[string]gjson.Result{}
			for _, content := range root.Get("contents").Array() {
				for _, part := range content.Get("parts").Array() {
					if fr := part.Get("functionResponse"); fr.Exists() {
						results[fr.Get("name").String()] = fr.Get("response")
					}
				}
			}
			if len(results) != 2 || results["first"].Get("error.text").String() != "failed" || results["first"].Get("result").Exists() || results["second"].Get("error").Exists() || results["second"].Get("result.n").Raw != "9007199254740993" || results["second"].Get("result.is_error").Type != gjson.True {
				t.Fatalf("tool identity/error/literal boundary changed: %s", out)
			}
			if !strings.Contains(string(out), "aGVsbG8=") || !strings.Contains(string(out), "cpa:commentary:v1") || !bytes.Equal(request, before) {
				t.Fatalf("image, text, or caller bytes changed: %s", out)
			}
		})
	}
}
