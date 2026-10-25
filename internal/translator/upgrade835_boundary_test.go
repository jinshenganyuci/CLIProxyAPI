package translator

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	codexgemini "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/gemini"
	codexinteractions "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/interactions"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
	"github.com/tidwall/gjson"
)

func TestUpgrade835ReasoningRoundTripFamilyIsolation(t *testing.T) {
	gptBytes := make([]byte, 89)
	gptBytes[0] = 0x80
	for i := 9; i < len(gptBytes); i++ {
		gptBytes[i] = byte(i)
	}
	grokBytes := make([]byte, 135)
	for i := range grokBytes {
		grokBytes[i] = byte(i*73 + 19)
	}
	families := []struct{ model, signature string }{
		{"openai-compatible/muse-spark-1.3", "Q-PaDgE" + strings.Repeat("A", 1419)},
		{"openrouter/openai/gpt-5.6", base64.RawURLEncoding.EncodeToString(gptBytes)},
		{"xai/grok-4", base64.RawStdEncoding.EncodeToString(grokBytes)},
	}
	for _, family := range families {
		for _, routed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/routed=%t", family.model, routed), func(t *testing.T) {
				sig := family.signature
				if routed {
					sig += ".PrivateRouter830"
				}
				response := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"same-id","model":%q,"output":[{"type":"reasoning","id":"same-item","encrypted_content":%q,"summary":[{"type":"summary_text","text":"hidden"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13,"input_tokens_details":{"cached_tokens":4}}}}`, family.model, sig, "literal "+sig))
				for _, ingress := range []string{"gemini", "interactions"} {
					var replay []byte
					if ingress == "gemini" {
						converted := codexgemini.ConvertCodexResponseToGeminiNonStream(t.Context(), family.model, nil, nil, response, nil)
						replay = []byte(fmt.Sprintf(`{"model":%q,"contents":[%s]}`, family.model, gjson.GetBytes(converted, "candidates.0.content").Raw))
					} else {
						converted := codexinteractions.ConvertCodexResponseToInteractionsNonStream(t.Context(), family.model, nil, nil, response, nil)
						replay = []byte(fmt.Sprintf(`{"model":%q,"input":%s}`, family.model, gjson.GetBytes(converted, "steps").Raw))
					}
					if !json.Valid(replay) {
						t.Fatalf("invalid %s replay", ingress)
					}
					for _, target := range families {
						out := translator.Request(ingress, "codex", target.model, replay, false)
						var reasoning []gjson.Result
						for _, item := range gjson.GetBytes(out, "input").Array() {
							if item.Get("type").String() == "reasoning" {
								reasoning = append(reasoning, item)
							}
						}
						if family.model == target.model {
							if len(reasoning) != 1 || reasoning[0].Get("encrypted_content").String() != sig || reasoning[0].Get("summary.0.text").String() != "hidden" {
								t.Fatalf("%s same-family replay changed signature/summary", ingress)
							}
						} else if len(reasoning) != 0 {
							t.Fatalf("%s replay crossed from %s to %s", ingress, family.model, target.model)
						}
						if !bytes.Contains(out, []byte("literal "+sig)) {
							t.Fatalf("%s signature-looking visible text was changed", ingress)
						}
					}
				}
			})
		}
	}
}

func TestUpgrade835SchemaReferencesAndOriginalArgumentContract(t *testing.T) {
	original := []byte(`{"tools":[{"name":"Inspect","input_schema":{"type":"object","properties":{"nested":{"$ref":"#/$defs/object"},"contentMediaType":{"type":"string"}},"$defs":{"object":{"type":"object","properties":{"readOnly":{"type":"string"},"writeOnly":{"type":"string"},"contentEncoding":{"type":"string"},"reason":{"type":"string","readOnly":true,"contentMediaType":"text/plain","contentEncoding":"base64"}},"additionalProperties":false}}}},{"name":"Empty","input_schema":{"type":"object","properties":{},"additionalProperties":false}}],"messages":[{"role":"user","content":"inspect"}]}`)
	translated, errTranslate := geminiclaude.ConvertClaudeRequestToGemini("gemini-test", original, false)
	if errTranslate != nil {
		t.Fatal(errTranslate)
	}
	schema := gjson.GetBytes(translated, "tools.0.functionDeclarations.0.parametersJsonSchema")
	if !schema.Exists() {
		schema = gjson.GetBytes(translated, "tools.0.functionDeclarations.0.parameters")
	}
	nested := schema.Get("properties.nested")
	if nested.Get("$ref").Exists() || nested.Get("properties.reason.readOnly").Exists() || nested.Get("properties.reason.contentMediaType").Exists() || nested.Get("properties.reason.contentEncoding").Exists() || nested.Get("properties.readOnly.type").String() != "string" {
		t.Fatalf("local ref/schema keyword boundary changed: %s", translated)
	}
	args := `{"nested":{"readOnly":"literal-read","writeOnly":"literal-write","contentEncoding":"literal-encoding","reason":"declared reason"},"contentMediaType":"literal-media"}`
	raw := []byte(fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"Inspect","args":%s}},{"functionCall":{"name":"Empty","args":{"reason":"synthetic"}}}]},"finishReason":"STOP"}]}`, args))
	out := geminiclaude.ConvertGeminiResponseToClaudeNonStream(t.Context(), "gemini-test", original, translated, raw, nil)
	if gjson.GetBytes(out, "content.0.input").Raw != args || gjson.GetBytes(out, "content.1.input").Raw != "{}" {
		t.Fatalf("original declarations did not distinguish literal values and synthetic placeholders: %s", out)
	}
}

func TestUpgrade835OpenAIClaudeStreamCachesAreRequestLocal(t *testing.T) {
	const workers = 16
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			name := "Bash"
			if i%2 == 1 {
				name = "BASH"
			}
			request := []byte(fmt.Sprintf(`{"stream":true,"tools":[{"name":%q}]}`, name))
			var state any
			first := []byte(`data: {"id":"shared","choices":[{"delta":{"content":"hello"}}]}`)
			openaiclaude.ConvertOpenAIResponseToClaude(t.Context(), "model", request, nil, first, &state)
			second := []byte(`data: {"id":"shared","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"bash","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
			out := bytes.Join(openaiclaude.ConvertOpenAIResponseToClaude(t.Context(), "model", nil, nil, second, &state), nil)
			if !bytes.Contains(out, []byte(`"name":"`+name+`"`)) {
				t.Errorf("stream %d reused another request's tool map: %s", i, out)
			}
			var nonstream any
			plain := bytes.Join(openaiclaude.ConvertOpenAIResponseToClaude(t.Context(), "model", []byte(`{"stream":false}`), nil, first, &nonstream), nil)
			if gjson.GetBytes(plain, "type").String() != "message" {
				t.Errorf("stream %d leaked streaming mode into new request", i)
			}
		})
	}
	wg.Wait()
}
