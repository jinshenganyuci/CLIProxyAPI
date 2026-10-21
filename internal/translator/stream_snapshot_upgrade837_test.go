package translator

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	agclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/claude"
	codexclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/claude"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	"github.com/tidwall/gjson"
)

func toolStarts837(chunks [][]byte) []string {
	var names []string
	for _, chunk := range chunks {
		for _, line := range bytes.Split(chunk, []byte("\n")) {
			p := gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: ")))
			if p.Get("type").String() == "content_block_start" && p.Get("content_block.type").String() == "tool_use" {
				names = append(names, p.Get("content_block.name").String())
			}
		}
	}
	return names
}

func TestUpgrade837InterleavedToolNameSnapshots(t *testing.T) {
	t.Run("gemini", func(t *testing.T) {
		requests := [][]byte{[]byte(`{"tools":[{"name":"Read","input_schema":{"type":"object"}}]}`), []byte(`{"tools":[{"name":"read","input_schema":{"type":"object"}}]}`)}
		var states [2]any
		for i := range states {
			geminiclaude.ConvertGeminiResponseToClaude(t.Context(), "", requests[i], nil, []byte(`{"candidates":[{"content":{"parts":[{"text":"ready"}]}}]}`), &states[i])
		}
		for i, want := range []string{"Read", "read"} {
			// Once initialized, later caller buffers cannot alter the name snapshot.
			copy(requests[i], bytes.Repeat([]byte("!"), len(requests[i])))
			out := geminiclaude.ConvertGeminiResponseToClaude(t.Context(), "", requests[1-i], nil, []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"READ","args":{"literal":"keep"}}}]},"finishReason":"STOP"}]}`), &states[i])
			if names := toolStarts837(out); len(names) != 1 || names[0] != want {
				t.Fatalf("stream %d tool snapshot changed: %q", i, names)
			}
		}
	})
	t.Run("antigravity", func(t *testing.T) {
		requests := [][]byte{[]byte(`{"tools":[{"name":"tool a","input_schema":{"type":"object"}}]}`), []byte(`{"tools":[{"name":"tool/a","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}]}`)}
		translated := [][]byte{[]byte(`{"model":"gemini-first","request":{}}`), []byte(`{"model":"gemini-second","request":{}}`)}
		var states [2]any
		for i := range states {
			agclaude.ConvertAntigravityResponseToClaude(t.Context(), "", requests[i], translated[i], []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"ready"}]}}]}}`), &states[i])
		}
		for i, want := range []string{"tool a", "tool/a"} {
			out := agclaude.ConvertAntigravityResponseToClaude(t.Context(), "", requests[1-i], translated[1-i], []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"tool_a","args":{"literal":"keep"}}}]}}]}}`), &states[i])
			if names := toolStarts837(out); len(names) != 1 || names[0] != want {
				t.Fatalf("stream %d tool snapshot changed: %q", i, names)
			}
			p := states[i].(*agclaude.Params)
			if p.ModelName != []string{"gemini-first", "gemini-second"}[i] || p.WebSearchStreamMode {
				t.Fatalf("stream %d model/search metadata crossed streams", i)
			}
		}

		var searchState any
		searchRequest := []byte(`{"model":"gemini-search","request":{"tools":[{"googleSearch":{}}]}}`)
		for _, translatedRequest := range [][]byte{searchRequest, []byte(`{}`)} {
			agclaude.ConvertAntigravityResponseToClaude(t.Context(), "", requests[1], translatedRequest, []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"buffered"}]}}]}}`), &searchState)
			p := searchState.(*agclaude.Params)
			if !p.WebSearchStreamMode || p.ModelName != "gemini-search" {
				t.Fatal("search-mode snapshot changed after initialization")
			}
		}

	})
	t.Run("codex_lazy", func(t *testing.T) {
		short := strings.Repeat("x", 64)
		names := []string{short + "-first", short + "-second"}
		requests := [][]byte{[]byte(fmt.Sprintf(`{"tools":[{"name":%q}]}`, names[0])), []byte(fmt.Sprintf(`{"tools":[{"name":%q}]}`, names[1]))}
		var states [2]any
		for i := range states {
			codexclaude.ConvertCodexResponseToClaude(t.Context(), "", nil, nil, []byte(`data: {"type":"response.created","response":{"id":"snapshot"}}`), &states[i])
			if states[i].(*codexclaude.ConvertCodexResponseToClaudeParams).ToolNamesResolved {
				t.Fatal("text-only startup eagerly resolved tool declarations")
			}
		}
		for round := 0; round < 2; round++ {
			for i := range states {
				req := requests[i]
				if round == 1 {
					req = requests[1-i]
				}
				chunk := []byte(fmt.Sprintf(`data: {"type":"response.output_item.added","output_index":%d,"item":{"type":"function_call","id":"fc_%d","call_id":"call_%d","name":%q}}`, round, round, round, short))
				out := codexclaude.ConvertCodexResponseToClaude(t.Context(), "", req, nil, chunk, &states[i])
				if got := toolStarts837(out); len(got) != 1 || got[0] != names[i] {
					t.Fatalf("stream %d round %d lazy snapshot changed: %q", i, round, got)
				}
				end := []byte(fmt.Sprintf(`data: {"type":"response.output_item.done","output_index":%d,"item":{"type":"function_call","id":"fc_%d","call_id":"call_%d","name":%q,"arguments":"{}"}}`, round, round, round, short))
				codexclaude.ConvertCodexResponseToClaude(t.Context(), "", req, nil, end, &states[i])
			}
		}
	})
}
